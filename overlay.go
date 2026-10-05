package main

// Overlay: uma janela pequena, sempre por cima, que aparece sobre o jogo com
// Ctrl+Shift+G. Não injeta nada no jogo (zero risco de ban); por isso precisa do
// jogo em modo "Borderless" (tela cheia sem bordas) para ficar visível por cima.
// Ela roda numa thread própria e nunca recebe o foco, então o jogo continua
// recebendo o teclado e o mouse.

import (
	_ "embed"
	"encoding/json"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

//go:embed ui/overlay.html
var overlayHTML string

const (
	overlayW = 360 // largura em pixels "de CSS"
	overlayH = 620

	gwlExStyle      = ^uintptr(19) // GWL_EXSTYLE (-20)
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExNoActivate  = 0x08000000
	wsExLayered     = 0x00080000
	wsExTransparent = 0x00000020
	lwaAlpha        = 0x2
	swpNoActivate   = 0x0010
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swHide          = 0
	hwndTopmost     = ^uintptr(0) // HWND_TOPMOST (-1)
	hwndNoTopmost   = ^uintptr(1) // HWND_NOTOPMOST (-2)
	swRestore       = 9
	whCBT           = 5
	hcbtCreateWnd   = 3
	hcbtActivate    = 5
	modControl      = 0x0002
	modShift        = 0x0004
	modNoRepeat     = 0x4000
	wmHotkey        = 0x0312
	cornerAttribute = 33 // DWMWA_WINDOW_CORNER_PREFERENCE
)

var (
	procSetHook       = user32.NewProc("SetWindowsHookExW")
	procUnhook        = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHook  = user32.NewProc("CallNextHookEx")
	procGetForeground = user32.NewProc("GetForegroundWindow")
	procRegisterHot   = user32.NewProc("RegisterHotKey")
	procGetMessage    = user32.NewProc("GetMessageW")
	procIsVisible     = user32.NewProc("IsWindowVisible")
	procSetLayered    = user32.NewProc("SetLayeredWindowAttributes")
	procSetForeground = user32.NewProc("SetForegroundWindow")
	procIsIconic      = user32.NewProc("IsIconic")
	lastGameWindow    atomic.Uint64
	procWindowPid     = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowRect = user32.NewProc("GetWindowRect")
	procGetDpiMonitor = windows.NewLazySystemDLL("shcore.dll").NewProc("GetDpiForMonitor")
	procDwmSetAttr    = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	procCoInit        = windows.NewLazySystemDLL("ole32.dll").NewProc("CoInitializeEx")
)

// createStruct é o CREATESTRUCTW (64 bits) que o hook recebe ao criar uma janela.
type createStruct struct {
	createParams uintptr
	instance     uintptr
	menu         uintptr
	parent       uintptr
	cy, cx, y, x int32
	style        int32
	name, class  uintptr
	exStyle      uint32
}

// cbtCreateWnd é o CBT_CREATEWNDW; no HCBT_ACTIVATE o ponteiro é de outra estrutura, que não é lida.
type cbtCreateWnd struct {
	cs          *createStruct
	insertAfter uintptr
}

type overlay struct {
	w    webview2.WebView
	hwnd uintptr

	mu      sync.Mutex
	enabled bool // ligado pelo atalho ou pelo botão
	moved   bool // o jogador arrastou: respeita a posição escolhida
	left    bool // painel do lado esquerdo do jogo
	edit    bool // arrastável (Tacklebox aberto sobre o jogo); fora disso o mouse atravessa
	pos     hudPosition
	onState func(enabled bool)
}

// quietCreation cria janelas desta thread fora da tela, sem botão na barra de
// tarefas e sem ativação, para o overlay não piscar nem roubar o foco enquanto o
// WebView2 inicia.
func quietCreation() (undo func()) {
	cb := windows.NewCallback(func(code int32, wp uintptr, lp *cbtCreateWnd) uintptr {
		switch code {
		case hcbtCreateWnd:
			if cs := lp.cs; cs != nil && cs.parent == 0 {
				cs.x, cs.y = -32000, -32000
				procSetWindowLong.Call(wp, gwlExStyle, wsExToolWindow|wsExNoActivate)
			}
		case hcbtActivate:
			return 1
		}
		r, _, _ := procCallNextHook.Call(0, uintptr(code), wp, uintptr(unsafe.Pointer(lp)))
		return r
	})
	h, _, _ := procSetHook.Call(whCBT, cb, 0, uintptr(windows.GetCurrentThreadId()))
	return func() {
		if h != 0 {
			procUnhook.Call(h)
		}
	}
}

// startOverlay cria o overlay (escondido) numa thread própria.
func startOverlay(dataPath string, bind func(*overlay)) *overlay {
	ready := make(chan *overlay)
	go func() {
		runtime.LockOSThread()
		procCoInit.Call(0, 2) // COINIT_APARTMENTTHREADED, exigido pelo WebView2
		undo := quietCreation()
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath: dataPath,
			WindowOptions: webview2.WindowOptions{
				Title: "Tacklebox Overlay", Width: overlayW, Height: overlayH, IconId: 1,
			},
		})
		undo()
		if w == nil {
			ready <- nil
			return
		}
		o := &overlay{w: w, hwnd: uintptr(w.Window())}
		procShowWindow.Call(o.hwnd, swHide)
		procSetWindowLong.Call(o.hwnd, gwlStyle, wsPopup|wsClipChildren)
		// "atravessável": o mouse passa direto para o jogo (layered + transparent)
		procSetWindowLong.Call(o.hwnd, gwlExStyle, wsExTopmost|wsExToolWindow|wsExNoActivate|wsExLayered|wsExTransparent)
		procSetLayered.Call(o.hwnd, 0, 248, lwaAlpha)
		// sem moldura a área útil muda; o WM_SIZE resultante ajusta o WebView
		procSetWindowPos.Call(o.hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged)
		round := uint32(2) // DWMWCP_ROUND (Windows 11)
		procDwmSetAttr.Call(o.hwnd, cornerAttribute, uintptr(unsafe.Pointer(&round)), 4)
		bind(o)
		w.SetHtml(overlayHTML)
		ready <- o
		w.Run()
	}()
	return <-ready
}

// call executa window.fn(v) na página do overlay.
func (o *overlay) call(fn string, v interface{}) {
	b, _ := json.Marshal(v)
	o.w.Dispatch(func() { o.w.Eval("window." + fn + " && window." + fn + "(" + string(b) + ")") })
}

func (o *overlay) visible() bool {
	r, _, _ := procIsVisible.Call(o.hwnd)
	return r != 0
}

// setSide muda o lado do painel (vale na próxima vez que ele aparecer).
func (o *overlay) setSide(left bool) {
	o.mu.Lock()
	o.left = left
	o.pos = hudPosition{}
	o.mu.Unlock()
	saveHudPos(hudPosition{})
	o.w.Dispatch(func() {
		if o.visible() {
			procShowWindow.Call(o.hwnd, swHide) // o follow mostra de novo no lugar novo
		}
	})
}

// toggle liga ou desliga o overlay (atalho e botões).
func (o *overlay) toggle() { o.setEnabled(!o.isEnabled()) }

func (o *overlay) isEnabled() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.enabled
}

func (o *overlay) setEnabled(on bool) {
	o.mu.Lock()
	changed := o.enabled != on
	o.enabled = on
	cb := o.onState
	o.mu.Unlock()
	if changed && cb != nil {
		cb(on)
	}
	o.update()
}

// follow deixa o overlay visível só enquanto o jogo (ou o próprio overlay) está em
// primeiro plano: some no alt-tab e volta quando o jogo volta. Com o jogo fechado,
// ligar o overlay mostra uma prévia.
func (o *overlay) follow() {
	for {
		o.update()
		time.Sleep(250 * time.Millisecond)
	}
}

var (
	lastFgLogged   uintptr
	gameFrontTicks int // atualizações seguidas com o jogo na frente e o overlay aberto
)

var (
	gamePidMu   sync.Mutex
	gamePid     uint32
	gamePidSeen time.Time
)

// currentGamePid consulta a lista de processos no máximo a cada 3 s.
func currentGamePid() uint32 {
	gamePidMu.Lock()
	defer gamePidMu.Unlock()
	if time.Since(gamePidSeen) > 3*time.Second {
		gamePid, _ = findProcess(gameProcName())
		gamePidSeen = time.Now()
	}
	return gamePid
}

func (o *overlay) update() {
	fg, _, _ := procGetForeground.Call()
	var pid uint32
	procWindowPid.Call(fg, uintptr(unsafe.Pointer(&pid)))
	game := currentGamePid()
	gameFront := game != 0 && pid == game
	if fg != lastFgLogged {
		lastFgLogged = fg
		debugf("frente agora: janela %x pid %d (jogo=%v principal=%v painel=%v)", fg, pid, gameFront, fg == uintptr(mainHwnd.Load()), fg == o.hwnd)
	}
	// o Tacklebox aberto por Ctrl+Shift+G some quando o jogador volta ao jogo
	if h := summonedMain.Load(); h != 0 {
		if fg == uintptr(h) {
			mainFrontSeen.Store(true)
		}
		// fecha quando o jogador volta ao jogo: o jogo na frente por ~0,5 s seguidos,
		// sem botão apertado (nunca no meio de um arraste ou redimensionamento)
		if gameFront && mainFrontSeen.Load() && !resizing.Load() && !mouseDown() {
			gameFrontTicks++
		} else {
			gameFrontTicks = 0
		}
		if gameFrontTicks >= 2 {
			gameFrontTicks = 0
			debugf("jogo voltou para a frente: fechando o overlay")
			closeOverGame(uintptr(h), false)
		}
	}
	// jogo fechado: a janela principal escondida volta ao normal
	if game == 0 && hiddenForGame.Swap(false) {
		if h := mainHwnd.Load(); h != 0 {
			procShowWindow.Call(uintptr(h), swRestore)
		}
	}
	menu := summonedMain.Load() != 0
	o.setEdit(menu || game == 0)
	// com o Tacklebox aberto sobre o jogo, o painel fica acima dele (para ver e arrastar)
	if menu && o.visible() {
		procSetWindowPos.Call(o.hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	}
	want := o.isEnabled() && (gameFront || fg == o.hwnd || menu || game == 0 || *flagOverlayTeste)
	if want == o.visible() {
		return
	}
	o.w.Dispatch(func() {
		if !want {
			procShowWindow.Call(o.hwnd, swHide)
			return
		}
		o.show(fg)
	})
}

// setEdit liga o modo de arrastar: a janela passa a receber o mouse. Ao sair dele,
// a posição escolhida é guardada.
func (o *overlay) setEdit(on bool) {
	o.mu.Lock()
	if o.edit == on {
		o.mu.Unlock()
		return
	}
	o.edit = on
	o.mu.Unlock()
	o.w.Dispatch(func() {
		ex := exStyle(o.hwnd)
		if on {
			ex &^= wsExTransparent | wsExNoActivate
		} else {
			ex |= wsExTransparent | wsExNoActivate
		}
		procSetWindowLong.Call(o.hwnd, gwlExStyle, ex)
		if !on && o.visible() {
			var r windows.Rect
			procGetWindowRect.Call(o.hwnd, uintptr(unsafe.Pointer(&r)))
			o.mu.Lock()
			if o.moved {
				o.pos = hudPosition{Set: true, X: r.Left, Y: r.Top, W: r.Right - r.Left, H: r.Bottom - r.Top}
				saveHudPos(o.pos)
			}
			o.mu.Unlock()
		}
		o.call("onEdit", on)
	})
}

// show mostra sem ativar, na lateral direita da janela do jogo (ou do monitor),
// a não ser que o jogador tenha arrastado o overlay para outro lugar.
func (o *overlay) show(anchor uintptr) {
	flags := uintptr(swpNoActivate | swpShowWindow)
	var x, y, w, h int32
	o.mu.Lock()
	moved, pos := o.moved, o.pos
	o.mu.Unlock()
	if moved {
		flags |= swpNoMove | swpNoSize
	} else {
		if anchor == 0 {
			anchor = o.hwnd
		}
		mon, _, _ := procMonitorFrom.Call(anchor, 1) // MONITOR_DEFAULTTOPRIMARY
		mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
		procMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
		r := mi.monitor
		var wr windows.Rect
		if anchor != o.hwnd {
			if ok, _, _ := procGetWindowRect.Call(anchor, uintptr(unsafe.Pointer(&wr))); ok != 0 && wr.Right-wr.Left > 400 && wr.Bottom-wr.Top > 300 {
				r = wr
			}
		}
		dpiX, dpiY := uint32(96), uint32(96)
		if procGetDpiMonitor.Find() == nil {
			procGetDpiMonitor.Call(mon, 0, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY)))
		}
		if dpiX == 0 {
			dpiX = 96
		}
		px := func(v int32) int32 { return v * int32(dpiX) / 96 }
		w = px(overlayW)
		h = px(overlayH)
		if max := r.Bottom - r.Top - px(48); h > max {
			h = max
		}
		x = r.Right - w - px(24)
		o.mu.Lock()
		if o.left {
			x = r.Left + px(24)
		}
		o.mu.Unlock()
		y = r.Top + (r.Bottom-r.Top-h)/2
		if pos.Set && pos.W > 0 && pos.H > 0 { // tamanho escolhido pelo jogador
			w, h = min(pos.W, r.Right-r.Left), min(pos.H, r.Bottom-r.Top)
		}
		if pos.Set { // lugar escolhido pelo jogador, sempre dentro da área do jogo/monitor
			x = max(r.Left, min(pos.X, r.Right-w))
			y = max(r.Top, min(pos.Y, r.Bottom-h))
		}
	}
	procSetWindowPos.Call(o.hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), flags)
	o.call("onShow", true)
}

// Hotkey é um atalho global (funciona com o jogo em foco).
type Hotkey struct {
	Key  rune
	Name string
	Fn   func()
}

// listenHotkeys registra Ctrl+Shift+<tecla> para cada atalho e devolve os nomes
// dos que outro programa já usa.
func listenHotkeys(keys []Hotkey) []string {
	failed := make(chan []string)
	go func() {
		runtime.LockOSThread()
		var bad []string
		for i, k := range keys {
			if r, _, _ := procRegisterHot.Call(0, uintptr(i+1), modControl|modShift|modNoRepeat, uintptr(k.Key)); r == 0 {
				bad = append(bad, k.Name)
			}
		}
		failed <- bad
		var msg struct {
			hwnd    uintptr
			message uint32
			wParam  uintptr
			lParam  uintptr
			time    uint32
			pt      [2]int32
			private uint32
		}
		for {
			if r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0); int32(r) <= 0 {
				return
			}
			if msg.message == wmHotkey && msg.wParam >= 1 && int(msg.wParam) <= len(keys) {
				keys[msg.wParam-1].Fn()
			}
		}
	}()
	return <-failed
}
