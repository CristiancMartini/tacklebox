package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Janela sem a moldura do Windows: a barra de título é desenhada pela interface.

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	procSetWindowLong = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos  = user32.NewProc("SetWindowPos")
	procReleaseCap    = user32.NewProc("ReleaseCapture")
	procPostMessage   = user32.NewProc("PostMessageW")
	procShowWindow    = user32.NewProc("ShowWindow")
	procGetDpi        = user32.NewProc("GetDpiForWindow")
	procMonitorFrom   = user32.NewProc("MonitorFromWindow")
	procMonitorInfo   = user32.NewProc("GetMonitorInfoW")
)

const (
	gwlStyle        = ^uintptr(15) // GWL_STYLE (-16)
	wsPopup         = 0x80000000
	wsVisible       = 0x10000000
	wsClipChildren  = 0x02000000
	wsSysMenu       = 0x00080000
	wsMinimizeBox   = 0x00020000
	swpFrameChanged = 0x0020
	swpNoZOrder     = 0x0004
	swpShowWindow   = 0x0040
	wmNCLButtonDown = 0x00A1
	wmClose         = 0x0010
	htCaption       = 2
	swMinimize      = 6
)

type monitorInfo struct {
	cbSize  uint32
	monitor windows.Rect
	work    windows.Rect
	flags   uint32
}

// makeFrameless tira a moldura e centraliza a janela com o tamanho pedido em
// pixels "de CSS" (escalados pelo DPI do monitor).
func makeFrameless(hwnd uintptr, cssW, cssH int) {
	procSetWindowLong.Call(hwnd, gwlStyle, wsPopup|wsVisible|wsClipChildren|wsSysMenu|wsMinimizeBox)

	dpi, _, _ := procGetDpi.Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	w := int32(cssW) * int32(dpi) / 96
	h := int32(cssH) * int32(dpi) / 96

	x, y := int32(100), int32(100)
	mon, _, _ := procMonitorFrom.Call(hwnd, 2) // MONITOR_DEFAULTTONEAREST
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if r, _, _ := procMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); r != 0 {
		x = mi.work.Left + (mi.work.Right-mi.work.Left-w)/2
		y = mi.work.Top + (mi.work.Bottom-mi.work.Top-h)/2
	}
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		swpFrameChanged|swpNoZOrder|swpShowWindow)
}

// dragWindow começa a arrastar a janela (chamado no mousedown da barra de título).
func dragWindow(hwnd uintptr) {
	procReleaseCap.Call()
	procPostMessage.Call(hwnd, wmNCLButtonDown, htCaption, 0)
}

func minimizeWindow(hwnd uintptr) { procShowWindow.Call(hwnd, swMinimize) }

func closeWindow(hwnd uintptr) { procPostMessage.Call(hwnd, wmClose, 0, 0) }
