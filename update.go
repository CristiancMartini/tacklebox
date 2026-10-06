// Atualização automática.
//
// A cada abertura o Tacklebox pergunta ao GitHub qual é a última versão publicada.
// Se for mais nova, baixa o Tacklebox.exe da release, confere tamanho e SHA-256
// (o GitHub informa os dois), troca o próprio arquivo e reabre. No Windows o .exe
// em uso não pode ser apagado, mas pode ser renomeado: ele vira <nome>.exe.old e a
// versão nova apaga esse arquivo quando abre. Sem internet, ou se algo falhar, o
// programa segue na versão atual.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const (
	updateRepo  = "CristiancMartini/tacklebox"
	updateAsset = "Tacklebox.exe"
)

type releaseAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"` // "sha256:<hex>"
}

type release struct {
	Tag        string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

// versionParts transforma "v2.10.1" em [2 10 1].
func versionParts(v string) []int {
	var out []int
	for _, p := range strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func newerVersion(tag, cur string) bool {
	a, b := versionParts(tag), versionParts(cur)
	if a == nil || b == nil {
		return false
	}
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// updateDisabled: testes, execução pelo "go run"/"go test" e quem pedir para não atualizar.
func updateDisabled(exe string) bool {
	if *flagTeste || *flagSemAtualizar || os.Getenv("TACKLEBOX_NO_UPDATE") == "1" {
		return true
	}
	return strings.Contains(strings.ToLower(exe), "go-build")
}

// latestUpdate devolve a release e o asset se houver versão mais nova que a atual.
func latestUpdate(ctx context.Context) (*release, *releaseAsset, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+updateRepo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Tacklebox/"+appVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("GitHub respondeu %s", resp.Status)
	}
	var r release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, nil, err
	}
	if r.Draft || r.Prerelease || !newerVersion(r.Tag, appVersion) {
		return nil, nil, nil
	}
	for i := range r.Assets {
		a := &r.Assets[i]
		if strings.EqualFold(a.Name, updateAsset) && strings.HasPrefix(a.URL, "https://github.com/"+updateRepo+"/releases/download/") {
			return &r, a, nil
		}
	}
	return nil, nil, fmt.Errorf("a versão %s não tem o %s", r.Tag, updateAsset)
}

// downloadUpdate baixa o asset e confere tamanho, assinatura de executável e SHA-256.
func downloadUpdate(ctx context.Context, a *releaseAsset) ([]byte, error) {
	want, ok := strings.CutPrefix(a.Digest, "sha256:")
	if !ok || len(want) != 64 {
		return nil, errors.New("a release não informa o SHA-256 do arquivo")
	}
	if a.Size < 1<<20 || a.Size > 200<<20 {
		return nil, fmt.Errorf("tamanho inesperado (%d bytes)", a.Size)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
	req.Header.Set("User-Agent", "Tacklebox/"+appVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download respondeu %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != a.Size || !bytes.HasPrefix(b, []byte("MZ")) {
		return nil, errors.New("arquivo baixado incompleto")
	}
	sum := sha256.Sum256(b)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), want) {
		return nil, errors.New("o arquivo baixado não confere com o publicado")
	}
	return b, nil
}

// replaceExe grava a versão nova no lugar do .exe em uso.
func replaceExe(exe string, data []byte) error {
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(old, exe) // devolve a versão atual
		os.Remove(tmp)
		return err
	}
	return nil
}

// selfUpdate procura, baixa e instala uma versão nova. status recebe mensagens curtas
// para a interface. Devolve a versão instalada ("" se nada mudou).
func selfUpdate(status func(level, msg string)) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if updateDisabled(exe) {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	r, a, err := latestUpdate(ctx)
	cancel()
	if err != nil || a == nil {
		return "", err
	}
	ver := strings.TrimPrefix(r.Tag, "v")
	status("info", "Baixando a versão "+ver+" do Tacklebox...")
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	data, err := downloadUpdate(ctx, a)
	cancel()
	if err == nil {
		if err = replaceExe(exe, data); err != nil {
			err = fmt.Errorf("não consegui trocar o arquivo (%v)", err)
		}
	}
	if err != nil {
		// a partir daqui o usuário já viu o aviso, então ele fica sabendo do erro
		status("warn", "Não deu para atualizar agora: "+err.Error()+". Segue na versão "+appVersion+".")
		return "", nil
	}
	return ver, nil
}

// relaunchSelf abre a versão nova com os mesmos parâmetros; ela espera este
// processo fechar antes de começar (por causa dos atalhos e do WebView2).
func relaunchSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var args []string
	for i := 1; i < len(os.Args); i++ {
		if a := os.Args[i]; a == "-esperar-pid" || a == "--esperar-pid" {
			i++ // não repassa o valor antigo
			continue
		} else if strings.HasPrefix(strings.TrimLeft(a, "-"), "esperar-pid=") {
			continue
		}
		args = append(args, os.Args[i])
	}
	args = append(args, "-esperar-pid", strconv.Itoa(os.Getpid()))
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	return cmd.Start()
}

// afterUpdate roda no começo: espera a versão anterior fechar (se veio de uma
// atualização) e apaga o .exe.old que ela deixou.
func afterUpdate() {
	if *flagEsperarPid > 0 {
		if h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(*flagEsperarPid)); err == nil {
			windows.WaitForSingleObject(h, 10000)
			windows.CloseHandle(h)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	old := exe + ".old"
	for i := 0; i < 10 && exists(old); i++ {
		if os.Remove(old) == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
}
