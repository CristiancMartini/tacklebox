// Mod opcional que esconde a vegetação (grama, mato, pedras soltas e, se pedido, árvores).
//
// A engine (Apex) carrega archives_win64/initial/game0, game1, ... enquanto existirem
// e, quando o mesmo arquivo aparece em mais de um pacote, o último vence. O mod é um
// pacote extra game<N> (próximo número livre) com os arquivos de vegetação alterados;
// os pacotes originais não são tocados e apagar o game<N> desfaz tudo.
// (Testado: a pasta "dropzone" é ignorada no jogo publicado, e montar pastas com
// --vfs-fs/--vfs-archive faz o jogo fechar ao entrar no mapa.)
//
// O pacote leva, por mapa, worlds/<mapa>/climate/vegetation_layers.vegetationinfo
// (alcance das camadas, física e as tabelas da grama gerada na hora; veja
// patchVegetationInfo) e versões vazias dos trechos de vegetação pré-calculada (veja
// emptyVegStreams), para o jogo nem carregar o que está escondido. É só visual e
// local: peixes, missões e o multiplayer não dependem da vegetação.
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
	marker := murmur3h1([]byte(vegMarkerPath))
	all := true
	for k := 0; k < n; k++ {
		h := binary.LittleEndian.Uint64(b[0x28+k*24:])
		if h == marker {
			return true
		}
		if _, ok := want[h]; !ok {
			all = false
		}
	}
	return all // versões antigas: só os vegetationinfo, sem o marcador
}

func vegWant() map[uint64]string {
	want := map[uint64]string{}
	for _, w := range vegWorlds {
		p := vegFilePath(w)
		want[murmur3h1([]byte(p))] = p
	}
	return want
}

// singleBlockTab lê só o cabeçalho do .tab e diz se ele tem um bloco (como o nosso).
func singleBlockTab(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var h [0x14]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return false
	}
	return string(h[:4]) == "TAB\x00" && binary.LittleEndian.Uint32(h[0x10:]) == 1
}

// ownArchives lista os game*.tab gerados por este programa.
func ownArchives(initDir string) []string {
	want := vegWant()
	var own []string
	tabs, _ := filepath.Glob(filepath.Join(initDir, "game*.tab"))
	for _, tp := range tabs {
		// a interface pergunta isso a cada 2 s: os pacotes do jogo têm centenas de
		// blocos, então o cabeçalho basta para descartá-los sem ler o arquivo todo
		if !singleBlockTab(tp) {
			continue
		}
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
	hiddenObj := map[int16]bool{} // posição no VegetationObjects
	for i := uint32(0); i < uint32(objCnt); i++ {
		o := objArr + i*392
		if mode != vegAll && !hidden[u32(o+0x08)] { // LayerHash
			continue
		}
		hiddenObj[int16(i)] = true
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

	// A grama miúda nasce na hora, sorteada em tabelas de 64 posições por conjunto e
	// canal (VegetationSet.ProbabilityBuffer: índice do objeto ou -1 = nada). Com os
	// objetos escondidos trocados por -1, o jogo nem gera essas instâncias.
	// VegetationZones fica em +0x40 e DefaultVegetationZone em +0x60; cada zona é uma
	// lista de VegetationSet (40 bytes, ProbabilityBuffer em +8), cada ProbabilityBuffer
	// tem 24 bytes (Buffer em +0, AnyValidVariation no bit 0 de +0x10).
	arrOf := func(at uint32, size uint64) (uint32, uint64, error) {
		arr, n := base+u32(at), uint64(u32(at+8))
		if uint64(arr)+n*size > uint64(len(out)) {
			return 0, 0, errors.New("dados fora do arquivo")
		}
		return arr, n, nil
	}
	clearZone := func(zone uint32) error {
		sets, ns, err := arrOf(zone, 40)
		if err != nil {
			return err
		}
		for i := uint64(0); i < ns; i++ {
			pbs, np, err := arrOf(sets+uint32(i*40)+8, 24)
			if err != nil {
				return err
			}
			for k := uint64(0); k < np; k++ {
				pb := pbs + uint32(k*24)
				buf, nb, err := arrOf(pb, 2)
				if err != nil {
					return err
				}
				changed, any := false, false
				for j := uint64(0); j < nb; j++ {
					at := buf + uint32(j*2)
					v := int16(binary.LittleEndian.Uint16(out[at:]))
					if v >= 0 && hiddenObj[v] {
						binary.LittleEndian.PutUint16(out[at:], 0xffff)
						changed = true
					} else if v >= 0 {
						any = true
					}
				}
				if changed && !any {
					out[pb+0x10] &^= 1
				}
			}
		}
		return nil
	}
	zones, nz, err := arrOf(base+0x40, 16)
	if err != nil {
		return nil, 0, err
	}
	for i := uint64(0); i < nz; i++ {
		if err := clearZone(zones + uint32(i*16)); err != nil {
			return nil, 0, err
		}
	}
	if err := clearZone(base + 0x60); err != nil {
		return nil, 0, err
	}

	if _, err := parseADF(out); err != nil {
		return nil, 0, err
	}
	return out, count, nil
}

// ---------- vegetação pré-calculada (veg_streampatches) ----------
//
// Mato, arbustos, pedras e árvores não são sorteados na hora: vêm prontos em trechos
// worlds/<mapa>/terrain/veg_streampatches/width_<W>/patch_<nível>_<x>_<z>.streampatch,
// que o jogo vai carregando em volta do jogador. Cada camada de billboard lê os seus
// em width_<StreamPatchMapWidth>, no nível StreamPatchLod, e a camada de modelo de
// origem (SourceLayerHash) usa as mesmas instâncias de perto. Só esconder pelo alcance
// deixava tudo isso carregado e montado (custando FPS) e desenhado a 1 m da câmera.
// O mod troca esses trechos por trechos vazios, iguais aos que o próprio jogo já usa
// onde não há vegetação (só o cabeçalho, Size 0): nada é carregado.

type vegStream struct {
	width, lod int
	hide       bool // todas as camadas que usam este conjunto estão escondidas
}

// vegStreamSets lista os conjuntos de trechos usados pelas camadas de billboard e o
// tamanho do mundo (VegetationWorld.WorldSize, +0x90: x, altura, z).
func vegStreamSets(b []byte, mode string) ([]vegStream, float32, error) {
	info, err := parseADF(b)
	if err != nil {
		return nil, 0, err
	}
	var base uint32
	found := false
	for _, in := range info.instances {
		if t, ok := info.types[in.typeHash]; ok && t.name == "VegetationWorld" {
			base, found = in.off, true
		}
	}
	if t, ok := info.types[0x1707d93a]; !found || (ok && t.size != 52) {
		return nil, 0, errors.New("formato da vegetação inesperado")
	}
	u32 := func(o uint32) uint32 { return binary.LittleEndian.Uint32(b[o:]) }
	low := map[uint32]bool{}
	for _, n := range lowLayers {
		low[l3(n)] = true
	}
	arr, n := base+u32(base+0x10), u32(base+0x18) // VegetationBillboardLayer
	if uint64(arr)+uint64(n)*52 > uint64(len(b)) {
		return nil, 0, errors.New("dados fora do arquivo")
	}
	var sets []vegStream
	idx := map[[2]int]int{}
	for i := uint32(0); i < n; i++ {
		e := arr + i*52
		lod, width := int(int32(u32(e+0x14))), int(int32(u32(e+0x18)))
		if lod < 0 || lod > 20 || width < 1 || width > 64 {
			continue
		}
		hide := mode == vegAll || (mode == vegGrass && low[u32(e+0x30)]) // SourceLayerHash
		k := [2]int{width, lod}
		if j, ok := idx[k]; ok {
			sets[j].hide = sets[j].hide && hide
			continue
		}
		idx[k] = len(sets)
		sets = append(sets, vegStream{width, lod, hide})
	}
	size := max(math.Float32frombits(u32(base+0x90)), math.Float32frombits(u32(base+0x98)))
	if !(size >= 1024 && size <= 1<<20) {
		size = 32768
	}
	return sets, size, nil
}

func vegPatchPath(world string, width, lod, x, z int) string {
	return fmt.Sprintf("worlds/%s/terrain/veg_streampatches/width_%d/patch_%02d_%02d_%02d.streampatch", world, width, lod, x, z)
}

// patchesPerSide: um trecho do nível L cobre 2^(L+1) m (no nível 9, 32×32 trechos
// num mundo de 32768 m). Sobra uma fileira de folga; o que não existe é ignorado.
func patchesPerSide(size float32, lod int) int {
	n := int(math.Ceil(float64(size)/float64(int64(2)<<lod))) + 1
	return min(max(n, 1), 512)
}

// emptyPatchHeader confere se b é um trecho vazio (só o StreamPatchFileHeader, Size 0)
// e devolve onde está o cabeçalho.
func emptyPatchHeader(b []byte) (uint32, bool) {
	info, err := parseADF(b)
	if err != nil || len(info.instances) != 1 {
		return 0, false
	}
	in := info.instances[0]
	t, ok := info.types[in.typeHash]
	if !ok || t.name != "StreamPatchFileHeader" || t.size != 24 || uint64(in.off)+24 > uint64(len(b)) {
		return 0, false
	}
	return in.off, binary.LittleEndian.Uint32(b[in.off+4:]) == 0
}

type worldStreams struct {
	world string
	sets  []vegStream
	size  float32
}

// emptyVegStreams gera um trecho vazio para cada trecho escondido que existe no jogo.
// O modelo é um trecho vazio do próprio jogo (há vários nos níveis mais altos), com
// a posição e o nível trocados (cabeçalho +0x0c x, +0x10 z, +0x14 nível).
func emptyVegStreams(gameDir string, worlds []worldStreams, log logFn) ([]vegFile, error) {
	type pos struct{ world, lod, x, z int }
	want := map[uint64]string{}
	where := map[string]pos{}
	tmplWant := map[uint64]string{}
	for wi, ws := range worlds {
		widths := map[int]int{} // menor nível usado em cada width
		for _, s := range ws.sets {
			if l, ok := widths[s.width]; !ok || s.lod < l {
				widths[s.width] = s.lod
			}
			if !s.hide {
				continue
			}
			n := patchesPerSide(ws.size, s.lod)
			for x := 0; x < n; x++ {
				for z := 0; z < n; z++ {
					p := vegPatchPath(ws.world, s.width, s.lod, x, z)
					want[murmur3h1([]byte(p))] = p
					where[p] = pos{wi, s.lod, x, z}
				}
			}
		}
		for width, l0 := range widths {
			for l := l0 + 1; l <= 14; l++ {
				n := patchesPerSide(ws.size, l)
				for x := 0; x < n && x < 8; x++ {
					for z := 0; z < n && z < 8; z++ {
						p := vegPatchPath(ws.world, width, l, x, z)
						tmplWant[murmur3h1([]byte(p))] = p
					}
				}
			}
		}
	}
	if len(want) == 0 {
		return nil, nil
	}
	entries, err := findEntries(gameDir, want)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("trechos de vegetação não encontrados")
	}

	// modelo: o menor trecho que for vazio de verdade
	tmplEntries, _ := findEntries(gameDir, tmplWant)
	var cands []arcEntry
	for _, e := range tmplEntries {
		if e.usize <= 4096 {
			cands = append(cands, e)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].usize < cands[j].usize })
	var tmpl []byte
	var hdr uint32
	for i := 0; i < len(cands) && i < 20 && tmpl == nil; i++ {
		if b, err := readArcEntry(cands[i]); err == nil {
			if h, ok := emptyPatchHeader(b); ok {
				tmpl, hdr = b, h
			}
		}
	}
	if tmpl == nil {
		return nil, errors.New("não achei um trecho vazio do jogo para usar de modelo")
	}

	paths := make([]string, 0, len(entries))
	for p, e := range entries {
		if e.usize != uint32(len(tmpl)) { // os que já são vazios ficam como estão
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	files := make([]vegFile, 0, len(paths))
	perWorld := make([]int, len(worlds))
	for _, p := range paths {
		at := where[p]
		d := append([]byte(nil), tmpl...)
		binary.LittleEndian.PutUint32(d[hdr+0x0c:], uint32(at.x))
		binary.LittleEndian.PutUint32(d[hdr+0x10:], uint32(at.z))
		binary.LittleEndian.PutUint32(d[hdr+0x14:], uint32(at.lod))
		files = append(files, vegFile{p, d})
		perWorld[at.world]++
	}
	for wi, ws := range worlds {
		if perWorld[wi] > 0 {
			log("info", fmt.Sprintf("%s: %d trechos de vegetação deixam de ser carregados", mapLabel(ws.world), perWorld[wi]))
		}
	}
	return files, nil
}

// ---------- instalar / desinstalar ----------

// vegMarkerPath é uma entrada extra que identifica o pacote deste programa (o jogo
// nunca pede esse caminho).
const vegMarkerPath = "tacklebox/vegmod.txt"

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
	size := 0
	for _, f := range files {
		size += (len(f.data) + 0xfff) &^ 0xfff
	}
	arc = make([]byte, 0, size)
	for _, f := range files {
		off := len(arc)
		arc = append(arc, f.data...)
		arc = append(arc, make([]byte, (0x1000-len(arc)%0x1000)%0x1000)...)
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
	var streams []worldStreams
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
		if sets, size, err := vegStreamSets(orig, mode); err == nil {
			streams = append(streams, worldStreams{w, sets, size})
		} else {
			log("warn", mapLabel(w)+": "+err.Error()+" (vegetação pré-calculada continua carregando)")
		}
	}
	if len(files) == 0 {
		return errors.New("nenhum mapa foi modificado")
	}
	empties, err := emptyVegStreams(gameDir, streams, log)
	if err != nil {
		log("warn", err.Error()+" (só o alcance das camadas foi reduzido)")
	}
	files = append(files, empties...)
	files = append(files, vegFile{vegMarkerPath, []byte("Tacklebox: vegetação escondida (modo " + mode + ")\r\n")})

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
