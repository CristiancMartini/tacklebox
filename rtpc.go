package main

// Leitor de RTPC v3 ("runtime property container"), o formato dos arquivos .blo
// de cada reserva: uma árvore de contêineres com propriedades tipadas, cujos
// nomes são o lookup3 do nome do campo.

import (
	"encoding/binary"
	"errors"
	"math"
)

type rtpcNode struct {
	props    map[uint32]rtpcProp
	children []*rtpcNode
}

type rtpcProp struct {
	typ byte
	raw uint32 // valor (u32/f32) ou deslocamento no arquivo
}

type rtpcFile struct {
	b    []byte
	root *rtpcNode
}

func parseRTPC(b []byte) (*rtpcFile, error) {
	if len(b) < 20 || string(b[:4]) != "RTPC" || binary.LittleEndian.Uint32(b[4:]) != 3 {
		return nil, errors.New("não é RTPC v3")
	}
	f := &rtpcFile{b: b}
	var read func(o uint32, depth int) (*rtpcNode, error)
	read = func(o uint32, depth int) (*rtpcNode, error) {
		if depth > 32 || uint64(o)+12 > uint64(len(b)) {
			return nil, errors.New("contêiner inválido")
		}
		off := binary.LittleEndian.Uint32(b[o+4:])
		np := uint32(binary.LittleEndian.Uint16(b[o+8:]))
		nc := uint32(binary.LittleEndian.Uint16(b[o+10:]))
		if uint64(off)+uint64(np)*9 > uint64(len(b)) {
			return nil, errors.New("propriedades inválidas")
		}
		n := &rtpcNode{props: make(map[uint32]rtpcProp, np)}
		for i := uint32(0); i < np; i++ {
			p := off + i*9
			n.props[binary.LittleEndian.Uint32(b[p:])] = rtpcProp{typ: b[p+8], raw: binary.LittleEndian.Uint32(b[p+4:])}
		}
		c := (off + np*9 + 3) &^ 3
		for i := uint32(0); i < nc; i++ {
			child, err := read(c+i*12, depth+1)
			if err != nil {
				return nil, err
			}
			n.children = append(n.children, child)
		}
		return n, nil
	}
	root, err := read(8, 0)
	if err != nil {
		return nil, err
	}
	f.root = root
	return f, nil
}

// walk visita todos os contêineres.
func (n *rtpcNode) walk(fn func(*rtpcNode)) {
	fn(n)
	for _, c := range n.children {
		c.walk(fn)
	}
}

func (f *rtpcFile) prop(n *rtpcNode, name string, typ byte) (rtpcProp, bool) {
	p, ok := n.props[l3(name)]
	return p, ok && p.typ == typ
}

func (f *rtpcFile) u32(n *rtpcNode, name string) (uint32, bool) {
	p, ok := f.prop(n, name, 1)
	return p.raw, ok
}

func (f *rtpcFile) f32(n *rtpcNode, name string) (float64, bool) {
	p, ok := f.prop(n, name, 2)
	return float64(math.Float32frombits(p.raw)), ok
}

func (f *rtpcFile) str(n *rtpcNode, name string) string {
	p, ok := f.prop(n, name, 3)
	if !ok || int(p.raw) >= len(f.b) {
		return ""
	}
	s := f.b[p.raw:]
	for i, c := range s {
		if c == 0 {
			return string(s[:i])
		}
	}
	return ""
}

func (f *rtpcFile) floats(off uint32, count int) []float64 {
	if uint64(off)+uint64(count)*4 > uint64(len(f.b)) {
		return nil
	}
	out := make([]float64, count)
	for i := range out {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(f.b[off+uint32(i)*4:])))
	}
	return out
}

// vec3 devolve um vec3 (tipo 5).
func (f *rtpcFile) vec3(n *rtpcNode, name string) []float64 {
	p, ok := f.prop(n, name, 5)
	if !ok {
		return nil
	}
	return f.floats(p.raw, 3)
}

// mat4 devolve a matriz 4x4 (linhas; a translação fica em [12:15]).
func (f *rtpcFile) mat4(n *rtpcNode, name string) []float64 {
	p, ok := f.prop(n, name, 8)
	if !ok {
		return nil
	}
	return f.floats(p.raw, 16)
}

// floatArray devolve um array de f32 (tipo 10).
func (f *rtpcFile) floatArray(n *rtpcNode, name string) []float64 {
	p, ok := f.prop(n, name, 10)
	if !ok || uint64(p.raw)+4 > uint64(len(f.b)) {
		return nil
	}
	return f.floats(p.raw+4, int(binary.LittleEndian.Uint32(f.b[p.raw:])))
}
