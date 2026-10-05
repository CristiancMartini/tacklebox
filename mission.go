package main

// Destino da missão atual: o texto que o jogo mostra na tela (lido ao vivo) é
// ligado à chave de texto, dela ao objetivo nos arquivos de missão e daí à área
// onde ele acontece. Sem texto, valem os objetivos em andamento do save. Quando a
// missão tem ícones no mapa do jogo, a seta aponta para o ícone mais próximo.

import (
	"math"
	"strings"
	"sync"
)

// Target é para onde a seta da missão aponta.
type Target struct {
	X          float64 `json:"x"`
	Z          float64 `json:"z"`
	Label      string  `json:"label"`
	Travel     string  `json:"travel,omitempty"`     // viagem rápida desbloqueada que encurta o caminho
	TravelX    float64 `json:"travelX,omitempty"`    //
	TravelZ    float64 `json:"travelZ,omitempty"`    //
	TravelDist float64 `json:"travelDist,omitempty"` // do ponto de viagem até o objetivo
}

var (
	textKeysOnce sync.Once
	textKeys     map[string][]string // texto em inglês -> chaves
)

func keysForText(gd *gameData, text string) []string {
	textKeysOnce.Do(func() {
		textKeys = map[string][]string{}
		for k, v := range gd.text {
			v = strings.TrimSpace(v)
			textKeys[v] = append(textKeys[v], k)
		}
	})
	return textKeys[strings.TrimSpace(text)]
}

func keyTokens(k string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Split(strings.ToLower(k), "_") {
		switch t {
		case "", "name", "title", "desc":
		default:
			out[t] = true
		}
	}
	return out
}

// missionTarget calcula o destino; nil quando não dá para saber.
func missionTarget(g *Guide, st *Stats, l Live) *Target {
	if g == nil || !l.OK || l.World == "" {
		return nil
	}
	var res *GuideReserve
	for i := range g.Reserves {
		if g.Reserves[i].World == l.World {
			res = &g.Reserves[i]
		}
	}
	if res == nil {
		return nil
	}
	gd := loadGameData("")

	// missão: pelo nome na tela; sem ele, a rastreada no save
	var mission uint32
	var tracked *MissionInfo
	if st != nil {
		for i := range st.Missions {
			if m := &st.Missions[i]; m.World == l.World && m.Tracked {
				tracked = m
			}
		}
	}
	if l.Mission != "" {
		for _, k := range keysForText(gd, l.Mission) {
			base := k
			for _, suf := range []string{"_name", "_title"} {
				base = strings.TrimSuffix(base, suf)
			}
			mission = l3(base)
			if tracked != nil && tracked.ID == mission {
				break
			}
		}
	} else if tracked != nil {
		mission = tracked.ID
	}
	inMission := func(o Objective) bool {
		if mission == 0 {
			return true
		}
		for _, m := range o.Missions {
			if m == mission {
				return true
			}
		}
		return false
	}

	// objetivo: pelo texto da tela (as palavras do ID estão na chave do texto)
	label := l.Objective
	var obj *Objective
	if label != "" {
		bestScore := 0
		for _, k := range keysForText(gd, label) {
			kt := keyTokens(k)
			for i := range res.objectives {
				o := &res.objectives[i]
				it := keyTokens(o.ID)
				all := len(it) > 0
				for t := range it {
					if !kt[t] {
						all = false
						break
					}
				}
				score := len(it)
				if inMission(*o) {
					score += 10
				}
				if all && score > bestScore {
					obj, bestScore = o, score
				}
			}
		}
	}
	// sem texto: o objetivo em andamento mais perto, segundo o save
	if obj == nil && tracked != nil && mission == tracked.ID {
		best := math.Inf(1)
		for _, h := range tracked.Active {
			for i := range res.objectives {
				if o := &res.objectives[i]; o.Hash == h {
					if d := math.Hypot(o.X-l.X, o.Z-l.Z); d < best {
						obj, best = o, d
					}
				}
			}
		}
		if obj != nil && label == "" {
			label = gd.label(obj.Hash)
		}
	}

	var t *Target
	if obj != nil {
		t = &Target{X: obj.X, Z: obj.Z, Label: label}
	}
	// ícones da missão no mapa do jogo: ajustam o ponto ou servem de destino
	if mission != 0 {
		best := math.Inf(1)
		var icon *MapIcon
		for i := range res.Icons {
			ic := &res.Icons[i]
			if ic.Mission != mission {
				continue
			}
			ref := [2]float64{l.X, l.Z}
			if t != nil {
				ref = [2]float64{t.X, t.Z}
			}
			if d := math.Hypot(ic.X-ref[0], ic.Z-ref[1]); d < best {
				icon, best = ic, d
			}
		}
		if icon != nil && (t == nil || best < 400) {
			if t == nil {
				t = &Target{Label: label}
			}
			t.X, t.Z = icon.X, icon.Z
		}
	}
	if t == nil {
		return nil
	}

	// viagem rápida já desbloqueada mais perto do destino, se encurtar bem o caminho
	if st != nil {
		unlocked := map[uint32]bool{}
		for _, h := range st.Travel {
			unlocked[h] = true
		}
		here := math.Hypot(t.X-l.X, t.Z-l.Z)
		best := math.Inf(1)
		for _, ic := range res.Icons {
			if !ic.Travel || !unlocked[ic.TravelID] || ic.Name == "" {
				continue
			}
			if d := math.Hypot(ic.X-t.X, ic.Z-t.Z); d < best {
				best = d
				t.Travel, t.TravelX, t.TravelZ, t.TravelDist = ic.Name, ic.X, ic.Z, d
			}
		}
		if t.Travel != "" && !(here > 1500 && best < here*0.5) {
			t.Travel, t.TravelX, t.TravelZ, t.TravelDist = "", 0, 0, 0
		}
	}
	return t
}
