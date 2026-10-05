package main

// Arquivos do jogo lidos pelo Tacklebox (somente leitura): os textos em inglês
// e as tabelas de dados (níveis, peixes, iscas e itens).

import (
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"strconv"
	"strings"
	"sync"
)

const textPath = "text/master_eng.stringlookup"

func tablePath(name string) string { return "settings/game_data_tables/" + name + ".csvc" }

type gameData struct {
	rawText []byte
	text    map[string]string // chave de texto -> texto em inglês
	keyOf   map[uint32]string // lookup3 de uma chave (sem _name/_title/_desc) -> chave
	raw     map[string][]byte // tabela (nome sem pasta e extensão) -> conteúdo
}

var (
	dataOnce sync.Once
	data     gameData
)

func loadGameData(gameDir string) *gameData {
	dataOnce.Do(func() {
		data = gameData{text: map[string]string{}, keyOf: map[uint32]string{}, raw: map[string][]byte{}}
		if gameDir == "" {
			return
		}
		tables := []string{"progression_curves", "fish_codex_bait_compatibility", "item_defs", "lure_groups"}
		for _, w := range vegWorlds {
			tables = append(tables, "fish_codex_"+w, "fish_codex_legendary_"+w)
		}
		want := map[uint64]string{murmur3h1([]byte(textPath)): textPath}
		for _, t := range tables {
			want[murmur3h1([]byte(tablePath(t)))] = tablePath(t)
		}
		entries, _ := findEntries(gameDir, want)
		if e, ok := entries[textPath]; ok {
			if b, err := readArcEntry(e); err == nil {
				data.rawText = b
				data.text = stringTableFrom(b)
				data.keyOf = keysByHash(data.text)
			}
		}
		for _, t := range tables {
			if e, ok := entries[tablePath(t)]; ok {
				if b, err := readArcEntry(e); err == nil {
					data.raw[t] = b
				}
			}
		}
	})
	return &data
}

// tr devolve o texto em inglês de uma chave (ou "" se não existir).
func (g *gameData) tr(key string) string {
	return strings.TrimSpace(g.text[key])
}

// keysByHash indexa as chaves de texto pelo lookup3, que é como missões e
// objetivos aparecem no save ("tta01.5" -> nome "tta01.5_title").
func keysByHash(text map[string]string) map[uint32]string {
	out := map[uint32]string{}
	for k := range text {
		base := k
		for _, suf := range []string{"_name", "_title", "_desc"} {
			base = strings.TrimSuffix(base, suf)
		}
		for _, c := range []string{k, base} {
			if _, ok := out[l3(c)]; !ok {
				out[l3(c)] = c
			}
		}
	}
	return out
}

// label devolve o texto de um ID do save (missão ou objetivo), ou "".
func (g *gameData) label(id uint32) string {
	k, ok := g.keyOf[id]
	if !ok {
		return ""
	}
	for _, c := range []string{k + "_name", k + "_title", k} {
		if t := g.tr(c); t != "" {
			return t
		}
	}
	return ""
}

// stringTableFrom lê o .stringlookup: pares ordenados {hash, texto, chave}
// apontando para um bloco de strings terminadas em zero.
func stringTableFrom(b []byte) map[string]string {
	out := map[string]string{}
	doc, err := loadADF(b)
	if err != nil {
		return out
	}
	for _, in := range doc.instances {
		if t := doc.types[in.typeHash]; t == nil || t.name != "StringLookup" {
			continue
		}
		d := doc.b
		base := uint64(in.off)
		if base+0x30 > uint64(len(d)) {
			return out
		}
		pairsOff := base + uint64(binary.LittleEndian.Uint32(d[base:]))
		pairs := binary.LittleEndian.Uint64(d[base+8:])
		textOff := base + uint64(binary.LittleEndian.Uint32(d[base+0x20:]))
		textLen := binary.LittleEndian.Uint64(d[base+0x28:])
		if textOff+textLen > uint64(len(d)) || pairsOff+pairs*12 > uint64(len(d)) {
			return out
		}
		text := d[textOff : textOff+textLen]
		cstr := func(o uint32) string {
			if uint64(o) >= uint64(len(text)) {
				return ""
			}
			s := text[o:]
			if i := bytes.IndexByte(s, 0); i >= 0 {
				s = s[:i]
			}
			return string(s)
		}
		for i := uint64(0); i < pairs; i++ {
			p := pairsOff + i*12
			out[cstr(binary.LittleEndian.Uint32(d[p+8:]))] = cstr(binary.LittleEndian.Uint32(d[p+4:]))
		}
	}
	return out
}

// csvTable é uma tabela .csvc: cabeçalho, linha de tipos e os dados.
type csvTable struct {
	cols map[string]int
	head []string
	rows [][]string
}

func parseTable(b []byte) csvTable {
	t := csvTable{cols: map[string]int{}}
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	all, _ := r.ReadAll()
	if len(all) < 2 {
		return t
	}
	for i, h := range all[0] {
		h = strings.TrimSpace(strings.TrimPrefix(h, string(rune(0xFEFF))))
		t.head = append(t.head, h)
		t.cols[h] = i
	}
	t.rows = all[2:] // a segunda linha só diz o tipo de cada coluna
	return t
}

func (t csvTable) get(row []string, col string) string {
	if i, ok := t.cols[col]; ok && i < len(row) {
		return strings.TrimSpace(row[i])
	}
	return ""
}

func (t csvTable) num(row []string, col string) float64 {
	v, _ := strconv.ParseFloat(t.get(row, col), 64)
	return v
}

func (t csvTable) find(col, value string) []string {
	for _, r := range t.rows {
		if t.get(r, col) == value {
			return r
		}
	}
	return nil
}
