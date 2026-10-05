package main

// Lugares de cada reserva, lidos dos arquivos do mundo (worlds/<reserva>/...):
//   - corpos d'água do sistema TruFish, cada um com a lista de espécies que vivem nele;
//   - pontos marcados pelo jogo para uma espécie: áreas de missão de pesca (que
//     forçam a espécie e a faixa de troféu), desafios de local e esconderijos de
//     lendários.
// Coordenadas do jogo: X para leste, Z para o sul (norte = -Z).

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Tipos de objeto (lookup3 do nome da classe) usados nos .blo.
const (
	classTruFishVolume = 0xff6ff534
	classTruFishFish   = 0xe32465ac
	classOverrideArea  = 0xe3eb78b9
	classLegendaryArea = 0xbc61eeae
)

// Water é um volume retangular (rotacionado) onde o jogo espalha peixes.
type Water struct {
	X    float64  `json:"x"`
	Z    float64  `json:"z"`
	AX   float64  `json:"ax"` // eixo X local (no plano), unitário
	AZ   float64  `json:"az"`
	HX   float64  `json:"hx"` // metade do tamanho no eixo X local
	HZ   float64  `json:"hz"` // metade do tamanho no eixo Z local
	Fish []string `json:"fish"`
}

// Spot é um ponto do mapa ligado a uma espécie.
type Spot struct {
	Fish    string  `json:"fish"` // FishID
	X       float64 `json:"x"`
	Z       float64 `json:"z"`
	Kind    string  `json:"kind"`    // "missao", "desafio" ou "lendario"
	RankMin int     `json:"rankMin"` // -1 quando o jogo não define
	RankMax int     `json:"rankMax"`
}

type worldPlaces struct {
	Waters []Water `json:"waters"`
	Spots  []Spot  `json:"spots"`
}

var trailingNum = regexp.MustCompile(`_\d+$`)

// loadPlaces lê os lugares de uma reserva. fishIDs são os FishID conhecidos
// (normais e lendários) e zones liga as zonas de lendário ("L_x_1") ao FishID.
func loadPlaces(gameDir, world string, fishIDs []string, zones map[string]string) worldPlaces {
	out := worldPlaces{Waters: []Water{}, Spots: []Spot{}}
	byHash := map[uint32]string{}
	for _, id := range fishIDs {
		byHash[l3(id)] = id
	}
	want := map[uint64]string{}
	for _, p := range placeFiles[world] {
		full := "worlds/" + world + "/" + p
		want[murmur3h1([]byte(full))] = full
	}
	entries, _ := findEntries(gameDir, want)
	seen := map[string]bool{}
	addSpot := func(s Spot) {
		k := s.Fish + "|" + s.Kind + "|" + strconv.Itoa(int(s.X/10)) + "|" + strconv.Itoa(int(s.Z/10))
		if s.Fish == "" || seen[k] {
			return
		}
		seen[k] = true
		out.Spots = append(out.Spots, s)
	}

	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		b, err := readArcEntry(entries[p])
		if err != nil {
			continue
		}
		f, err := parseRTPC(b)
		if err != nil {
			continue
		}
		missionFish := ""
		f.root.walk(func(n *rtpcNode) {
			if id := f.str(n, "mission_id"); strings.HasPrefix(id, "loc_") && missionFish == "" {
				base := trailingNum.ReplaceAllString(strings.TrimPrefix(id, "loc_"), "")
				for _, fid := range fishIDs {
					if strings.EqualFold(trailingNum.ReplaceAllString(fid, ""), base) {
						missionFish = fid
						break
					}
				}
			}
		})
		challengeDone := false
		f.root.walk(func(n *rtpcNode) {
			class, _ := f.u32(n, "_class_hash")
			m := f.mat4(n, "world")
			switch class {
			case classTruFishVolume:
				scale := f.vec3(n, "transform_scale")
				if m == nil || scale == nil {
					return
				}
				w := Water{X: m[12], Z: m[14], HX: scale[0] / 2, HZ: scale[2] / 2, Fish: []string{}}
				if l := math.Hypot(m[0], m[2]); l > 0 {
					w.AX, w.AZ = m[0]/l, m[2]/l
				} else {
					w.AX = 1
				}
				for _, c := range n.children {
					if h, ok := f.u32(c, "fish"); ok {
						if id, ok := byHash[h]; ok {
							w.Fish = append(w.Fish, id)
						}
					}
				}
				if len(w.Fish) > 0 {
					out.Waters = append(out.Waters, w)
				}
			case classOverrideArea:
				// a área tem a posição; cada filho diz uma espécie e a faixa de troféu
				if m == nil {
					return
				}
				for _, c := range append([]*rtpcNode{n}, n.children...) {
					h, ok := f.u32(c, "fish_override")
					if !ok {
						continue
					}
					s := Spot{Fish: byHash[h], X: m[12], Z: m[14], Kind: "missao", RankMin: -1, RankMax: -1}
					if v, ok := f.u32(c, "rank_override_min"); ok && v < 6 {
						s.RankMin = int(v)
					}
					if v, ok := f.u32(c, "rank_override"); ok && v < 6 {
						s.RankMax = int(v)
					}
					addSpot(s)
				}
			case classLegendaryArea:
				if m == nil {
					return
				}
				x, z := m[12], m[14]
				// o centro da zona é a média dos pontos do polígono (relativos à origem)
				if pts := f.floatArray(n, "polygon_points"); len(pts) >= 4 {
					var sx, sz float64
					k := 0
					for i := 0; i+3 < len(pts); i += 4 {
						sx += pts[i]
						sz += pts[i+2]
						k++
					}
					x += sx / float64(k)
					z += sz / float64(k)
				}
				addSpot(Spot{Fish: zones[f.str(n, "name")], X: x, Z: z, Kind: "lendario", RankMin: 5, RankMax: 5})
			default:
				// desafio de local: a área marcada é o primeiro objeto posicionado do arquivo
				if missionFish != "" && !challengeDone && m != nil && (m[12] != 0 || m[14] != 0) {
					challengeDone = true
					addSpot(Spot{Fish: missionFish, X: m[12], Z: m[14], Kind: "desafio", RankMin: -1, RankMax: -1})
				}
			}
		})
	}
	return out
}
