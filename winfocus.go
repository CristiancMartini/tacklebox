package main

// Foco e tamanho das janelas do Tacklebox.
//
// Foco: o Windows só deixa um programa vir para a frente em certas condições; num
// atalho global às vezes ele recusa. forceForeground liga por um instante a fila
// de entrada da nossa thread à da janela que está na frente (o jogo), o que libera
// a troca, e desliga logo em seguida.
//
// Tamanho: as janelas não têm moldura do Windows; a página detecta o clique nas
// bordas e cantos e o redimensionamento acompanha o mouse até o botão ser solto.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procAttachInput   = user32.NewProc("AttachThreadInput")
	procBringToTop    = user32.NewProc("BringWindowToTop")
	procGetCursorPos  = user32.NewProc("GetCursorPos")
	procGetAsyncKey   = user32.NewProc("GetAsyncKeyState")
	procGetCurrentTID = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentThreadId")

	mainFrontSeen atomic.Bool // o overlay principal já esteve na frente desde que abriu
	resizing      atomic.Bool // um redimensionamento está em andamento
)

// forceForeground traz hwnd para a frente mesmo quando outra janela tem o foco.
func forceForeground(hwnd uintptr) {
	fg, _, _ := procGetForeground.Call()
	if fg == hwnd {
		return
	}
	fgThread, _, _ := procWindowPid.Call(fg, 0)
	me, _, _ := procGetCurrentTID.Call()
	if fgThread != 0 && fgThread != me {
		procAttachInput.Call(me, fgThread, 1)
		defer procAttachInput.Call(me, fgThread, 0)
	}
	procBringToTop.Call(hwnd)
	procSetForeground.Call(hwnd)
}

// resizeWindow redimensiona pela borda ou canto (edge contém "l", "r", "t" e/ou "b")
// enquanto o botão esquerdo estiver apertado. done recebe o retângulo final.
func resizeWindow(hwnd uintptr, edge string, minW, minH int32, done func(windows.Rect)) {
	var start windows.Rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&start)))
	var p0 [2]int32
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p0)))
	has := func(c byte) bool {
		for i := 0; i < len(edge); i++ {
			if edge[i] == c {
				return true
			}
		}
		return false
	}
	resizing.Store(true)
	go func() {
		defer resizing.Store(false)
		r := start
		for {
			if st, _, _ := procGetAsyncKey.Call(0x01); st&0x8000 == 0 { // VK_LBUTTON solto
				break
			}
			if v, _, _ := procIsVisible.Call(hwnd); v == 0 { // a janela sumiu: não continua nem salva
				return
			}
			var p [2]int32
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
			dx, dy := p[0]-p0[0], p[1]-p0[1]
			r = start
			if has('l') {
				r.Left = min(start.Left+dx, start.Right-minW)
			}
			if has('r') {
				r.Right = max(start.Right+dx, start.Left+minW)
			}
			if has('t') {
				r.Top = min(start.Top+dy, start.Bottom-minH)
			}
			if has('b') {
				r.Bottom = max(start.Bottom+dy, start.Top+minH)
			}
			procSetWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpNoZOrder|swpNoActivate)
			time.Sleep(15 * time.Millisecond)
		}
		if done != nil {
			done(r)
		}
	}()
}

// dpiScale devolve pixels reais por pixel "de CSS" da janela.
func dpiScale(hwnd uintptr) int32 {
	dpi, _, _ := procGetDpi.Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	return int32(dpi)
}

// Tamanho normal da janela principal (em pixels "de CSS"), lembrado entre usos.
type mainSize struct {
	W int `json:"w"`
	H int `json:"h"`
}

func mainSizePath() string { return filepath.Join(filepath.Dir(configPath()), "janela.json") }

func loadMainSize() (int, int) {
	var m mainSize
	if b, err := os.ReadFile(mainSizePath()); err == nil {
		json.Unmarshal(b, &m)
	}
	if m.W < 900 || m.H < 560 {
		return 1240, 800
	}
	return m.W, m.H
}

func saveMainSize(r windows.Rect, dpi int32) {
	m := mainSize{W: int((r.Right - r.Left) * 96 / dpi), H: int((r.Bottom - r.Top) * 96 / dpi)}
	os.MkdirAll(filepath.Dir(mainSizePath()), 0o755)
	if b, err := json.Marshal(m); err == nil {
		os.WriteFile(mainSizePath(), b, 0o644)
	}
}

// mouseDown diz se o botão esquerdo está apertado (arrastando ou redimensionando).
func mouseDown() bool {
	st, _, _ := procGetAsyncKey.Call(0x01)
	return st&0x8000 != 0
}

// debugf anota em %TEMP%\tacklebox-debug.log quando TACKLEBOX_DEBUG=1 (diagnóstico).
func debugf(format string, args ...interface{}) {
	if os.Getenv("TACKLEBOX_DEBUG") != "1" {
		return
	}
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "tacklebox-debug.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, time.Now().Format("15:04:05.000")+" "+format+"\n", args...)
}
