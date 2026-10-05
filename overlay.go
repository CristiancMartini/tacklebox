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
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

//go:embed ui/overlay.html
var overlayHTML string

const (
	overlayW = 400 // largura em pixels "de CSS"
	overlayH = 760

	gwlExStyle      = ^uintptr(19) // GWL_EXSTYLE (-20)
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExNoActivate  = 0x08000000
	swpNoActivate   = 0x0010
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swHide          = 0
	hwndTopmost     = ^uintptr(0) // HWND_TOPMOST (-1)
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
		procSetWindowLong.Call(o.hwnd, gwlExStyle, wsExTopmost|wsExToolWindow|wsExNoActivate)
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
	gamePidMu   sync.Mutex
	gamePid     uint32
	gamePidSeen time.Time
)

// currentGamePid consulta a lista de processos no máximo a cada 3 s.
func currentGamePid() uint32 {
	gamePidMu.Lock()
	defer gamePidMu.Unlock()
	if time.Since(gamePidSeen) > 3*time.Second {
		gamePid, _ = findProcess(gameExeName)
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
	want := o.isEnabled() && (gameFront || fg == o.hwnd || game == 0 || *flagOverlayTeste)
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

// show mostra sem ativar, na lateral direita da janela do jogo (ou do monitor),
// a não ser que o jogador tenha arrastado o overlay para outro lugar.
func (o *overlay) show(anchor uintptr) {
	flags := uintptr(swpNoActivate | swpShowWindow)
	var x, y, w, h int32
	o.mu.Lock()
	moved := o.moved
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
		y = r.Top + (r.Bottom-r.Top-h)/2
	}
	procSetWindowPos.Call(o.hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), flags)
	o.call("onShow", true)
}

// listenHotkey registra Ctrl+Shift+G para o sistema todo (funciona com o jogo em
// foco) e chama fn a cada aperto. Devolve false se outro programa já usa o atalho.
func listenHotkey(fn func()) bool {
	ok := make(chan bool)
	go func() {
		runtime.LockOSThread()
		r, _, _ := procRegisterHot.Call(0, 1, modControl|modShift|modNoRepeat, 'G')
		ok <- r != 0
		if r == 0 {
			return
		}
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
			if msg.message == wmHotkey {
				fn()
			}
		}
	}()
	return <-ok
}
