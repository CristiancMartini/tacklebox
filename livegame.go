package main

// Dados ao vivo do jogo, lidos da memória do processo em modo somente leitura
// (PROCESS_VM_READ): nada é escrito nem injetado. O jogo não tem anti-cheat.
//
// Os objetos são achados pelo tipo: o executável traz RTTI (nomes das classes),
// então a vtable de CPlayer, CMissionHUDModel e CReserveSelectModel é localizada
// no .exe e depois procurada na memória. Os deslocamentos dos campos foram medidos
// no build 16541670 e cada leitura é validada; se o jogo mudar, o overlay só deixa
// de mostrar os dados ao vivo.

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Live é o que o overlay mostra sobre o momento atual.
type Live struct {
	OK        bool    `json:"ok"`
	Status    string  `json:"status,omitempty"`
	World     string  `json:"world"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Z         float64 `json:"z"`
	Heading   float64 `json:"heading"` // graus: 0 = norte, 90 = leste
	Mission   string  `json:"mission"`
	Objective string  `json:"objective"`
	Target    *Target `json:"target,omitempty"` // destino da missão (calculado com o guia e o save)
}

const (
	offPlayerPos   = 0xF0  // CPlayer: vec3 posição
	offPlayerDir   = 0xFC  // CPlayer: vec3 direção do olhar (unitário)
	offMissionName = 0x18  // CMissionHUDModel: std::string nome da missão
	offMissionObj  = 0x58  // CMissionHUDModel: std::string objetivo atual
	offReserveName = 0xB90 // CReserveSelectModel: std::string código da reserva
	offPlayerChar  = 0x30  // CPlayer: ponteiro para o CCharacter controlado

	// ponteiro global para o personagem local (build 16541670). Só desempata quando
	// há mais de um CPlayer na memória (partida online); se não bater, vale o primeiro.
	rvaLocalChar = 0x3343cf0
)

var liveClasses = []string{"CPlayer", "CMissionHUDModel", "CReserveSelectModel"}

var (
	procVQEx  = windows.NewLazySystemDLL("kernel32.dll").NewProc("VirtualQueryEx")
	procRPM   = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadProcessMemory")
	liveState liveReader
)

type memInfo struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	PartitionID       uint16
	_                 uint16
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	_                 uint32
}

type liveReader struct {
	mu       sync.Mutex
	pid      uint32
	h        windows.Handle
	base     uintptr
	vt       map[string]uint32 // RVA da vtable de cada classe
	obj      map[string]uintptr
	scanning atomic.Bool
	nextScan time.Time
	backoff  time.Duration
}

// readLive devolve o estado atual; nunca bloqueia por muito tempo (a busca dos
// objetos roda em segundo plano).
func readLive(env Env) Live {
	return liveState.read(env)
}

func (r *liveReader) read(env Env) Live {
	r.mu.Lock()
	defer r.mu.Unlock()
	pid := currentGamePid()
	if pid == 0 {
		r.close()
		return Live{Status: "Jogo fechado"}
	}
	if pid != r.pid {
		r.close()
		h, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, false, pid)
		if err != nil {
			return Live{Status: "Sem acesso ao jogo"}
		}
		r.pid, r.h, r.base = pid, h, moduleBase(pid)
		r.obj = map[string]uintptr{}
		r.backoff = 5 * time.Second
		r.nextScan = time.Time{}
	}
	if r.vt == nil {
		vt, err := vtableRVAs(env.GameExe, liveClasses)
		if err != nil {
			return Live{Status: "Versão do jogo não reconhecida"}
		}
		r.vt = vt
	}
	ok := func(name string) bool {
		a := r.obj[name]
		return a != 0 && r.u64(a) == uint64(r.base)+uint64(r.vt[name])
	}
	if !ok("CPlayer") {
		if !r.scanning.Load() && time.Now().After(r.nextScan) {
			r.startScan()
		}
		return Live{Status: "Procurando o jogador…"}
	}
	p := r.obj["CPlayer"]
	f := r.floats(p+offPlayerPos, 6)
	if f == nil || !finite(f) || math.Abs(f[0]) > 50000 || math.Abs(f[2]) > 50000 {
		return Live{Status: "Dados fora do esperado"}
	}
	l := Live{OK: true, X: f[0], Y: f[1], Z: f[2]}
	if math.Hypot(f[3], f[5]) > 0.1 {
		l.Heading = math.Mod(math.Atan2(f[3], -f[5])*180/math.Pi+360, 360)
	}
	if ok("CMissionHUDModel") {
		m := r.obj["CMissionHUDModel"]
		l.Mission, l.Objective = r.str(m+offMissionName), r.str(m+offMissionObj)
	}
	if ok("CReserveSelectModel") {
		if w := r.str(r.obj["CReserveSelectModel"] + offReserveName); mapNames[w][0] != "" {
			l.World = w
		}
	}
	return l
}

func (r *liveReader) close() {
	if r.h != 0 {
		windows.CloseHandle(r.h)
	}
	r.pid, r.h, r.base, r.obj = 0, 0, 0, nil
}

// startScan procura os objetos em segundo plano. Se não achar (menu principal,
// tela de carregamento), tenta de novo com intervalos crescentes.
func (r *liveReader) startScan() {
	r.scanning.Store(true)
	h, base, vt := r.h, r.base, r.vt
	go func() {
		defer r.scanning.Store(false)
		targets := map[uint64]string{}
		for name, rva := range vt {
			targets[uint64(base)+uint64(rva)] = name
		}
		found := scanVtables(h, targets)
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.h != h {
			return
		}
		for k, v := range found {
			r.obj[k] = v[0]
		}
		if ps := found["CPlayer"]; len(ps) > 1 {
			local := r.u64(base + rvaLocalChar)
			for _, p := range ps {
				if local != 0 && r.u64(p+offPlayerChar) == local {
					r.obj["CPlayer"] = p
				}
			}
		}
		if len(found["CPlayer"]) == 0 {
			r.nextScan = time.Now().Add(r.backoff)
			if r.backoff < time.Minute {
				r.backoff *= 2
			}
		} else {
			r.backoff = 5 * time.Second
		}
	}()
}

func (r *liveReader) readMem(addr uintptr, buf []byte) bool {
	var got uintptr
	ok, _, _ := procRPM.Call(uintptr(r.h), addr, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&got)))
	return ok != 0 && got == uintptr(len(buf))
}

func (r *liveReader) u64(addr uintptr) uint64 {
	var b [8]byte
	if !r.readMem(addr, b[:]) {
		return 0
	}
	return binary.LittleEndian.Uint64(b[:])
}

func (r *liveReader) floats(addr uintptr, n int) []float64 {
	b := make([]byte, 4*n)
	if !r.readMem(addr, b) {
		return nil
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:])))
	}
	return out
}

// str lê um std::string do MSVC: 16 bytes (texto curto ou ponteiro), tamanho e capacidade.
func (r *liveReader) str(addr uintptr) string {
	var b [32]byte
	if !r.readMem(addr, b[:]) {
		return ""
	}
	size := binary.LittleEndian.Uint64(b[16:])
	capa := binary.LittleEndian.Uint64(b[24:])
	if size == 0 || size > 512 || capa < size {
		return ""
	}
	if capa < 16 {
		return cleanText(b[:size])
	}
	t := make([]byte, size)
	if !r.readMem(uintptr(binary.LittleEndian.Uint64(b[:8])), t) {
		return ""
	}
	return cleanText(t)
}

func cleanText(b []byte) string {
	s := strings.ToValidUTF8(string(b), "")
	return strings.TrimSpace(strings.Map(func(c rune) rune {
		if c < 32 {
			return -1
		}
		return c
	}, s))
}

func finite(v []float64) bool {
	for _, f := range v {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
	}
	return true
}

func moduleBase(pid uint32) uintptr {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, pid)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var me windows.ModuleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	if windows.Module32First(snap, &me) != nil {
		return 0
	}
	return me.ModBaseAddr
}

// scanVtables procura objetos cujo primeiro campo é uma das vtables. O motor
// guarda esses objetos em arenas de 16 MB, lidas em paralelo; o resto da memória
// (texturas, áudio) só é lido se não houver arenas.
func scanVtables(h windows.Handle, targets map[uint64]string) map[string][]uintptr {
	type region struct{ base, size uintptr }
	var arenas, others []region
	var addr uintptr
	for {
		var m memInfo
		if r, _, _ := procVQEx.Call(uintptr(h), addr, uintptr(unsafe.Pointer(&m)), unsafe.Sizeof(m)); r == 0 {
			break
		}
		next := m.BaseAddress + m.RegionSize
		if m.State == 0x1000 && m.Type == 0x20000 && m.Protect == 0x04 { // MEM_COMMIT, MEM_PRIVATE, PAGE_READWRITE
			if m.RegionSize == 16<<20 && m.BaseAddress == m.AllocationBase {
				arenas = append(arenas, region{m.BaseAddress, m.RegionSize})
			} else {
				others = append(others, region{m.BaseAddress, m.RegionSize})
			}
		}
		if next <= addr {
			break
		}
		addr = next
	}

	var mu sync.Mutex
	found := map[string][]uintptr{}
	var done atomic.Bool
	const maxEach = 4 // vários CPlayer só numa partida online
	complete := func() bool {
		for _, name := range targets {
			if len(found[name]) == 0 || (name == "CPlayer" && len(found[name]) < maxEach) {
				return false
			}
		}
		return true
	}
	run := func(list []region) {
		jobs := make(chan region)
		var wg sync.WaitGroup
		workers := runtime.NumCPU() / 2
		if workers < 1 {
			workers = 1
		}
		if workers > 4 {
			workers = 4
		}
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				buf := make([]byte, 16<<20)
				for rg := range jobs {
					for off := uintptr(0); off < rg.size && !done.Load(); off += uintptr(len(buf)) {
						n := rg.size - off
						if n > uintptr(len(buf)) {
							n = uintptr(len(buf))
						}
						var got uintptr
						if ok, _, _ := procRPM.Call(uintptr(h), rg.base+off, uintptr(unsafe.Pointer(&buf[0])), n, uintptr(unsafe.Pointer(&got))); ok == 0 {
							continue
						}
						words := unsafe.Slice((*uint64)(unsafe.Pointer(&buf[0])), got/8)
						for i, w := range words {
							if name, ok := targets[w]; ok {
								mu.Lock()
								if len(found[name]) < maxEach {
									found[name] = append(found[name], rg.base+off+uintptr(i*8))
									if complete() {
										done.Store(true)
									}
								}
								mu.Unlock()
							}
						}
					}
				}
			}()
		}
		for _, rg := range list {
			if done.Load() {
				break
			}
			jobs <- rg
		}
		close(jobs)
		wg.Wait()
	}
	if len(arenas) > 0 {
		run(arenas)
	} else {
		run(others)
	}
	return found
}

// ---------- RTTI ----------

type rttiCache struct {
	Size    int64             `json:"size"`
	ModTime int64             `json:"modTime"`
	RVAs    map[string]uint32 `json:"rvas"`
}

func rttiCachePath() string { return filepath.Join(filepath.Dir(configPath()), "rtti.json") }

// vtableRVAs acha a vtable principal de cada classe pelo RTTI do MSVC x64:
// nome ".?AVClasse@@" -> TypeDescriptor -> CompleteObjectLocator -> vtable.
// O resultado fica guardado por versão do executável.
func vtableRVAs(exe string, classes []string) (map[string]uint32, error) {
	st, err := os.Stat(exe)
	if err != nil {
		return nil, err
	}
	var c rttiCache
	if b, err := os.ReadFile(rttiCachePath()); err == nil && json.Unmarshal(b, &c) == nil &&
		c.Size == st.Size() && c.ModTime == st.ModTime().Unix() && len(c.RVAs) == len(classes) {
		return c.RVAs, nil
	}
	f, err := pe.Open(exe)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		return nil, errors.New("executável não é 64 bits")
	}
	type sec struct {
		va   uint32
		data []byte
	}
	var secs []sec
	for _, s := range f.Sections {
		if d, err := s.Data(); err == nil {
			secs = append(secs, sec{s.VirtualAddress, d})
		}
	}
	out := map[string]uint32{}
	for _, cls := range classes {
		name := []byte(".?AV" + cls + "@@\x00")
		var td uint32
		for _, s := range secs {
			if j := bytes.Index(s.data, name); j >= 16 {
				td = s.va + uint32(j-16)
				break
			}
		}
		if td == 0 {
			continue
		}
		var col uint32
		for _, s := range secs {
			for j := 0; j+24 <= len(s.data) && col == 0; j += 4 {
				d := s.data[j:]
				if binary.LittleEndian.Uint32(d) == 1 && binary.LittleEndian.Uint32(d[4:]) == 0 &&
					binary.LittleEndian.Uint32(d[12:]) == td && binary.LittleEndian.Uint32(d[20:]) == s.va+uint32(j) {
					col = s.va + uint32(j)
				}
			}
		}
		if col == 0 {
			continue
		}
		want := oh.ImageBase + uint64(col)
		for _, s := range secs {
			for j := 0; j+16 <= len(s.data); j += 8 {
				if binary.LittleEndian.Uint64(s.data[j:]) == want {
					out[cls] = s.va + uint32(j+8)
					break
				}
			}
			if out[cls] != 0 {
				break
			}
		}
	}
	if len(out) != len(classes) {
		return nil, errors.New("classes não encontradas no executável")
	}
	c = rttiCache{Size: st.Size(), ModTime: st.ModTime().Unix(), RVAs: out}
	if b, err := json.Marshal(c); err == nil {
		os.MkdirAll(filepath.Dir(rttiCachePath()), 0o755)
		os.WriteFile(rttiCachePath(), b, 0o644)
	}
	return out, nil
}
