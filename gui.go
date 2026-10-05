package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

//go:embed ui/index.html
var uiHTML string

const repoURL = "https://github.com/CristiancMartini/tacklebox"

// displayPath mostra a pasta do usuário como %USERPROFILE% (prints sem o nome do usuário).
func displayPath(p string) string {
	if home := os.Getenv("USERPROFILE"); home != "" && len(p) >= len(home) && strings.EqualFold(p[:len(home)], home) {
		return "%USERPROFILE%" + p[len(home):]
	}
	return p
}

// runGUI abre a janela (WebView2, que já vem no Windows 10/11).
func runGUI(env Env) error {
	dataPath := filepath.Join(filepath.Dir(configPath()), "webview")
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  dataPath,
		WindowOptions: webview2.WindowOptions{
			Title:  "Tacklebox",
			Width:  1240,
			Height: 800,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		return errors.New("WebView2 indisponível")
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	makeFrameless(hwnd, 1240, 800)

	// As funções chamadas pelo JavaScript rodam na thread da janela; o trabalho
	// pesado vai para goroutines e o resultado volta por Dispatch.
	call := func(fn string, v interface{}) {
		b, _ := json.Marshal(v)
		w.Dispatch(func() { w.Eval("window." + fn + " && window." + fn + "(" + string(b) + ")") })
	}
	push := func(level, msg string) { call("onLog", []string{level, msg}) }

	// overlay (janela própria, criada escondida) e o guia de peixes
	var shared sync.Mutex
	var ov *overlay
	var guide *Guide
	var stats *Stats
	overlayCall := func(fn string, v interface{}) {
		shared.Lock()
		o := ov
		shared.Unlock()
		if o != nil {
			o.call(fn, v)
		}
	}
	go func() {
		g := buildGuide(env.GameDir)
		shared.Lock()
		guide = &g
		shared.Unlock()
		call("onGuide", g)
		overlayCall("onGuide", g)
	}()
	go func() {
		time.Sleep(800 * time.Millisecond) // deixa a janela principal subir primeiro
		o := startOverlay(dataPath, func(o *overlay) {
			o.w.Bind("getGuide", func() *Guide {
				shared.Lock()
				defer shared.Unlock()
				return guide
			})
			o.w.Bind("getStats", func() *Stats {
				shared.Lock()
				defer shared.Unlock()
				return stats
			})
			o.w.Bind("ovWin", func(action string) {
				switch action {
				case "drag":
					dragWindow(o.hwnd)
				case "hide":
					procShowWindow.Call(o.hwnd, swHide)
				}
			})
		})
		if o == nil {
			push("warn", "Não consegui criar o overlay.")
			return
		}
		shared.Lock()
		ov = o
		shared.Unlock()
		if !listenHotkey(o.toggle) {
			push("warn", "O atalho Ctrl+Shift+G já é usado por outro programa. Abra o overlay pelo botão no Guia.")
		}
	}()

	var mapsMu sync.Mutex
	var maps []MapInfo
	go func() {
		m := analyzeMaps(env.GameDir)
		mapsMu.Lock()
		maps = m
		mapsMu.Unlock()
		call("onMaps", m)
	}()

	// Estatísticas: lê o save e acompanha as mudanças (o jogo salva sozinho
	// de tempos em tempos), mandando a versão nova para a tela.
	go func() {
		var last time.Time
		for {
			_, _, mod := findSave()
			if !mod.Equal(last) {
				last = mod
				st := readStats(env)
				shared.Lock()
				stats = &st
				shared.Unlock()
				call("onStats", st)
				overlayCall("onStats", st)
			}
			time.Sleep(3 * time.Second)
		}
	}()

	var busy sync.Mutex
	background := func(job func() bool, after func()) error {
		if !busy.TryLock() {
			return errors.New("já tem uma operação em andamento")
		}
		go func() {
			defer busy.Unlock()
			call("onBusy", true)
			if !*flagIgnorarAbert && gameRunning() {
				push("warn", "O jogo está aberto. Feche o jogo que eu continuo sozinho...")
				for gameRunning() {
					time.Sleep(2 * time.Second)
				}
			}
			call("onDone", job())
			if after != nil {
				after()
			}
		}()
		return nil
	}

	w.Bind("getInfo", func() map[string]interface{} {
		return map[string]interface{}{
			"version":  appVersion,
			"options":  loadOptions(),
			"gamePath": displayPath(env.GameDir),
			"iniPath":  displayPath(env.IniPath),
			"build":    steamBuild(env.GameDir),
			"repo":     repoURL,
		}
	})
	w.Bind("getBanner", func() string { return gameBanner(env.GameDir) })
	w.Bind("getStatus", func() Status { return getStatus(env) })
	w.Bind("getGraphics", func(scale int) []GraphicsRow { return graphicsTable(env.IniPath, scale) })
	w.Bind("getStats", func() *Stats {
		shared.Lock()
		defer shared.Unlock()
		return stats
	})
	w.Bind("getGuide", func() *Guide {
		shared.Lock()
		defer shared.Unlock()
		return guide
	})
	w.Bind("toggleOverlay", func() error {
		shared.Lock()
		o := ov
		shared.Unlock()
		if o == nil {
			return errors.New("o overlay ainda está carregando")
		}
		o.toggle()
		return nil
	})
	w.Bind("getMaps", func() []MapInfo {
		mapsMu.Lock()
		defer mapsMu.Unlock()
		return maps
	})
	w.Bind("saveOptions", func(o Options) {
		o.normalize()
		saveOptions(o)
	})
	w.Bind("apply", func(o Options, launch bool) error {
		var after func()
		if launch && !env.Test {
			after = func() { launchGame(o.Priority, push) }
		}
		return background(func() bool { return Apply(env, o, push) }, after)
	})
	w.Bind("undo", func() error {
		return background(func() bool { return Undo(env, push) }, nil)
	})
	w.Bind("openURL", func(u string) error {
		if !strings.HasPrefix(u, repoURL) && u != communitySheet {
			return errors.New("link não permitido")
		}
		return openURL(u)
	})
	w.Bind("win", func(action string) {
		switch action {
		case "drag":
			dragWindow(hwnd)
		case "min":
			minimizeWindow(hwnd)
		case "close":
			closeWindow(hwnd)
		}
	})

	w.SetHtml(uiHTML)
	w.Run()
	return nil
}
