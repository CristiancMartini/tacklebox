package main

// Leitor genérico de ADF (Avalanche Data Format), o formato dos saves e de vários
// arquivos do jogo. O arquivo descreve os próprios tipos, então os valores são
// lidos pelo nome do campo; mudanças de versão do save não quebram a leitura.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

type adfMember struct {
	name   string
	typ    uint32
	offset uint32
	bit    uint32
}

type adfTypeDef struct {
	meta     uint32
	size     uint32
	name     string
	elemHash uint32
	elemLen  uint32
	members  []adfMember
}

type adfDoc struct {
	b         []byte
	types     map[uint32]*adfTypeDef
	instances []adfInstanceDef
}

type adfInstanceDef struct {
	name     string
	typeHash uint32
	off      uint32
	size     uint32
}

var adfPrimitives = map[uint32]struct {
	kind string
	size uint32
}{
	0x0ca2821d: {"u8", 1}, 0x580d0a62: {"i8", 1},
	0x86d152bd: {"u16", 2}, 0xd13fcf93: {"i16", 2},
	0x075e4e4f: {"u32", 4}, 0x192fe633: {"i32", 4},
	0xa139e01f: {"u64", 8}, 0xaf41354f: {"i64", 8},
	0x7515a207: {"f32", 4}, 0xc609f663: {"f64", 8},
	0x8955583e: {"str", 8},
}

// loadADF aceita o ADF puro ou com um prefixo antes de " FDA" (caso dos saves).
func loadADF(raw []byte) (*adfDoc, error) {
	start := -1
	for i := 0; i+4 <= len(raw) && i < 64; i++ {
		if string(raw[i:i+4]) == " FDA" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, errors.New("não é um arquivo ADF")
	}
	b := raw[start:]
	if len(b) < 0x40 || binary.LittleEndian.Uint32(b[4:]) != 4 {
		return nil, errors.New("versão de ADF não suportada")
	}
	in := func(o, n uint32) bool { return uint64(o)+uint64(n) <= uint64(len(b)) }
	u32 := func(o uint32) uint32 { return binary.LittleEndian.Uint32(b[o:]) }
	u64 := func(o uint32) uint64 { return binary.LittleEndian.Uint64(b[o:]) }
	d := &adfDoc{b: b, types: map[uint32]*adfTypeDef{}}

	nameCount, nameOff := u32(0x20), u32(0x24)
	if !in(nameOff, nameCount) {
		return nil, errors.New("tabela de nomes inválida")
	}
	var names []string
	p := nameOff + nameCount
	for i := uint32(0); i < nameCount; i++ {
		l := uint32(b[nameOff+i])
		if !in(p, l+1) {
			return nil, errors.New("nome inválido")
		}
		names = append(names, string(b[p:p+l]))
		p += l + 1
	}
	nm := func(i uint64) string {
		if i < uint64(len(names)) {
			return names[i]
		}
		return ""
	}

	typeCount, typeOff := u32(0x10), u32(0x14)
	p = typeOff
	for i := uint32(0); i < typeCount; i++ {
		if !in(p, 40) {
			return nil, errors.New("tipo inválido")
		}
		t := &adfTypeDef{meta: u32(p), size: u32(p + 4), name: nm(u64(p + 16)), elemHash: u32(p + 28), elemLen: u32(p + 32)}
		hash, cnt := u32(p+12), u32(p+36)
		p += 40
		switch t.meta {
		case 1: // estrutura
			if !in(p, cnt*32) {
				return nil, errors.New("campos inválidos")
			}
			for k := uint32(0); k < cnt; k++ {
				off := u32(p + 16)
				t.members = append(t.members, adfMember{name: nm(u64(p)), typ: u32(p + 8), offset: off & 0xffffff, bit: off >> 24})
				p += 32
			}
		case 8: // enum
			p += cnt * 12
		default:
			p += cnt * 32
		}
		d.types[hash] = t
	}

	instCount, instOff := u32(0x08), u32(0x0c)
	for i := uint32(0); i < instCount; i++ {
		q := instOff + i*24
		if !in(q, 24) {
			return nil, errors.New("instância inválida")
		}
		d.instances = append(d.instances, adfInstanceDef{name: nm(u64(q + 16)), typeHash: u32(q + 4), off: u32(q + 8), size: u32(q + 12)})
	}
	return d, nil
}

func (d *adfDoc) sizeOf(h uint32) uint32 {
	if p, ok := adfPrimitives[h]; ok {
		return p.size
	}
	if t, ok := d.types[h]; ok {
		return t.size
	}
	return 0
}

// decode transforma uma instância em mapas/listas/números (como um JSON).
func (d *adfDoc) decode(inst adfInstanceDef) (interface{}, error) {
	return d.value(inst.off, inst.off, inst.typeHash, 0)
}

func (d *adfDoc) value(base, off, th uint32, depth int) (v interface{}, err error) {
	if depth > 64 {
		return nil, errors.New("estrutura profunda demais")
	}
	b := d.b
	need := func(n uint32) error {
		if uint64(off)+uint64(n) > uint64(len(b)) {
			return fmt.Errorf("leitura fora do arquivo em %x", off)
		}
		return nil
	}
	if p, ok := adfPrimitives[th]; ok {
		if err := need(p.size); err != nil {
			return nil, err
		}
		switch p.kind {
		case "u8":
			return float64(b[off]), nil
		case "i8":
			return float64(int8(b[off])), nil
		case "u16":
			return float64(binary.LittleEndian.Uint16(b[off:])), nil
		case "i16":
			return float64(int16(binary.LittleEndian.Uint16(b[off:]))), nil
		case "u32":
			return float64(binary.LittleEndian.Uint32(b[off:])), nil
		case "i32":
			return float64(int32(binary.LittleEndian.Uint32(b[off:]))), nil
		case "u64", "i64":
			return float64(binary.LittleEndian.Uint64(b[off:])), nil
		case "f32":
			f := float64(math.Float32frombits(binary.LittleEndian.Uint32(b[off:])))
			if math.IsNaN(f) || math.IsInf(f, 0) {
				f = 0
			}
			return f, nil
		case "f64":
			return math.Float64frombits(binary.LittleEndian.Uint64(b[off:])), nil
		case "str":
			s := base + binary.LittleEndian.Uint32(b[off:])
			e := s
			for e < uint32(len(b)) && b[e] != 0 {
				e++
			}
			if s > uint32(len(b)) {
				return "", nil
			}
			return string(b[s:e]), nil
		}
	}
	t, ok := d.types[th]
	if !ok {
		return nil, nil
	}
	switch t.meta {
	case 1: // estrutura
		m := map[string]interface{}{}
		for _, mem := range t.members {
			if bt, ok := d.types[mem.typ]; ok && bt.meta == 7 { // bitfield
				if err := need(mem.offset + 4); err != nil {
					return nil, err
				}
				raw := binary.LittleEndian.Uint32(b[off+mem.offset:])
				width := bt.elemLen
				if width == 0 || width > 32 {
					width = 1
				}
				m[mem.name] = float64((raw >> mem.bit) & (1<<width - 1))
				continue
			}
			v, err := d.value(base, off+mem.offset, mem.typ, depth+1)
			if err != nil {
				return nil, err
			}
			m[mem.name] = v
		}
		return m, nil
	case 3: // array: {u32 offset, u32, u64 count}
		if err := need(16); err != nil {
			return nil, err
		}
		ao := binary.LittleEndian.Uint32(b[off:])
		n := binary.LittleEndian.Uint64(b[off+8:])
		es := d.sizeOf(t.elemHash)
		if n > 1<<20 || uint64(base)+uint64(ao)+n*uint64(es) > uint64(len(b)) {
			return nil, fmt.Errorf("array inválido em %x", off)
		}
		out := make([]interface{}, 0, n)
		for i := uint64(0); i < n; i++ {
			v, err := d.value(base, base+ao+uint32(i)*es, t.elemHash, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case 4: // array de tamanho fixo
		es := d.sizeOf(t.elemHash)
		out := make([]interface{}, 0, t.elemLen)
		for i := uint32(0); i < t.elemLen; i++ {
			v, err := d.value(base, off+i*es, t.elemHash, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case 8, 9: // enum, hash de string
		if err := need(4); err != nil {
			return nil, err
		}
		return float64(binary.LittleEndian.Uint32(b[off:])), nil
	}
	return nil, nil
}

// Acesso cômodo aos dados decodificados.

func get(v interface{}, path ...string) interface{} {
	for _, k := range path {
		m, ok := v.(map[string]interface{})
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func num(v interface{}, path ...string) float64 {
	f, _ := get(v, path...).(float64)
	return f
}

func list(v interface{}, path ...string) []interface{} {
	l, _ := get(v, path...).([]interface{})
	return l
}
