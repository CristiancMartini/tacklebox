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
	"golang.org/x/sys/windows"
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
	mw, mh := loadMainSize()
	makeFrameless(hwnd, mw, mh)
	mainHwnd.Store(uint64(hwnd))

	// As funções chamadas pelo JavaScript rodam na thread da janela; o trabalho
	// pesado vai para goroutines e o resultado volta por Dispatch.
	call := func(fn string, v interface{}) {
		b, _ := json.Marshal(v)
		w.Dispatch(func() { w.Eval("window." + fn + " && window." + fn + "(" + string(b) + ")") })
	}
	push := func(level, msg string) { call("onLog", []string{level, msg}) }
	mainOverlayMu.Lock()
	mainPageCb = call
	mainOverlayMu.Unlock()

	// overlay (janela própria, criada escondida) e o guia de peixes
	var shared sync.Mutex
	var ov *overlay
	var guide *Guide
	var stats *Stats
	var live Live
	var thumbs map[string]string
	overlayCall := func(fn string, v interface{}) {
		shared.Lock()
		o := ov
		shared.Unlock()
		if o != nil {
			o.call(fn, v)
		}
	}
	both := func(fn string, v interface{}) {
		call(fn, v)
		overlayCall(fn, v)
	}
	go func() {
		g := buildGuide(env.GameDir)
		shared.Lock()
		guide = &g
		shared.Unlock()
		both("onGuide", g)
		// miniaturas de todos os peixes (decodificadas uma vez, em segundo plano)
		var icons []string
		for _, r := range g.Reserves {
			for _, f := range r.Fish {
				icons = append(icons, f.Icon)
			}
		}
		t := fishImages(env.GameDir, icons, "thumb")
		shared.Lock()
		thumbs = t
		shared.Unlock()
		both("onThumbs", t)
	}()

	// Dados ao vivo do jogo (memória, só leitura), duas vezes por segundo.
	go func() {
		var last []byte
		for {
			l := readLive(env)
			shared.Lock()
			l.Target = missionTarget(guide, stats, l)
			live = l
			shared.Unlock()
			if b, _ := json.Marshal(l); string(b) != string(last) {
				last = b
				both("onLive", l)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	// fotos grandes sob demanda: a resposta volta por onPhoto
	requestPhoto := func(icon string) {
		go func() {
			both("onPhoto", map[string]string{"icon": icon, "url": fishImage(env.GameDir, icon, "medium")})
		}()
	}
	// imagem do mapa de uma reserva, sob demanda: a resposta volta por onMap
	requestMap := func(world string) {
		go func() {
			shared.Lock()
			g := guide
			shared.Unlock()
			if g == nil {
				return
			}
			for i := range g.Reserves {
				if r := &g.Reserves[i]; r.World == world {
					both("onMap", map[string]string{"world": world, "url": mapImage(env.GameDir, r)})
				}
			}
		}()
	}
	common := func(bind func(string, interface{}) error) {
		bind("getGuide", func() *Guide {
			shared.Lock()
			defer shared.Unlock()
			return guide
		})
		bind("getStats", func() *Stats {
			shared.Lock()
			defer shared.Unlock()
			return stats
		})
		bind("getLive", func() Live {
			shared.Lock()
			defer shared.Unlock()
			return live
		})
		bind("getThumbs", func() map[string]string {
			shared.Lock()
			defer shared.Unlock()
			return thumbs
		})
		bind("requestPhoto", requestPhoto)
		bind("requestMap", requestMap)
		bind("getMyCatches", loadMyCatches)
	}
	go func() {
		time.Sleep(800 * time.Millisecond) // deixa a janela principal subir primeiro
		o := startOverlay(dataPath, func(o *overlay) {
			common(o.w.Bind)
			o.w.Bind("ovWin", func(action string) {
				switch action {
				case "drag":
					o.mu.Lock()
					o.moved = true
					o.mu.Unlock()
					dragWindow(o.hwnd)
				case "hide":
					go o.setEnabled(false)
				default:
					o.mu.Lock()
					edit := o.edit
					o.mu.Unlock()
					if e, ok := strings.CutPrefix(action, "resize:"); ok && edit {
						s := dpiScale(o.hwnd)
						resizeWindow(o.hwnd, e, 280*s/96, 300*s/96, func(r windows.Rect) {
							o.mu.Lock()
							o.moved = true
							o.mu.Unlock()
						})
					}
				}
			})
		})
		if o == nil {
			push("warn", "Não consegui criar o overlay.")
			return
		}
		o.mu.Lock()
		o.onState = func(on bool) { call("onOverlay", on) }
		o.mu.Unlock()
		shared.Lock()
		ov = o
		shared.Unlock()
		o.mu.Lock()
		o.left = loadOptions().HudSide == "esquerda"
		o.pos = loadHudPos()
		o.mu.Unlock()
		go o.follow()
		bad := listenHotkeys([]Hotkey{
			{Key: 'G', Name: "Ctrl+Shift+G", Fn: func() { w.Dispatch(func() { toggleOverGame(hwnd) }) }},
			{Key: 'X', Name: "Ctrl+Shift+X", Fn: o.toggle},
			{Key: 'M', Name: "Ctrl+Shift+M", Fn: func() { w.Dispatch(func() { toggleMapOverGame(hwnd) }) }},
		})
		for _, k := range bad {
			push("warn", "O atalho "+k+" já é usado por outro programa.")
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
		var newest int64 = -1 // capturas até aqui já estavam no save quando o app abriu
		for {
			_, _, mod := findSave()
			if !mod.Equal(last) {
				last = mod
				st := readStats(env)
				shared.Lock()
				stats = &st
				l := live
				shared.Unlock()
				if newest >= 0 {
					recordCatches(st.Catches, newest, l)
				}
				for _, c := range st.Catches {
					if c.Date > newest {
						newest = c.Date
					}
				}
				if newest < 0 {
					newest = 0
				}
				both("onStats", st)
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
	common(w.Bind)
	w.Bind("setHudSide", func(side string) {
		op := loadOptions()
		op.HudSide = side
		op.normalize()
		saveOptions(op)
		shared.Lock()
		o := ov
		shared.Unlock()
		if o != nil {
			o.setSide(op.HudSide == "esquerda")
		}
	})
	w.Bind("toggleOverlay", func() (bool, error) {
		shared.Lock()
		o := ov
		shared.Unlock()
		if o == nil {
			return false, errors.New("o overlay ainda está carregando")
		}
		o.toggle()
		return o.isEnabled(), nil
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
		case "back": // sai do modo overlay e volta ao jogo
			closeOverGame(hwnd, true)
		default:
			if edge, ok := strings.CutPrefix(action, "resize:"); ok {
				debugf("redimensionar janela principal: %s", edge)
				s := dpiScale(hwnd)
				resizeWindow(hwnd, edge, 900*s/96, 560*s/96, func(r windows.Rect) {
					if summonedMain.Load() == 0 { // tamanho normal fica salvo para a próxima vez
						saveMainSize(r, s)
					}
				})
			}
		}
	})

	if *flagTesteG || *flagTesteMapa {
		go func() {
			time.Sleep(4 * time.Second)
			w.Dispatch(func() {
				if *flagTesteMapa {
					toggleMapOverGame(hwnd)
				} else {
					toggleOverGame(hwnd)
				}
			})
		}()
	}
	w.SetHtml(uiHTML)
	w.Run()
	return nil
}
