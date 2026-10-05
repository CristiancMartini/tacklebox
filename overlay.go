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
	w      webview2.WebView
	hwnd   uintptr
	placed bool
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

func (o *overlay) toggle() {
	o.w.Dispatch(func() {
		if o.visible() {
			procShowWindow.Call(o.hwnd, swHide)
			return
		}
		o.show()
	})
}

func (o *overlay) hide() { o.w.Dispatch(func() { procShowWindow.Call(o.hwnd, swHide) }) }

// show mostra sem ativar. Na primeira vez posiciona na lateral direita do
// monitor do jogo; depois respeita onde o jogador arrastou.
func (o *overlay) show() {
	flags := uintptr(swpNoActivate | swpShowWindow)
	var x, y, w, h int32
	if o.placed {
		flags |= swpNoMove | swpNoSize
	} else {
		fg, _, _ := procGetForeground.Call()
		if fg == 0 {
			fg = o.hwnd
		}
		mon, _, _ := procMonitorFrom.Call(fg, 1) // MONITOR_DEFAULTTOPRIMARY
		mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
		procMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
		dpiX, dpiY := uint32(96), uint32(96)
		if procGetDpiMonitor.Find() == nil {
			procGetDpiMonitor.Call(mon, 0, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY)))
		}
		if dpiX == 0 {
			dpiX = 96
		}
		px := func(v int32) int32 { return v * int32(dpiX) / 96 }
		r := mi.monitor
		w = px(overlayW)
		h = px(overlayH)
		if max := r.Bottom - r.Top - px(48); h > max {
			h = max
		}
		x = r.Right - w - px(24)
		y = r.Top + (r.Bottom-r.Top-h)/2
		o.placed = true
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
