package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	steamAppID  = "1408610"
	gameExeName = "CotWTheAngler_Steam.exe"
	gameDirName = "Call of the Wild The Angler"
	stateName   = "otimizador-estado.txt"
	gpuPrefKey  = `Software\Microsoft\DirectX\UserGpuPreferences`
)

// ---------- estado para desfazer os ajustes do Windows ----------

func statePath(iniPath string) string { return filepath.Join(filepath.Dir(iniPath), stateName) }

func readState(iniPath string) map[string]string {
	st := map[string]string{}
	b, err := os.ReadFile(statePath(iniPath))
	if err != nil {
		return st
	}
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			st[k] = v
		}
	}
	return st
}

// rememberOnce guarda o valor original só na primeira vez.
func rememberOnce(iniPath, key, val string) error {
	if _, ok := readState(iniPath)[key]; ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(statePath(iniPath)), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(statePath(iniPath), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s=%s\n", key, val)
	return err
}

func windowsTweaksApplied(iniPath string) bool { return len(readState(iniPath)) > 0 }

// ---------- ajustes do Windows (só do usuário atual, sem administrador) ----------

func setHighPerfGPU(iniPath, gameExe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, gpuPrefKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	prev, _, perr := k.GetStringValue(gameExe)
	if perr != nil {
		prev = "<nenhum>"
	}
	if err := rememberOnce(iniPath, "gpu|"+gameExe, prev); err != nil {
		return err
	}
	return k.SetStringValue(gameExe, "GpuPreference=2;")
}

var dvrValues = []struct{ path, name string }{
	{`System\GameConfigStore`, "GameDVR_Enabled"},
	{`Software\Microsoft\Windows\CurrentVersion\GameDVR`, "AppCaptureEnabled"},
}

func disableGameDVR(iniPath string) error {
	for _, d := range dvrValues {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, d.path, registry.QUERY_VALUE|registry.SET_VALUE)
		if err != nil {
			return err
		}
		prev := "<nenhum>"
		if v, _, err := k.GetIntegerValue(d.name); err == nil {
			prev = strconv.FormatUint(v, 10)
		}
		if err := rememberOnce(iniPath, "dword|"+d.path+"|"+d.name, prev); err != nil {
			k.Close()
			return err
		}
		err = k.SetDWordValue(d.name, 0)
		k.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func restoreRegistry(iniPath string) {
	st := readState(iniPath)
	// formato da versão 0.x (gpu_exe / gpu_prev_existed / gpu_prev)
	if exe := st["gpu_exe"]; exe != "" {
		prev := "<nenhum>"
		if st["gpu_prev_existed"] == "1" {
			prev = st["gpu_prev"]
		}
		st["gpu|"+exe] = prev
	}
	for key, prev := range st {
		parts := strings.Split(key, "|")
		switch {
		case parts[0] == "gpu" && len(parts) == 2:
			if k, err := registry.OpenKey(registry.CURRENT_USER, gpuPrefKey, registry.SET_VALUE); err == nil {
				if prev == "<nenhum>" {
					k.DeleteValue(parts[1])
				} else {
					k.SetStringValue(parts[1], prev)
				}
				k.Close()
			}
		case parts[0] == "dword" && len(parts) == 3:
			if k, err := registry.OpenKey(registry.CURRENT_USER, parts[1], registry.SET_VALUE); err == nil {
				if prev == "<nenhum>" {
					k.DeleteValue(parts[2])
				} else if v, err := strconv.ParseUint(prev, 10, 32); err == nil {
					k.SetDWordValue(parts[2], uint32(v))
				}
				k.Close()
			}
		}
	}
	os.Remove(statePath(iniPath))
}

// ---------- localizar o jogo ----------

var vdfPathRe = regexp.MustCompile(`"path"\s+"([^"]+)"`)

func findGameExe() string {
	var cands []string
	if self, err := os.Executable(); err == nil {
		d := filepath.Dir(self)
		cands = append(cands, filepath.Join(d, gameExeName), filepath.Join(filepath.Dir(d), gameExeName))
	}
	var steamRoots []string
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE); err == nil {
		if p, _, err := k.GetStringValue("SteamPath"); err == nil {
			steamRoots = append(steamRoots, filepath.FromSlash(p))
		}
		k.Close()
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, registry.QUERY_VALUE); err == nil {
		if p, _, err := k.GetStringValue("InstallPath"); err == nil {
			steamRoots = append(steamRoots, p)
		}
		k.Close()
	}
	for _, root := range steamRoots {
		libs := []string{root}
		if b, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf")); err == nil {
			for _, m := range vdfPathRe.FindAllStringSubmatch(string(b), -1) {
				libs = append(libs, strings.ReplaceAll(m[1], `\\`, `\`))
			}
		}
		for _, lib := range libs {
			cands = append(cands, filepath.Join(lib, "steamapps", "common", gameDirName, gameExeName))
		}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			if abs, err := filepath.Abs(c); err == nil {
				return abs
			}
			return c
		}
	}
	return ""
}

// ---------- processo do jogo ----------

func findProcess(name string) (uint32, bool) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), name) {
			return e.ProcessID, true
		}
	}
	return 0, false
}

func gameRunning() bool {
	_, r := findProcess(gameExeName)
	return r
}

func openURL(u string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	p, _ := windows.UTF16PtrFromString(u)
	return windows.ShellExecute(0, verb, p, nil, nil, windows.SW_SHOWNORMAL)
}

// launchGame abre o jogo pela Steam e, se pedido, dá prioridade "acima do normal"
// quando o processo aparecer (até 5 minutos).
func launchGame(priority bool, log logFn) {
	if err := openURL("steam://rungameid/" + steamAppID); err != nil {
		log("err", "Não consegui abrir pela Steam: "+err.Error())
		return
	}
	log("info", "Abrindo o jogo pela Steam...")
	if !priority {
		return
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if pid, running := findProcess(gameExeName); running {
			time.Sleep(15 * time.Second)
			h, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, pid)
			if err == nil {
				err = windows.SetPriorityClass(h, windows.ABOVE_NORMAL_PRIORITY_CLASS)
				windows.CloseHandle(h)
			}
			if err == nil {
				log("ok", "Jogo aberto com prioridade acima do normal. Bom jogo!")
			} else {
				log("warn", "Jogo aberto (não consegui mudar a prioridade).")
			}
			return
		}
		time.Sleep(2 * time.Second)
	}
	log("warn", "O jogo não abriu em 5 minutos; pode abrir pela Steam normalmente.")
}

// ---------- console (modo sem interface) ----------

// attachConsole garante uma janela de texto para o modo sem interface (o exe é
// compilado como programa gráfico). Se a saída já foi redirecionada, mantém.
func attachConsole() {
	k32 := windows.NewLazySystemDLL("kernel32.dll")
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && h != 0 && h != windows.InvalidHandle {
		if t, _ := windows.GetFileType(h); t == windows.FILE_TYPE_PIPE || t == windows.FILE_TYPE_DISK {
			return
		}
	}
	const attachParent = ^uintptr(0) // ATTACH_PARENT_PROCESS
	if r, _, _ := k32.NewProc("AttachConsole").Call(attachParent); r == 0 {
		k32.NewProc("AllocConsole").Call()
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	k32.NewProc("SetConsoleOutputCP").Call(65001)
}
