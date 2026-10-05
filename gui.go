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

const repoURL = "https://github.com/CristiancMartini/angler-otimizador"

// displayPath mostra a pasta do usuário como %USERPROFILE% (prints sem o nome do usuário).
func displayPath(p string) string {
	if home := os.Getenv("USERPROFILE"); home != "" && len(p) >= len(home) && strings.EqualFold(p[:len(home)], home) {
		return "%USERPROFILE%" + p[len(home):]
	}
	return p
}

// runGUI abre a janela (WebView2, que já vem no Windows 10/11).
func runGUI(env Env) error {
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(filepath.Dir(configPath()), "webview"),
		WindowOptions: webview2.WindowOptions{
			Title:  "Angler Otimizador",
			Width:  1180,
			Height: 760,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		return errors.New("WebView2 indisponível")
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	makeFrameless(hwnd, 1180, 760)

	// As funções chamadas pelo JavaScript rodam na thread da janela; o trabalho
	// pesado vai para goroutines e o resultado volta por Dispatch.
	call := func(fn string, v interface{}) {
		b, _ := json.Marshal(v)
		w.Dispatch(func() { w.Eval("window." + fn + " && window." + fn + "(" + string(b) + ")") })
	}
	push := func(level, msg string) { call("onLog", []string{level, msg}) }

	var mapsMu sync.Mutex
	var maps []MapInfo
	go func() {
		m := analyzeMaps(env.GameDir)
		mapsMu.Lock()
		maps = m
		mapsMu.Unlock()
		call("onMaps", m)
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
		if !strings.HasPrefix(u, repoURL) {
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
