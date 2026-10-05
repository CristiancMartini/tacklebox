package main

// Onde o jogador pegou cada peixe. O save não guarda a posição da captura, então
// o Tacklebox anota a posição ao vivo quando aparece uma captura nova no save.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type MyCatch struct {
	World     string  `json:"world"`
	SpeciesID uint32  `json:"speciesId"`
	X         float64 `json:"x"`
	Z         float64 `json:"z"`
	Date      int64   `json:"date"`
	Weight    float64 `json:"weight"`
	Rank      int     `json:"rank"`
}

var myCatchesMu sync.Mutex

func myCatchesPath() string { return filepath.Join(filepath.Dir(configPath()), "capturas.json") }

func loadMyCatches() []MyCatch {
	myCatchesMu.Lock()
	defer myCatchesMu.Unlock()
	var out []MyCatch
	if b, err := os.ReadFile(myCatchesPath()); err == nil {
		json.Unmarshal(b, &out)
	}
	if out == nil {
		out = []MyCatch{}
	}
	return out
}

// recordCatches anota as capturas mais novas que since, usando a posição ao vivo
// (só se o jogador estiver na mesma reserva). Guarda as 1000 mais recentes.
func recordCatches(catches []CatchInfo, since int64, l Live) {
	if !l.OK || l.World == "" {
		return
	}
	all := loadMyCatches()
	have := map[int64]bool{}
	for _, c := range all {
		have[c.Date] = true
	}
	added := false
	for _, c := range catches {
		if c.Date <= since || have[c.Date] || c.World != l.World {
			continue
		}
		all = append(all, MyCatch{World: c.World, SpeciesID: c.SpeciesID, X: l.X, Z: l.Z, Date: c.Date, Weight: c.Weight, Rank: c.Rank})
		added = true
	}
	if !added {
		return
	}
	if len(all) > 1000 {
		all = all[len(all)-1000:]
	}
	myCatchesMu.Lock()
	defer myCatchesMu.Unlock()
	os.MkdirAll(filepath.Dir(myCatchesPath()), 0o755)
	if b, err := json.Marshal(all); err == nil {
		os.WriteFile(myCatchesPath(), b, 0o644)
	}
}
