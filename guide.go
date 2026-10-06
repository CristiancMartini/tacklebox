package main

// Guia de pesca: tudo vem das tabelas do próprio jogo (fish_codex_<reserva>,
// compatibilidade de iscas, itens e textos), então cobre todas as reservas e
// acompanha as atualizações do jogo sem depender de planilha.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Tag é um termo traduzido; En é como aparece no jogo.
type Tag struct {
	Name string `json:"name"`
	En   string `json:"en,omitempty"`
	Desc string `json:"desc,omitempty"`
}

type GuideBait struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`              // natural, viva, fundo, ceva
	Weight  int    `json:"weight"`            // preferência do peixe (5 a 40)
	Tier    int    `json:"tier"`              // 0 favorita, 1 boa, 2 às vezes
	HookMin int    `json:"hookMin,omitempty"` // anzóis compatíveis (1 = tamanho 10 ... 20 = 10/0)
	HookMax int    `json:"hookMax,omitempty"`
}

// GuideHook diz que troféus um tamanho de anzol consegue fisgar de um peixe:
// anzóis maiores afastam os peixes pequenos (no jogo, do 10 ao 1 e do 1/0 ao 10/0).
type GuideHook struct {
	Index int    `json:"index"` // 1 a 20
	Size  string `json:"size"`
	Ranks []int  `json:"ranks"` // 0 juvenil ... 5 lendário
}

// hookLabel devolve o tamanho de um anzol: 1..10 = "10".."1", 11..20 = "1/0".."10/0".
func hookLabel(i int) string {
	if i <= 10 {
		return strconv.Itoa(11 - i)
	}
	return strconv.Itoa(i-10) + "/0"
}

var rankIndex = map[string]int{"juvenile": 0, "bronze": 1, "silver": 2, "gold": 3, "diamond": 4, "legendary": 5}

// hookTable lê hook_meta_data: para cada FishID, os anzóis e os troféus que eles pegam.
// As células são "FishID/ranque/ranque..."; "gte" significa "deste para cima".
func hookTable(t csvTable) map[string][]GuideHook {
	out := map[string][]GuideHook{}
	for _, r := range t.rows {
		var idx int
		if _, err := fmt.Sscanf(t.get(r, "HookId"), "hook_%d", &idx); err != nil || idx < 1 {
			continue
		}
		for _, cell := range r[2:] {
			parts := strings.Split(strings.TrimSpace(cell), "/")
			if len(parts) < 2 {
				continue
			}
			var ranks []int
			for _, p := range parts[1:] {
				if p == "gte" && len(ranks) > 0 {
					for k := ranks[len(ranks)-1] + 1; k <= 4; k++ {
						ranks = append(ranks, k)
					}
					continue
				}
				if k, ok := rankIndex[p]; ok {
					ranks = append(ranks, k)
				}
			}
			if len(ranks) > 0 {
				out[parts[0]] = append(out[parts[0]], GuideHook{Index: idx, Size: hookLabel(idx), Ranks: ranks})
			}
		}
	}
	for id := range out {
		sort.Slice(out[id], func(a, b int) bool { return out[id][a].Index < out[id][b].Index })
	}
	return out
}

type GuideLure struct {
	Name   string   `json:"name"`
	Styles []string `json:"styles"` // recolhimentos com a maior preferência
	Weight int      `json:"weight"`
	Tier   int      `json:"tier"`
}

type GuideFish struct {
	ID        string      `json:"id"`
	SpeciesID uint32      `json:"speciesId"`
	Name      string      `json:"name"`
	Species   string      `json:"species,omitempty"` // espécie do lendário
	Icon      string      `json:"icon"`              // imagem do jogo (ui/shared/textures/items/...)
	Legendary bool        `json:"legendary"`
	Habitats  []Tag       `json:"habitats"`
	Foods     []Tag       `json:"foods"`
	Cover     []Tag       `json:"cover"`
	DepthMin  float64     `json:"depthMin"`
	DepthMax  float64     `json:"depthMax"`
	TempMin   float64     `json:"tempMin"`
	TempMax   float64     `json:"tempMax"`
	TempIdeal float64     `json:"tempIdeal"`
	Current   string      `json:"current"`
	Time      string      `json:"time"` // "dia", "noite" ou ""
	Traits    []Tag       `json:"traits"`
	KGMin     float64     `json:"kgMin"`
	KGMax     []float64   `json:"kgMax"` // teto de peso por medalha (juvenil..diamante); lendário: 1 valor
	Baits     []GuideBait `json:"baits"`
	Hooks     []GuideHook `json:"hooks"` // anzóis que pegam este peixe, do menor para o maior
	Lures     []GuideLure `json:"lures"`
}

type GuideReserve struct {
	World  string      `json:"world"`
	Name   string      `json:"name"`
	Region string      `json:"region"`
	Fish   []GuideFish `json:"fish"`
	Waters []Water     `json:"waters"`
	Spots  []Spot      `json:"spots"`
	Icons  []MapIcon   `json:"icons"`
	Lakes  []WaterTile `json:"-"`

	objectives []Objective
}

type Guide struct {
	OK       bool           `json:"ok"`
	Error    string         `json:"error,omitempty"`
	Reserves []GuideReserve `json:"reserves"`
	Sheet    string         `json:"sheet"`
}

// Planilha da comunidade (Discord oficial do jogo), só como link creditado.
const communitySheet = "https://docs.google.com/spreadsheets/d/1Es_hECA7EiUO1r1FiPxnmmxtoTMsFkX4cjK39zZH_Fc/edit"

var habitatNames = map[string]string{
	"lakeshore": "Margem do lago", "shallowlake": "Lago raso", "deeplake": "Lago fundo",
	"shallowpond": "Lagoa rasa", "deeppond": "Lagoa funda", "wetlandpond": "Lagoa alagada",
	"wetlandriver": "Rio alagado", "upriver": "Alto rio", "middleriver": "Médio rio",
	"rivermouth": "Foz do rio", "upstream": "Alto córrego", "middlestream": "Médio córrego",
	"lowstream": "Baixo córrego",
}

// texto do jogo para cada habitat (as chaves de texto não seguem o nome interno)
var habitatEn = map[string]string{
	"lakeshore": "fish_habitat_lake_shore_title", "shallowlake": "fish_habitat_lake_shallow_title",
	"deeplake": "fish_habitat_lake_deep_title", "shallowpond": "fish_habitat_pond_shallow_title",
	"deeppond": "fish_habitat_pond_deep_title", "wetlandpond": "fish_habitat_pond_wetland_title",
	"wetlandriver": "fish_habitat_river_wetland_title", "upriver": "fish_habitat_river_up_title",
	"middleriver": "fish_habitat_river_middle_title", "rivermouth": "fish_habitat_river_mouth_title",
	"upstream": "fish_habitat_stream_up_title", "middlestream": "fish_habitat_stream_middle_title",
	"lowstream": "fish_habitat_stream_low_title",
}

var foodNames = map[string]string{
	"insects": "Insetos", "algae": "Algas", "plants": "Plantas", "zooplankton": "Zooplâncton",
	"amphibians": "Anfíbios", "smallfish": "Peixes pequenos", "crustaceans": "Crustáceos",
	"smallmammals": "Pequenos mamíferos",
}

var coverNames = map[string]string{
	"log": "Troncos", "pier": "Píeres", "boulder": "Pedras",
	"bedveg": "Vegetação no fundo", "surfaceveg": "Vegetação na superfície",
}

var traitNames = map[string][2]string{
	"aggressive":            {"Agressivo", "Ataca direto, sem beliscar a isca."},
	"jumper":                {"Saltador", "Gosta de pular para fora d'água."},
	"hardfighter":           {"Brigador", "Faz de tudo para escapar do anzol."},
	"laststand":             {"Último fôlego", "Briga com força renovada quando está quase sendo pego."},
	"spiker":                {"Arrancador", "Finge que cansou e de repente puxa a linha com toda a força."},
	"easilyspooked":         {"Assustadiço", "Se assusta fácil com a presença humana."},
	"bottomlurker":          {"Fundeiro", "Prefere nadar perto do fundo."},
	"nightowl":              {"Noturno", "Mais ativo à noite."},
	"sunlover":              {"Diurno", "Mais ativo depois que o sol nasce."},
	"keensenses":            {"Faro apurado", "Sente comida de longe e responde bem a iscas artificiais."},
	"quickrecovery":         {"Recupera rápido", "Recupera o fôlego duas vezes mais rápido nas pausas da briga."},
	"roller":                {"Rodopiador", "Dá giros repentinos durante a briga."},
	"lowertensiontolerance": {"Linha sensível", "Escapa mais fácil com a linha muito esticada."},
	"anthropophobia":        {"Distante", "Fica longe das pessoas, a não ser durante a briga."},
	"powerswimmer":          {"Ousado", "Gosta de cachoeiras e correnteza forte."},
	"mirage":                {"Sol nascente", "Sobe à superfície de dia, mas se esconde ao ver o equipamento."},
}

var baitKinds = map[string]string{
	"natural": "natural", "live": "viva", "bottom_bait": "fundo",
	"groundbait": "ceva", "throwable_bait_sub": "ceva",
}

var retrieveStyles = map[string]string{
	"Constant": "Contínuo", "StopAndGo": "Stop & Go", "Twitching": "Twitching", "Jigging": "Jigging",
}

var styleOrder = []string{"Constant", "StopAndGo", "Twitching", "Jigging"}

func normKey(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

// splitTags separa "A/B/C", traduz e tira repetidos.
func splitTags(s string, tr func(key, raw string) (Tag, bool)) []Tag {
	out := []Tag{}
	seen := map[string]bool{}
	for _, part := range strings.Split(s, "/") {
		part = strings.TrimSpace(part)
		k := normKey(part)
		if k == "" {
			continue
		}
		t, ok := tr(k, part)
		if !ok || seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, t)
	}
	return out
}

// legendTraits indexa os comportamentos especiais (títulos "..._name"/"..._title" com "trait")
// pelo nome normalizado, que é como aparecem nas tabelas ("EagleEyed" -> "Eagle Eyed").
func legendTraits(gd *gameData) map[string]Tag {
	out := map[string]Tag{}
	for key, title := range gd.text {
		if !strings.Contains(key, "trait") {
			continue
		}
		var base string
		switch {
		case strings.HasSuffix(key, "_name"):
			base = strings.TrimSuffix(key, "_name")
		case strings.HasSuffix(key, "_title"):
			base = strings.TrimSuffix(key, "_title")
		default:
			continue
		}
		k := normKey(title)
		if _, ok := out[k]; k == "" || ok {
			continue
		}
		out[k] = Tag{Name: strings.TrimSpace(title), Desc: gd.tr(base + "_desc")}
	}
	return out
}

func buildGuide(gameDir string) Guide {
	gd := loadGameData(gameDir)
	g := Guide{Sheet: communitySheet, Reserves: []GuideReserve{}}
	if len(gd.raw) == 0 || len(gd.text) == 0 {
		g.Error = "Não encontrei os arquivos do jogo."
		return g
	}
	compat := parseTable(gd.raw["fish_codex_bait_compatibility"])
	items := parseTable(gd.raw["item_defs"])
	lureTable := parseTable(gd.raw["lure_groups"])
	special := legendTraits(gd)
	hooks := hookTable(parseTable(gd.raw["hook_meta_data"]))

	itemByID := map[string][]string{}
	for _, r := range items.rows {
		itemByID[items.get(r, "ItemId")] = r
	}
	lureName := map[string]string{}
	for _, r := range lureTable.rows {
		if len(r) >= 2 {
			lureName[strings.TrimSpace(r[0])] = gd.tr(strings.TrimSpace(r[1]))
		}
	}

	tackle := func(fishID string) ([]GuideBait, []GuideLure) {
		baits, lures := []GuideBait{}, []GuideLure{}
		row := compat.find("FishID", fishID)
		if row == nil {
			return baits, lures
		}
		best := map[string]int{}
		baitHooks := map[string][2]int{}
		var order []string
		styles := map[string]map[string]int{}
		var lureOrder []string
		for i, col := range compat.head {
			if i == 0 || i >= len(row) {
				continue
			}
			w, _ := strconv.Atoi(strings.TrimSpace(row[i]))
			if w <= 0 {
				continue
			}
			if group, style, ok := strings.Cut(col, "_"); ok && retrieveStyles[style] != "" && lureName[group] != "" {
				if styles[group] == nil {
					styles[group] = map[string]int{}
					lureOrder = append(lureOrder, group)
				}
				styles[group][style] = w
				continue
			}
			it := itemByID[col]
			if it == nil {
				continue
			}
			name := gd.tr(items.get(it, "GuiNameId"))
			kind := baitKinds[items.get(it, "SubCategoryId")]
			if name == "" || kind == "" {
				continue
			}
			key := kind + "\x00" + name
			if _, ok := best[key]; !ok {
				order = append(order, key)
			}
			var a, z int
			if n, _ := fmt.Sscanf(items.get(it, "CompatibleItems"), "hook#%d#%d", &a, &z); n == 2 {
				baitHooks[key] = [2]int{a, z}
			}
			if w > best[key] {
				best[key] = w
			}
		}
		for _, key := range order {
			kind, name, _ := strings.Cut(key, "\x00")
			b := GuideBait{Name: name, Kind: kind, Weight: best[key]}
			if hr, ok := baitHooks[key]; ok {
				b.HookMin, b.HookMax = hr[0], hr[1]
			}
			baits = append(baits, b)
		}
		sort.SliceStable(baits, func(i, j int) bool { return baits[i].Weight > baits[j].Weight })
		for _, group := range lureOrder {
			l := GuideLure{Name: lureName[group], Styles: []string{}}
			for _, s := range styleOrder {
				if w := styles[group][s]; w > l.Weight {
					l.Weight = w
				}
			}
			for _, s := range styleOrder {
				if styles[group][s] == l.Weight {
					l.Styles = append(l.Styles, retrieveStyles[s])
				}
			}
			lures = append(lures, l)
		}
		sort.SliceStable(lures, func(i, j int) bool { return lures[i].Weight > lures[j].Weight })

		// "favorita" é relativa ao peixe: alguns têm 15 como maior preferência
		top := 0
		for _, b := range baits {
			if b.Kind != "ceva" && b.Weight > top {
				top = b.Weight
			}
		}
		for _, l := range lures {
			if l.Weight > top {
				top = l.Weight
			}
		}
		if top > 35 {
			top = 35
		}
		tier := func(w int) int {
			switch {
			case w >= top:
				return 0
			case w >= 15:
				return 1
			}
			return 2
		}
		for i := range baits {
			baits[i].Tier = tier(baits[i].Weight)
		}
		for i := range lures {
			lures[i].Tier = tier(lures[i].Weight)
		}
		return baits, lures
	}

	traits := func(s string) ([]Tag, string) {
		when := ""
		tags := splitTags(s, func(k, raw string) (Tag, bool) {
			switch k {
			case "nightowl":
				when = "noite"
			case "sunlover", "mirage":
				when = "dia"
			}
			if t, ok := traitNames[k]; ok {
				en := special[k].Name
				return Tag{Name: t[0], En: en, Desc: t[1]}, true
			}
			if t, ok := special[k]; ok && t.Desc != "" {
				return t, true
			}
			return Tag{}, false // comportamentos internos que o jogo não mostra
		})
		return tags, when
	}

	current := func(speed float64) string {
		switch {
		case speed < 0.1:
			return "Água parada"
		case speed < 0.4:
			return "Correnteza fraca"
		case speed < 0.7:
			return "Correnteza média"
		}
		return "Correnteza forte"
	}

	speciesName := map[string]string{}
	for _, w := range vegWorlds {
		t := parseTable(gd.raw["fish_codex_"+w])
		if len(t.rows) == 0 {
			continue
		}
		res := GuideReserve{World: w, Name: mapNames[w][0], Region: mapNames[w][1], Fish: []GuideFish{}}
		for _, r := range t.rows {
			id := t.get(r, "FishID")
			if id == "" || strings.EqualFold(id, "TUTORIAL") || strings.Contains(strings.ToLower(id), "halloween") {
				continue
			}
			f := GuideFish{
				ID:        id,
				SpeciesID: l3(t.get(r, "FishSpecies")),
				Name:      gd.tr(t.get(r, "FishName")),
				Icon:      strings.TrimSuffix(t.get(r, "IconName"), ".png"),
				DepthMin:  t.num(r, "SpawnDepthMin"),
				DepthMax:  t.num(r, "SpawnDepthMax"),
				TempMin:   t.num(r, "WaterTempMin"),
				TempMax:   t.num(r, "WaterTempMax"),
				TempIdeal: t.num(r, "WaterTempIdeal"),
				Current:   current(t.num(r, "WaterSpeedIdeal")),
				KGMin:     t.num(r, "KG_MIN"),
			}
			if f.Name == "" {
				f.Name = id
			}
			speciesName[t.get(r, "FishSpecies")] = f.Name
			f.Habitats = splitTags(t.get(r, "Habitat"), func(k, raw string) (Tag, bool) {
				if n, ok := habitatNames[k]; ok {
					return Tag{Name: n, En: gd.tr(habitatEn[k])}, true
				}
				return Tag{Name: raw}, true
			})
			f.Foods = splitTags(t.get(r, "FoodTypes"), func(k, raw string) (Tag, bool) {
				if n, ok := foodNames[k]; ok {
					return Tag{Name: n}, true
				}
				return Tag{Name: "Peixes pequenos", En: raw}, true // espécies de isca (sculpin, dace...)
			})
			f.Cover = splitTags(t.get(r, "CoverType"), func(k, raw string) (Tag, bool) {
				if n, ok := coverNames[k]; ok {
					return Tag{Name: n}, true
				}
				return Tag{}, false
			})
			f.Traits, f.Time = traits(t.get(r, "Traits"))
			for _, rank := range []string{"Juvenile", "Bronze", "Silver", "Gold", "Diamond"} {
				f.KGMax = append(f.KGMax, t.num(r, "MAX_KG_"+rank))
			}
			f.Baits, f.Lures = tackle(id)
			f.Hooks = hooks[id]
			if f.Hooks == nil {
				f.Hooks = []GuideHook{}
			}
			res.Fish = append(res.Fish, f)
		}
		sort.SliceStable(res.Fish, func(i, j int) bool { return res.Fish[i].Name < res.Fish[j].Name })

		zones := map[string]string{}
		lt := parseTable(gd.raw["fish_codex_legendary_"+w])
		for _, r := range lt.rows {
			id := lt.get(r, "FishID")
			if id == "" || lt.get(r, "ShowInHandbook") == "FALSE" {
				continue
			}
			f := GuideFish{
				ID:        id,
				SpeciesID: l3(lt.get(r, "FishSpecies")),
				Name:      gd.tr(lt.get(r, "FishName")),
				Species:   speciesName[lt.get(r, "FishSpecies")],
				Icon:      strings.TrimSuffix(lt.get(r, "IconName"), ".png"),
				Legendary: true,
				DepthMin:  lt.num(r, "SpawnDepthMin"),
				DepthMax:  lt.num(r, "SpawnDepthMax"),
				KGMin:     lt.num(r, "KG_MIN"),
				KGMax:     []float64{lt.num(r, "KG_MAX")},
				Habitats:  []Tag{},
				Foods:     []Tag{},
				Cover:     []Tag{},
			}
			if f.Name == "" {
				f.Name = id
			}
			if f.Species == "" {
				f.Species = loadGameNames(gameDir).species[f.SpeciesID]
			}
			f.Traits, f.Time = traits(lt.get(r, "Traits"))
			f.Baits, f.Lures = tackle(id)
			f.Hooks = hooks[id]
			if f.Hooks == nil {
				f.Hooks = []GuideHook{}
			}
			res.Fish = append(res.Fish, f)
			for _, z := range strings.Split(lt.get(r, "SpawnZones"), "/") {
				if z = strings.TrimSpace(z); z != "" {
					zones[z] = id
				}
			}
		}
		ids := make([]string, 0, len(res.Fish))
		for _, f := range res.Fish {
			ids = append(ids, f.ID)
		}
		pl := loadPlaces(gameDir, w, ids, zones)
		res.Waters, res.Spots, res.Icons, res.Lakes, res.objectives = pl.Waters, pl.Spots, pl.Icons, pl.Lakes, pl.Objectives
		g.Reserves = append(g.Reserves, res)
	}
	g.OK = len(g.Reserves) > 0
	if !g.OK {
		g.Error = "As tabelas de peixes não foram encontradas nesta versão do jogo."
	}
	return g
}
