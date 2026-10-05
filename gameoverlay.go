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
	mainPageCb     func(fn string, arg interface{}) // chama uma função da página principal
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

// Modos do overlay principal: o Tacklebox inteiro (Ctrl+Shift+G) ou só o mapa,
// grande, cobrindo quase toda a tela do jogo (Ctrl+Shift+M).
const (
	modeApp = "app"
	modeMap = "map"
)

var (
	curMode   string       // modo aberto ("" = nenhum); protegido por mainOverlayMu
	appNormal windows.Rect // tamanho/posição normais da janela antes do overlay
)

// toggleOverGame é o Ctrl+Shift+G: abre o Tacklebox sobre o jogo; se algo já
// estiver aberto (Tacklebox ou mapa), fecha e volta ao jogo.
func toggleOverGame(hwnd uintptr) { toggleMode(hwnd, modeApp) }

// toggleMapOverGame é o Ctrl+Shift+M: o mapa grande sobre o jogo. Com o
// Tacklebox aberto, troca para o mapa; com o mapa aberto, fecha.
func toggleMapOverGame(hwnd uintptr) { toggleMode(hwnd, modeMap) }

func toggleMode(hwnd uintptr, mode string) {
	mainOverlayMu.Lock()
	cur := curMode
	page := mainPageCb
	mainOverlayMu.Unlock()
	if cur != "" {
		if cur == mode || mode == modeApp {
			closeOverGame(hwnd, true)
		} else {
			openOverGame(hwnd, mode)
		}
		return
	}
	if game, _ := findProcess(gameProcName()); game == 0 {
		// sem jogo aberto, é uma janela comum: traz para a frente ou minimiza
		if fg, _, _ := procGetForeground.Call(); fg == hwnd && mode == modeApp {
			procShowWindow.Call(hwnd, swMinimize)
			return
		}
		procShowWindow.Call(hwnd, swRestore)
		procSetForeground.Call(hwnd)
		if mode == modeMap && page != nil {
			page("goTab", "mapa")
		}
		return
	}
	openOverGame(hwnd, mode)
}

func openOverGame(hwnd uintptr, mode string) {
	fg, _, _ := procGetForeground.Call()
	var pid uint32
	procWindowPid.Call(fg, uintptr(unsafe.Pointer(&pid)))
	if game, _ := findProcess(gameProcName()); game != 0 && pid == game {
		lastGameWindow.Store(uint64(fg))
	}
	hiddenForGame.Store(false)

	wp := windowPlacement{length: uint32(unsafe.Sizeof(windowPlacement{}))}
	procGetPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))

	mainOverlayMu.Lock()
	cur := curMode
	if cur == "" {
		appNormal = wp.normal
	} else if cur == modeApp {
		// troca do Tacklebox para o mapa: guarda onde o Tacklebox estava
		var r windows.Rect
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		mainOverlayPos.set, mainOverlayPos.x, mainOverlayPos.y = true, r.Left, r.Top
	}
	x, y, set := mainOverlayPos.x, mainOverlayPos.y, mainOverlayPos.set
	w, h := appNormal.Right-appNormal.Left, appNormal.Bottom-appNormal.Top
	page := mainPageCb
	mainOverlayMu.Unlock()

	// área do jogo (ou do monitor) e área útil do monitor
	area := windows.Rect{Right: 1920, Bottom: 1080}
	gw := gameWindow()
	if gw != 0 {
		procGetWindowRect.Call(gw, uintptr(unsafe.Pointer(&area)))
	}
	mon, _, _ := procMonitorFrom.Call(gw, 1)
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	work := area
	if ok, _, _ := procMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); ok != 0 {
		work = mi.work
	}
	if mode == modeMap {
		// quase a tela toda do jogo, com uma margem
		mx, my := (area.Right-area.Left)*4/100, (area.Bottom-area.Top)*5/100
		x, y = area.Left+mx, area.Top+my
		w, h = area.Right-area.Left-2*mx, area.Bottom-area.Top-2*my
	} else if !set {
		x = area.Left + (area.Right-area.Left-w)/2
		y = area.Top + (area.Bottom-area.Top-h)/2
	}
	x = max(work.Left, min(x, work.Right-w))
	y = max(work.Top, min(y, work.Bottom-h))

	if cur == "" {
		procShowWindow.Call(hwnd, swHide)
		procSetWindowLong.Call(hwnd, gwlExStyle, exStyle(hwnd)|wsExToolWindow|wsExLayered)
		procSetLayered.Call(hwnd, 0, 246, lwaAlpha)
		wp.showCmd = 1 // SW_SHOWNORMAL, já na posição certa
		wp.normal = windows.Rect{Left: x, Top: y, Right: x + w, Bottom: y + h}
		procSetPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
		procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow|swpFrameChanged)
	} else {
		procSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpShowWindow)
	}
	procSetForeground.Call(hwnd)
	mainOverlayMu.Lock()
	curMode = mode
	mainOverlayMu.Unlock()
	summonedMain.Store(uint64(hwnd))
	if page != nil {
		page("onOverlayMode", mode)
	}
}

// closeOverGame esconde o overlay e devolve a janela ao estilo e tamanho normais (escondida).
func closeOverGame(hwnd uintptr, refocusGame bool) {
	if summonedMain.Swap(0) == 0 {
		return
	}
	mainOverlayMu.Lock()
	if curMode == modeApp {
		var r windows.Rect
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		mainOverlayPos.set, mainOverlayPos.x, mainOverlayPos.y = true, r.Left, r.Top
	}
	curMode = ""
	normal := appNormal
	page := mainPageCb
	mainOverlayMu.Unlock()

	procShowWindow.Call(hwnd, swHide)
	procSetWindowLong.Call(hwnd, gwlExStyle, exStyle(hwnd)&^(wsExToolWindow|wsExLayered|wsExTopmost))
	wp := windowPlacement{length: uint32(unsafe.Sizeof(windowPlacement{}))}
	procGetPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	if normal.Right > normal.Left {
		wp.showCmd = swHide
		wp.normal = normal
		procSetPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	}
	procSetWindowPos.Call(hwnd, hwndNoTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpFrameChanged)
	hiddenForGame.Store(true)
	if g := lastGameWindow.Load(); refocusGame && g != 0 {
		procSetForeground.Call(uintptr(g))
	}
	if page != nil {
		page("onOverlayMode", "")
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

// ---------- janela do jogo ----------

var (
	procEnumWindows = user32.NewProc("EnumWindows")
	procIsWindow    = user32.NewProc("IsWindow")
	enumMu          sync.Mutex
	enumPid         uint32
	enumBest        uintptr
	enumArea        int64
	enumCallback    = windows.NewCallback(func(h, _ uintptr) uintptr {
		var pid uint32
		procWindowPid.Call(h, uintptr(unsafe.Pointer(&pid)))
		if pid == enumPid {
			if v, _, _ := procIsVisible.Call(h); v != 0 {
				var r windows.Rect
				procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
				if a := int64(r.Right-r.Left) * int64(r.Bottom-r.Top); a > enumArea {
					enumBest, enumArea = h, a
				}
			}
		}
		return 1
	})
)

// gameWindow devolve a janela do jogo: a última vista em primeiro plano ou, se não
// houver, a maior janela visível do processo do jogo.
func gameWindow() uintptr {
	if g := uintptr(lastGameWindow.Load()); g != 0 {
		if ok, _, _ := procIsWindow.Call(g); ok != 0 {
			return g
		}
	}
	pid, ok := findProcess(gameProcName())
	if !ok {
		return 0
	}
	enumMu.Lock()
	defer enumMu.Unlock()
	enumPid, enumBest, enumArea = pid, 0, 0
	procEnumWindows.Call(enumCallback, 0)
	if enumBest != 0 {
		lastGameWindow.Store(uint64(enumBest))
	}
	return enumBest
}
