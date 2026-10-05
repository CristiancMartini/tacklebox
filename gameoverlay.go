package main

// Modo overlay da janela principal (Ctrl+Shift+G). Com o jogo aberto, o Tacklebox
// aparece por cima do jogo como overlay: sem botão na barra de tarefas, levemente
// translúcido, na última posição usada (ou centralizado sobre o jogo). Ao voltar
// para o jogo ele some; quando o jogo fecha, a janela volta ao normal.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetWindowLong = user32.NewProc("GetWindowLongPtrW")
	procGetPlacement  = user32.NewProc("GetWindowPlacement")
	procSetPlacement  = user32.NewProc("SetWindowPlacement")

	mainHwnd      atomic.Uint64 // janela principal
	summonedMain  atomic.Uint64 // janela principal aberta sobre o jogo (0 = fechada)
	hiddenForGame atomic.Bool   // escondida ao voltar ao jogo; reaparece quando o jogo fecha

	mainOverlayMu  sync.Mutex
	mainOverlayCb  func(on bool) // avisa a página
	mainOverlayPos struct {
		set  bool
		x, y int32
	}
)

type windowPlacement struct {
	length, flags, showCmd uint32
	minPos, maxPos         [2]int32
	normal                 windows.Rect
	device                 windows.Rect
}

func exStyle(hwnd uintptr) uintptr {
	r, _, _ := procGetWindowLong.Call(hwnd, gwlExStyle)
	return r
}

// toggleOverGame é o Ctrl+Shift+G.
func toggleOverGame(hwnd uintptr) {
	if summonedMain.Load() != 0 {
		closeOverGame(hwnd, true)
		return
	}
	if currentGamePid() == 0 {
		// sem jogo aberto, é uma janela comum: traz para a frente ou minimiza
		if fg, _, _ := procGetForeground.Call(); fg == hwnd {
			procShowWindow.Call(hwnd, swMinimize)
		} else {
			procShowWindow.Call(hwnd, swRestore)
			procSetForeground.Call(hwnd)
		}
		return
	}
	openOverGame(hwnd)
}

func openOverGame(hwnd uintptr) {
	fg, _, _ := procGetForeground.Call()
	var pid uint32
	procWindowPid.Call(fg, uintptr(unsafe.Pointer(&pid)))
	if game := currentGamePid(); game != 0 && pid == game {
		lastGameWindow.Store(uint64(fg))
	}
	hiddenForGame.Store(false)

	// tamanho normal da janela (vale mesmo se ela estiver minimizada)
	wp := windowPlacement{length: uint32(unsafe.Sizeof(windowPlacement{}))}
	procGetPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	w, h := wp.normal.Right-wp.normal.Left, wp.normal.Bottom-wp.normal.Top

	mainOverlayMu.Lock()
	x, y, set := mainOverlayPos.x, mainOverlayPos.y, mainOverlayPos.set
	cb := mainOverlayCb
	mainOverlayMu.Unlock()
	if !set {
		// centralizada sobre o jogo
		area := windows.Rect{Right: 1920, Bottom: 1080}
		if g := lastGameWindow.Load(); g != 0 {
			procGetWindowRect.Call(uintptr(g), uintptr(unsafe.Pointer(&area)))
		}
		x = area.Left + (area.Right-area.Left-w)/2
		y = area.Top + (area.Bottom-area.Top-h)/2
	}
	// sempre inteira dentro do monitor
	mon, _, _ := procMonitorFrom.Call(uintptr(lastGameWindow.Load()), 1)
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ok, _, _ := procMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); ok != 0 {
		wa := mi.work
		x = max(wa.Left, min(x, wa.Right-w))
		y = max(wa.Top, min(y, wa.Bottom-h))
	}

	procShowWindow.Call(hwnd, swHide)
	procSetWindowLong.Call(hwnd, gwlExStyle, exStyle(hwnd)|wsExToolWindow|wsExLayered)
	procSetLayered.Call(hwnd, 0, 246, lwaAlpha)
	wp.showCmd = 1 // SW_SHOWNORMAL, já na posição certa
	wp.normal = windows.Rect{Left: x, Top: y, Right: x + w, Bottom: y + h}
	procSetPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow|swpFrameChanged)
	procSetForeground.Call(hwnd)
	summonedMain.Store(uint64(hwnd))
	if cb != nil {
		cb(true)
	}
}

// closeOverGame esconde o overlay e devolve a janela ao estilo normal (escondida).
func closeOverGame(hwnd uintptr, refocusGame bool) {
	if summonedMain.Swap(0) == 0 {
		return
	}
	var r windows.Rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	mainOverlayMu.Lock()
	mainOverlayPos.set, mainOverlayPos.x, mainOverlayPos.y = true, r.Left, r.Top
	cb := mainOverlayCb
	mainOverlayMu.Unlock()

	procShowWindow.Call(hwnd, swHide)
	procSetWindowLong.Call(hwnd, gwlExStyle, exStyle(hwnd)&^(wsExToolWindow|wsExLayered|wsExTopmost))
	procSetWindowPos.Call(hwnd, hwndNoTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpFrameChanged)
	hiddenForGame.Store(true)
	if g := lastGameWindow.Load(); refocusGame && g != 0 {
		procSetForeground.Call(uintptr(g))
	}
	if cb != nil {
		cb(false)
	}
}

// ---------- posição do painel fixo ----------

type hudPosition struct {
	Set bool  `json:"set"`
	X   int32 `json:"x"`
	Y   int32 `json:"y"`
}

func hudPosPath() string { return filepath.Join(filepath.Dir(configPath()), "painel.json") }

func loadHudPos() hudPosition {
	var p hudPosition
	if b, err := os.ReadFile(hudPosPath()); err == nil {
		json.Unmarshal(b, &p)
	}
	return p
}

func saveHudPos(p hudPosition) {
	os.MkdirAll(filepath.Dir(hudPosPath()), 0o755)
	if b, err := json.Marshal(p); err == nil {
		os.WriteFile(hudPosPath(), b, 0o644)
	}
}
