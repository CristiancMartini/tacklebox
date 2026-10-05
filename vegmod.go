// Mod opcional que esconde a vegetação (grama, mato, pedras soltas e, se pedido, árvores).
//
// A engine (Apex) carrega archives_win64/initial/game0, game1, ... enquanto existirem
// e, quando o mesmo arquivo aparece em mais de um pacote, o último vence. O mod é um
// pacote extra game<N> (próximo número livre) com os arquivos de vegetação alterados;
// os pacotes originais não são tocados e apagar o game<N> desfaz tudo.
// (Testado: a pasta "dropzone" é ignorada no jogo publicado, e montar pastas com
// --vfs-fs/--vfs-archive faz o jogo fechar ao entrar no mapa.)
//
// O arquivo alterado é worlds/<mapa>/climate/vegetation_layers.vegetationinfo; veja
// patchVegetationInfo para o que muda. As instâncias continuam existindo nos dados do
// mapa (só não são desenhadas), então o multiplayer continua sincronizado.
package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var vegWorlds = []string{"achelous", "alpheus", "belisama", "ceto", "doris"}

const (
	vegMarker = "otimizador-angler-mod.txt"
)

// ---------- hashes ----------

// MurmurHash3 x64_128 (h1), usado no índice dos .tab
func murmur3h1(data []byte) uint64 {
	const c1, c2 = 0x87c37b91114253d5, 0x4cf5ad432745937f
	var h1, h2 uint64
	n := len(data) / 16
	for i := 0; i < n; i++ {
		k1 := binary.LittleEndian.Uint64(data[i*16:])
		k2 := binary.LittleEndian.Uint64(data[i*16+8:])
		k1 *= c1
		k1 = bits.RotateLeft64(k1, 31)
		k1 *= c2
		h1 ^= k1
		h1 = bits.RotateLeft64(h1, 27)
		h1 += h2
		h1 = h1*5 + 0x52dce729
		k2 *= c2
		k2 = bits.RotateLeft64(k2, 33)
		k2 *= c1
		h2 ^= k2
		h2 = bits.RotateLeft64(h2, 31)
		h2 += h1
		h2 = h2*5 + 0x38495ab5
	}
	tail := data[n*16:]
	var k1, k2 uint64
	for i := len(tail) - 1; i >= 8; i-- {
		k2 ^= uint64(tail[i]) << (uint(i-8) * 8)
	}
	if len(tail) > 8 {
		k2 *= c2
		k2 = bits.RotateLeft64(k2, 33)
		k2 *= c1
		h2 ^= k2
	}
	for i := min(len(tail), 8) - 1; i >= 0; i-- {
		k1 ^= uint64(tail[i]) << (uint(i) * 8)
	}
	if len(tail) > 0 {
		k1 *= c1
		k1 = bits.RotateLeft64(k1, 31)
		k1 *= c2
		h1 ^= k1
	}
	h1 ^= uint64(len(data))
	h2 ^= uint64(len(data))
	h1 += h2
	h2 += h1
	h1 = fmix64(h1)
	h2 = fmix64(h2)
	h1 += h2
	return h1
}

func fmix64(k uint64) uint64 {
	k ^= k >> 33
	k *= 0xff51afd7ed558ccd
	k ^= k >> 33
	k *= 0xc4ceb9fe1a85ec53
	k ^= k >> 33
	return k
}

// Jenkins lookup3 hashlittle (seed 0), usado nos nomes dentro dos arquivos ADF
func lookup3(key []byte) uint32 {
	rot := func(x uint32, k uint) uint32 { return bits.RotateLeft32(x, int(k)) }
	a := 0xdeadbeef + uint32(len(key))
	b, c := a, a
	k := key
	for len(k) > 12 {
		a += binary.LittleEndian.Uint32(k[0:])
		b += binary.LittleEndian.Uint32(k[4:])
		c += binary.LittleEndian.Uint32(k[8:])
		a -= c
		a ^= rot(c, 4)
		c += b
		b -= a
		b ^= rot(a, 6)
		a += c
		c -= b
		c ^= rot(b, 8)
		b += a
		a -= c
		a ^= rot(c, 16)
		c += b
		b -= a
		b ^= rot(a, 19)
		a += c
		c -= b
		c ^= rot(b, 4)
		b += a
		k = k[12:]
	}
	if len(k) == 0 {
		return c
	}
	var t [12]byte
	copy(t[:], k)
	a += binary.LittleEndian.Uint32(t[0:])
	b += binary.LittleEndian.Uint32(t[4:])
	c += binary.LittleEndian.Uint32(t[8:])
	c ^= b
	c -= rot(b, 14)
	a ^= c
	a -= rot(c, 11)
	b ^= a
	b -= rot(a, 25)
	c ^= b
	c -= rot(b, 16)
	a ^= c
	a -= rot(c, 4)
	b ^= a
	b -= rot(a, 14)
	c ^= b
	c -= rot(b, 24)
	return c
}

func l3(s string) uint32 { return lookup3([]byte(s)) }

// ---------- leitura dos arquivos .tab/.arc ----------

type arcEntry struct {
	arcPath string
	off     uint32
	csize   uint32
	usize   uint32
	block   uint16
	ctype   uint8
	blocks  [][2]uint32
}

// findEntries procura os hashes pedidos em todos os game*.tab (initial e supplemental).
// isOwnTab reconhece o pacote gerado por este programa: um único bloco (o sentinela)
// e só entradas de vegetação. Os pacotes do jogo têm centenas de blocos.
func isOwnTab(b []byte, want map[uint64]string) bool {
	if len(b) < 0x28 || string(b[:4]) != "TAB\x00" || binary.LittleEndian.Uint32(b[0x10:]) != 1 {
		return false
	}
	n := int(binary.LittleEndian.Uint32(b[0x0c:]))
	if n == 0 || len(b) != 0x28+n*24 {
		return false
	}
	for k := 0; k < n; k++ {
		if _, ok := want[binary.LittleEndian.Uint64(b[0x28+k*24:])]; !ok {
			return false
		}
	}
	return true
}

func vegWant() map[uint64]string {
	want := map[uint64]string{}
	for _, w := range vegWorlds {
		p := vegFilePath(w)
		want[murmur3h1([]byte(p))] = p
	}
	return want
}

// ownArchives lista os game*.tab gerados por este programa.
func ownArchives(initDir string) []string {
	want := vegWant()
	var own []string
	tabs, _ := filepath.Glob(filepath.Join(initDir, "game*.tab"))
	for _, tp := range tabs {
		if b, err := os.ReadFile(tp); err == nil && isOwnTab(b, want) {
			own = append(own, tp)
		}
	}
	return own
}

// findEntries procura os hashes pedidos nos game*.tab do jogo, ignorando os deste programa.
func findEntries(gameDir string, want map[uint64]string) (map[string]arcEntry, error) {
	found := map[string]arcEntry{}
	for _, sub := range []string{"initial", "supplemental"} {
		dir := filepath.Join(gameDir, "archives_win64", sub)
		tabsFiles, _ := filepath.Glob(filepath.Join(dir, "game*.tab"))
		// o jogo carrega game0, game1, game2... e o último vence; a ordem do Glob é alfabética
		sort.SliceStable(tabsFiles, func(i, j int) bool { return tabIndex(tabsFiles[i]) < tabIndex(tabsFiles[j]) })
		for _, tp := range tabsFiles {
			b, err := os.ReadFile(tp)
			if err != nil || len(b) < 0x20 || string(b[:4]) != "TAB\x00" || isOwnTab(b, want) {
				continue
			}
			n := int(binary.LittleEndian.Uint32(b[0x0c:]))
			start := len(b) - n*24
			if start < 0x20 {
				continue
			}
			var blocks [][2]uint32
			for o := 0x20; o+8 <= start; o += 8 {
				blocks = append(blocks, [2]uint32{binary.LittleEndian.Uint32(b[o:]), binary.LittleEndian.Uint32(b[o+4:])})
			}
			for k := 0; k < n; k++ {
				o := start + k*24
				h := binary.LittleEndian.Uint64(b[o:])
				name, ok := want[h]
				if !ok {
					continue
				}
				found[name] = arcEntry{
					arcPath: strings.TrimSuffix(tp, ".tab") + ".arc",
					off:     binary.LittleEndian.Uint32(b[o+8:]),
					csize:   binary.LittleEndian.Uint32(b[o+12:]),
					usize:   binary.LittleEndian.Uint32(b[o+16:]),
					block:   binary.LittleEndian.Uint16(b[o+20:]),
					ctype:   b[o+22],
					blocks:  blocks,
				}
			}
		}
	}
	return found, nil
}

// tabIndex devolve o N de ".../gameN.tab" (ou -1).
func tabIndex(path string) int {
	n := 0
	digits := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "game"), ".tab")
	if digits == "" {
		return -1
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func readArcEntry(e arcEntry) ([]byte, error) {
	f, err := os.Open(e.arcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw := make([]byte, e.csize)
	if _, err := f.ReadAt(raw, int64(e.off)); err != nil {
		return nil, err
	}
	switch e.ctype {
	case 0:
		return raw, nil
	case 1:
	default:
		return nil, fmt.Errorf("compressão %d não suportada", e.ctype)
	}
	if e.usize <= 0x80000 {
		if e.csize == e.usize {
			return raw, nil
		}
		r, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		out, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		if uint32(len(out)) != e.usize {
			return nil, errors.New("tamanho descompactado inesperado")
		}
		return out, nil
	}
	var out []byte
	pos := 0
	for bi := int(e.block); uint32(len(out)) < e.usize; bi++ {
		if bi >= len(e.blocks) {
			return nil, errors.New("blocos insuficientes")
		}
		cs, us := int(e.blocks[bi][0]), int(e.blocks[bi][1])
		if pos+cs > len(raw) {
			return nil, errors.New("bloco além do fim")
		}
		if cs == us { // bloco que não comprimiu fica guardado cru
			out = append(out, raw[pos:pos+cs]...)
			pos += cs
			continue
		}
		r, err := zlib.NewReader(bytes.NewReader(raw[pos : pos+cs]))
		if err != nil {
			return nil, err
		}
		part, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
		pos += cs
	}
	if uint32(len(out)) != e.usize {
		return nil, errors.New("tamanho descompactado inesperado")
	}
	return out, nil
}

// ---------- ADF (só o necessário) ----------

type adfType struct {
	size uint32
	name string
}

type adfInfo struct {
	types     map[uint32]adfType
	instances []struct{ typeHash, off uint32 }
}

func parseADF(b []byte) (*adfInfo, error) {
	if len(b) < 0x40 || string(b[0:4]) != " FDA" || binary.LittleEndian.Uint32(b[4:]) != 4 {
		return nil, errors.New("formato ADF inesperado")
	}
	u32 := func(o uint32) uint32 { return binary.LittleEndian.Uint32(b[o:]) }
	u64 := func(o uint32) uint64 { return binary.LittleEndian.Uint64(b[o:]) }
	inRange := func(o, n uint32) bool { return uint64(o)+uint64(n) <= uint64(len(b)) }
	info := &adfInfo{types: map[uint32]adfType{}}

	nameCount, nameOff := u32(0x20), u32(0x24)
	if !inRange(nameOff, nameCount) {
		return nil, errors.New("tabela de nomes inválida")
	}
	var names []string
	p := nameOff + nameCount
	for i := uint32(0); i < nameCount; i++ {
		l := uint32(b[nameOff+i])
		if !inRange(p, l+1) {
			return nil, errors.New("nome inválido")
		}
		names = append(names, string(b[p:p+l]))
		p += l + 1
	}
	typeCount, typeOff := u32(0x10), u32(0x14)
	p = typeOff
	for i := uint32(0); i < typeCount; i++ {
		if !inRange(p, 40) {
			return nil, errors.New("tipo inválido")
		}
		meta, size, hash, ni, cnt := u32(p), u32(p+4), u32(p+12), u64(p+16), u32(p+36)
		name := ""
		if ni < uint64(len(names)) {
			name = names[ni]
		}
		info.types[hash] = adfType{size: size, name: name}
		p += 40
		switch meta {
		case 8:
			p += cnt * 12
		default:
			p += cnt * 32
		}
	}
	instCount, instOff := u32(0x08), u32(0x0c)
	for i := uint32(0); i < instCount; i++ {
		q := instOff + i*24
		if !inRange(q, 24) {
			return nil, errors.New("instância inválida")
		}
		info.instances = append(info.instances, struct{ typeHash, off uint32 }{u32(q + 4), u32(q + 8)})
	}
	return info, nil
}

// ---------- modos de vegetação ----------

const (
	vegNormal = "normal" // jogo original
	vegGrass  = "grama"  // some grama, mato, flores e pedras soltas; árvores ficam
	vegAll    = "tudo"   // some tudo, inclusive árvores e os billboards distantes
)

const noPhysics = 0xdeadbeef

// Camadas "baixas": grama, flores, mato, pedras soltas e plantas d'água.
var lowLayers = []string{"crops", "detail", "detail_close", "detail_extreme", "detail_far", "near", "mid", "water_veg_far"}

// patchVegetationInfo esconde camadas inteiras do sistema de vegetação.
//
// Cada instância pertence a uma camada gravada nos dados do mapa, e o alcance da
// camada (Range) é aplicado na hora de desenhar; então alcance de 1 m = some.
// Os objetos das camadas escondidas perdem física e efeitos (PfxFile/efeitos =
// 0xdeadbeef, o mesmo "nenhum" que o jogo usa), para não sobrar colisão invisível
// nem partícula de folha no ar. Nada muda de lugar nem de tamanho no arquivo.
func patchVegetationInfo(b []byte, mode string) ([]byte, int, error) {
	out := append([]byte(nil), b...)
	info, err := parseADF(out)
	if err != nil {
		return nil, 0, err
	}
	for _, t := range []struct {
		hash uint32
		size uint32
		name string
	}{
		{0xa137fc88, 56, "VegetationModelLayer"},
		{0x1707d93a, 52, "VegetationBillboardLayer"},
		{0xd10f0420, 24, "VegetationPhysicsLayer"},
		{0xeb5fdc96, 72, "VegetationForestLayer"},
		{0xaa677f46, 392, "VegetationObject"},
	} {
		if got, ok := info.types[t.hash]; ok && (got.size != t.size || got.name != t.name) {
			return nil, 0, fmt.Errorf("formato de %s mudou (atualização do jogo?)", t.name)
		}
	}
	var base uint32
	found := false
	for _, in := range info.instances {
		if t, ok := info.types[in.typeHash]; ok && t.name == "VegetationWorld" {
			base, found = in.off, true
		}
	}
	if !found {
		return nil, 0, errors.New("VegetationWorld não encontrado")
	}
	u32 := func(o uint32) uint32 { return binary.LittleEndian.Uint32(out[o:]) }
	setF := func(o uint32, v float32) { binary.LittleEndian.PutUint32(out[o:], math.Float32bits(v)) }
	none := func(o uint32) { binary.LittleEndian.PutUint32(out[o:], noPhysics) }
	// VegetationLayers fica no início do VegetationWorld: Forest +0x00, Billboard +0x10,
	// Model +0x20, Physics +0x30 (cada um é {offset u32, ?, count u64}).
	each := func(field uint32, size uint64, fn func(e uint32)) error {
		arr, n := base+u32(base+field), uint64(u32(base+field+8))
		if uint64(arr)+n*size > uint64(len(out)) {
			return errors.New("dados fora do arquivo")
		}
		for i := uint64(0); i < n; i++ {
			fn(arr + uint32(i*size))
		}
		return nil
	}

	low := map[uint32]bool{}
	for _, n := range lowLayers {
		low[l3(n)] = true
	}
	hidden := map[uint32]bool{} // camadas de modelo escondidas
	hide := func(layer uint32) bool { return mode == vegAll || (mode == vegGrass && low[layer]) }

	err = errors.Join(
		each(0x20, 56, func(e uint32) { // VegetationModelLayer
			if h := u32(e); hide(h) {
				hidden[h] = true
				setF(e+0x18, 1) // Range
			}
		}),
		each(0x10, 52, func(e uint32) { // VegetationBillboardLayer
			if mode == vegAll || hidden[u32(e+0x30)] { // SourceLayerHash
				setF(e+0x1c, 1)    // Range
				setF(e+0x20, 0.25) // FadeInStart
				setF(e+0x24, 0.25) // FadeInRange
				setF(e+0x28, 0.5)  // FadeOutStart
				setF(e+0x2c, 0.25) // FadeOutRange
			}
		}),
		each(0x30, 24, func(e uint32) { // VegetationPhysicsLayer
			if mode == vegAll || hidden[u32(e+0x14)] { // SourceLayerHash
				setF(e+0x10, 1) // Range
			}
		}),
		each(0x00, 72, func(e uint32) { // VegetationForestLayer
			if mode == vegAll {
				setF(e+0x18, 1) // Range
			}
		}),
	)
	if err != nil {
		return nil, 0, err
	}

	objArr, objCnt := base+u32(base+0x50), uint64(u32(base+0x58))
	if uint64(objArr)+objCnt*392 > uint64(len(out)) {
		return nil, 0, errors.New("dados fora do arquivo")
	}
	count := 0
	for i := uint32(0); i < uint32(objCnt); i++ {
		o := objArr + i*392
		if mode != vegAll && !hidden[u32(o+0x08)] { // LayerHash
			continue
		}
		none(o + 0x98)  // Physics.PfxFile
		none(o + 0xa0)  // Physics.StumpFile
		none(o + 0xa4)  // Physics.PfxStumpFile
		none(o + 0xe0)  // Effects.BreakEffect
		none(o + 0xe4)  // Effects.CollideEffect
		none(o + 0xe8)  // Effects.PassThroughEffect
		none(o + 0xec)  // Effects.FastThroughEffect
		none(o + 0x138) // Effects.FallingLeavesEffect
		count++
	}
	if _, err := parseADF(out); err != nil {
		return nil, 0, err
	}
	return out, count, nil
}

// ---------- instalar / desinstalar ----------

func vegFilePath(world string) string {
	return "worlds/" + world + "/climate/vegetation_layers.vegetationinfo"
}

type vegFile struct {
	path string
	data []byte
}

// buildArchive monta um par .tab/.arc no formato do jogo (TAB v3), sem compressão.
func buildArchive(files []vegFile) (tab, arc []byte) {
	sort.Slice(files, func(i, j int) bool {
		return murmur3h1([]byte(files[i].path)) < murmur3h1([]byte(files[j].path))
	})
	tab = make([]byte, 0x20, 0x28+len(files)*24)
	copy(tab, "TAB\x00")
	binary.LittleEndian.PutUint16(tab[4:], 3)
	binary.LittleEndian.PutUint16(tab[6:], 1)
	binary.LittleEndian.PutUint32(tab[8:], 0x1000)               // alinhamento
	binary.LittleEndian.PutUint32(tab[0xc:], uint32(len(files))) // entradas
	binary.LittleEndian.PutUint32(tab[0x10:], 1)                 // blocos (só o sentinela)
	binary.LittleEndian.PutUint32(tab[0x14:], 1)
	binary.LittleEndian.PutUint32(tab[0x18:], 0x80000)
	binary.LittleEndian.PutUint32(tab[0x1c:], 0x80000)
	tab = append(tab, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	for _, f := range files {
		off := len(arc)
		arc = append(arc, f.data...)
		for len(arc)%0x1000 != 0 {
			arc = append(arc, 0)
		}
		var e [24]byte
		binary.LittleEndian.PutUint64(e[0:], murmur3h1([]byte(f.path)))
		binary.LittleEndian.PutUint32(e[8:], uint32(off))
		binary.LittleEndian.PutUint32(e[12:], uint32(len(f.data)))
		binary.LittleEndian.PutUint32(e[16:], uint32(len(f.data)))
		tab = append(tab, e[:]...) // bloco 0, sem compressão
	}
	return tab, arc
}

// installVegMod lê os arquivos de vegetação do jogo em gameDir e grava um pacote
// extra game<N> (próximo número livre) em outDir/archives_win64/initial. O jogo
// carrega todos os game<N> em sequência e o último a definir um arquivo vence.
func installVegMod(gameDir, outDir, mode string, log logFn) error {
	want := vegWant()
	entries, err := findEntries(gameDir, want)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("não achei os arquivos de vegetação do jogo (pasta archives_win64)")
	}
	var files []vegFile
	for _, w := range vegWorlds {
		p := vegFilePath(w)
		e, ok := entries[p]
		if !ok {
			log("warn", mapLabel(w)+": não encontrado (mapa não instalado?)")
			continue
		}
		orig, err := readArcEntry(e)
		if err != nil {
			log("warn", mapLabel(w)+": erro lendo: "+err.Error())
			continue
		}
		patched, n, err := patchVegetationInfo(orig, mode)
		if err != nil {
			log("warn", mapLabel(w)+": "+err.Error()+" (mapa fica normal)")
			continue
		}
		files = append(files, vegFile{p, patched})
		log("info", fmt.Sprintf("%s: %d tipos de objeto escondidos", mapLabel(w), n))
	}
	if len(files) == 0 {
		return errors.New("nenhum mapa foi modificado")
	}

	// limpa versões anteriores do mod
	uninstallVegMod(outDir)

	gameInit := filepath.Join(gameDir, "archives_win64", "initial")
	outInit := filepath.Join(outDir, "archives_win64", "initial")
	if err := os.MkdirAll(outInit, 0o755); err != nil {
		return err
	}
	n := 0
	for exists(filepath.Join(gameInit, fmt.Sprintf("game%d.tab", n))) || exists(filepath.Join(gameInit, fmt.Sprintf("game%d.arc", n))) {
		n++
	}
	tab, arc := buildArchive(files)
	base := filepath.Join(outInit, fmt.Sprintf("game%d", n))
	if err := writeAtomic(base+".arc", arc); err != nil {
		return err
	}
	if err := writeAtomic(base+".tab", tab); err != nil {
		os.Remove(base + ".arc")
		return err
	}
	log("info", "Pacote criado: archives_win64\\initial\\"+filepath.Base(base)+".tab/.arc")
	return nil
}

// uninstallVegMod apaga só os pacotes que têm a assinatura deste programa
// (e as pastas das versões antigas do mod).
func uninstallVegMod(outDir string) error {
	removeModFolder(filepath.Join(outDir, "dropzone"))
	removeModFolder(filepath.Join(outDir, "otimizador_mod"))
	own := ownArchives(filepath.Join(outDir, "archives_win64", "initial"))
	if len(own) == 0 {
		return errors.New("o mod não está instalado")
	}
	for _, tp := range own {
		os.Remove(tp)
		os.Remove(strings.TrimSuffix(tp, ".tab") + ".arc")
	}
	return nil
}

func vegModInstalled(outDir string) bool {
	return len(ownArchives(filepath.Join(outDir, "archives_win64", "initial"))) > 0
}

// removeModFolder apaga pastas criadas por versões antigas (só os arquivos listados no marcador).
func removeModFolder(dz string) bool {
	b, err := os.ReadFile(filepath.Join(dz, vegMarker))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "worlds/") || !strings.HasSuffix(l, ".vegetationinfo") || strings.Contains(l, "..") {
			continue
		}
		f := filepath.Join(dz, filepath.FromSlash(l))
		os.Remove(f)
		for d := filepath.Dir(f); len(d) > len(dz); d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
	}
	os.Remove(filepath.Join(dz, vegMarker))
	os.Remove(filepath.Join(dz, "digest_cache.dc"))
	os.Remove(dz)
	return true
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func mapLabel(w string) string {
	if n, ok := mapNames[w]; ok {
		return n[0]
	}
	return w
}
