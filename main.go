// Tacklebox: launcher e estatísticas para Call of the Wild: The Angler.
//
// Sem parâmetros abre a interface. Pelo terminal:
//
//	Tacklebox.exe --auto       aplica as opções salvas e abre o jogo, sem perguntar nada
//	Tacklebox.exe --desfazer   volta tudo como estava
//
// O que ele faz está em core.go; os detalhes do jogo em ini.go e vegmod.go.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const appVersion = "2.1.0"

var (
	flagAuto         = flag.Bool("auto", false, "aplica as opções salvas e abre o jogo, sem interface")
	flagDesfazer     = flag.Bool("desfazer", false, "volta tudo como estava, sem interface")
	flagIni          = flag.String("ini", "", "caminho alternativo do settings.ini (testes)")
	flagTeste        = flag.Bool("teste", false, "não mexe no registro e não abre o jogo (testes)")
	flagIgnorarAbert = flag.Bool("ignorar-jogo-aberto", false, "não espera o jogo fechar (testes)")
	flagSaidaMod     = flag.String("saida-mod", "", "pasta onde gravar o pacote do mod (testes)")
	flagStatsJSON    = flag.Bool("stats-json", false, "imprime as estatísticas lidas do save (diagnóstico)")
	flagAnonimo      = flag.Bool("anonimo", false, "não mostra nome e avatar da Steam (para prints)")
	flagGuideJSON    = flag.Bool("guia-json", false, "imprime o guia de peixes lido do jogo (diagnóstico)")
	flagAoVivo       = flag.Bool("ao-vivo", false, "mostra por 20 s o que o overlay lê do jogo aberto (diagnóstico)")
	flagOverlayTeste = flag.Bool("overlay-sempre", false, "mostra o overlay mesmo sem o jogo em primeiro plano (testes)")
	flagFoto         = flag.String("foto", "", "salva a foto de um peixe (nome do ícone) como PNG na pasta atual (diagnóstico)")
)

func main() {
	flag.Parse()
	env := detectEnv(*flagIni, *flagSaidaMod, *flagTeste)

	if *flagStatsJSON {
		attachConsole()
		st := readStats(env)
		st.Player.Avatar = fmt.Sprintf("(%d bytes)", len(st.Player.Avatar))
		b, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(b))
		return
	}
	if *flagAoVivo {
		attachConsole()
		start := time.Now()
		for time.Since(start) < 20*time.Second {
			b, _ := json.Marshal(readLive(env))
			fmt.Printf("%5.1fs %s\n", time.Since(start).Seconds(), b)
			time.Sleep(time.Second)
		}
		return
	}
	if *flagFoto != "" {
		attachConsole()
		for _, size := range []string{"small", "medium"} {
			u := fishImage(env.GameDir, *flagFoto, size)
			if i := strings.Index(u, ","); i >= 0 {
				b, _ := base64.StdEncoding.DecodeString(u[i+1:])
				os.WriteFile(*flagFoto+"_"+size+".png", b, 0o644)
			}
			fmt.Println(size, len(u))
		}
		return
	}
	if *flagGuideJSON {
		attachConsole()
		b, _ := json.MarshalIndent(buildGuide(env.GameDir), "", "  ")
		fmt.Println(string(b))
		return
	}

	if !*flagAuto && !*flagDesfazer {
		if err := runGUI(env); err == nil {
			return
		}
		// sem WebView2 (Windows muito antigo): cai no modo automático
		messageBox("Tacklebox", "Não consegui abrir a interface (WebView2 ausente).\nVou aplicar as otimizações no modo automático.")
		*flagAuto = true
	}
	runConsole(env)
}

func runConsole(env Env) {
	attachConsole()
	fmt.Println("==============================================")
	fmt.Println("   Tacklebox " + appVersion)
	fmt.Println("==============================================")
	fmt.Println()

	if !*flagIgnorarAbert && gameRunning() {
		fmt.Println("O jogo está aberto. Feche o jogo que eu continuo sozinho...")
		for gameRunning() {
			time.Sleep(2 * time.Second)
		}
	}

	log := func(level, msg string) {
		tag := map[string]string{"ok": "[ok]", "info": "  - ", "warn": "[!] ", "err": "[x] "}[level]
		fmt.Println(" ", tag, msg)
	}

	var ok bool
	if *flagDesfazer {
		fmt.Println("Desfazendo...")
		ok = Undo(env, log)
	} else {
		o := loadOptions()
		fmt.Println("Aplicando otimizações...")
		ok = Apply(env, o, log)
		if !env.Test {
			fmt.Println()
			launchGame(o.Priority, log)
		}
	}

	fmt.Println()
	if ok {
		fmt.Println("Pronto! Esta janela fecha sozinha.")
		time.Sleep(6 * time.Second)
	} else {
		fmt.Println("Terminou com avisos (veja acima). Fecha em 30 segundos.")
		time.Sleep(30 * time.Second)
	}
}

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONINFORMATION)
	_ = os.Stdout
}
