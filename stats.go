package main

// Estatísticas lidas do save local do jogador (somente leitura).

import (
	"bytes"
	"encoding/base64"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type CatchInfo struct {
	Species    string  `json:"species"`
	Reserve    string  `json:"reserve"`
	World      string  `json:"world"`
	Weight     float64 `json:"weight"`     // kg
	Length     float64 `json:"length"`     // m
	TruScore   float64 `json:"truScore"`   //
	Rank       int     `json:"rank"`       // 0 juvenil ... 5 lendário
	Date       int64   `json:"date"`       // unix
	CatchSecs  float64 `json:"catchSecs"`  // tempo de briga
	GameTime   float64 `json:"gameTime"`   // hora do jogo (0-24)
	WaterTempC float64 `json:"waterTempC"` //
}

type SpeciesStat struct {
	Species     string    `json:"species"`
	SpeciesID   uint32    `json:"speciesId"`
	Reserve     string    `json:"reserve"`
	World       string    `json:"world"`
	Caught      int       `json:"caught"`
	Escaped     int       `json:"escaped"`
	Day         int       `json:"day"`
	Night       int       `json:"night"`
	ByRank      []int     `json:"byRank"`
	TotalWeight float64   `json:"totalWeight"`
	Biggest     CatchInfo `json:"biggest"`
	BestScore   CatchInfo `json:"bestScore"`
}

type ReserveStat struct {
	World          string  `json:"world"`
	Name           string  `json:"name"`
	Region         string  `json:"region"`
	Reputation     int     `json:"reputation"`
	Caught         int     `json:"caught"`
	Escaped        int     `json:"escaped"`
	Species        int     `json:"species"`
	Legendaries    int     `json:"legendaries"`
	PerfectCasts   int     `json:"perfectCasts"`
	LinesSnapped   int     `json:"linesSnapped"`
	StrikeOK       int     `json:"strikeOK"`
	StrikeFail     int     `json:"strikeFail"`
	FallenFromBoat int     `json:"fallenFromBoat"`
	DistFoot       float64 `json:"distFoot"` // km
	DistBoat       float64 `json:"distBoat"`
	DistCar        float64 `json:"distCar"`
	DistFast       float64 `json:"distFast"`
	HighestHeight  float64 `json:"highestHeight"` // m
	TotalWeight    float64 `json:"totalWeight"`
	Discovered     int     `json:"discovered"`
}

type PlayerStat struct {
	Name         string  `json:"name"`
	SteamID      string  `json:"steamId"`
	Avatar       string  `json:"avatar"`
	Level        int     `json:"level"`
	MaxLevel     int     `json:"maxLevel"`
	XP           int     `json:"xp"`
	LevelXP      int     `json:"levelXp"` // XP dentro do nível atual
	NextLevelXP  int     `json:"nextLevelXp"`
	Money        int     `json:"money"`
	Spent        int     `json:"spent"`
	Earned       int     `json:"earned"`
	Caught       int     `json:"caught"`
	Escaped      int     `json:"escaped"`
	Species      int     `json:"species"`
	Legendaries  int     `json:"legendaries"`
	StrikeRate   float64 `json:"strikeRate"`
	PerfectCasts int     `json:"perfectCasts"`
	TotalWeight  float64 `json:"totalWeight"`
	Created      int64   `json:"created"`
	Saved        int64   `json:"saved"`
}

type Stats struct {
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	Player   PlayerStat    `json:"player"`
	Reserves []ReserveStat `json:"reserves"`
	Species  []SpeciesStat `json:"species"`
	Catches  []CatchInfo   `json:"catches"`
	Best     []CatchInfo   `json:"best"`
	Modified int64         `json:"modified"`
}

// ---------- nomes vindos do jogo ----------

type gameNames struct {
	species map[uint32]string
	levels  []int // XP necessário para sair de cada nível (índice 0 = nível 1)
}

var (
	namesOnce sync.Once
	names     gameNames
)

func loadGameNames(gameDir string) gameNames {
	namesOnce.Do(func() {
		gd := loadGameData(gameDir)
		names = gameNames{species: speciesNamesFrom(gd.rawText), levels: levelTableFrom(gd.raw["progression_curves"])}
		// o nome oficial de cada espécie vem das tabelas de peixes (ID = lookup3 de FishSpecies)
		for _, w := range vegWorlds {
			t := parseTable(gd.raw["fish_codex_"+w])
			for _, r := range t.rows {
				if n := gd.tr(t.get(r, "FishName")); n != "" && t.get(r, "FishSpecies") != "" {
					names.species[l3(t.get(r, "FishSpecies"))] = n
				}
			}
		}
	})
	return names
}

var titleRe = regexp.MustCompile(`^[A-Z][A-Za-z'’\-]*( [A-Z][A-Za-z'’\-]*){0,3}$`)

// speciesNamesFrom monta hash -> nome a partir dos textos em inglês: o ID de cada
// espécie é o lookup3 do nome junto e minúsculo, às vezes com as palavras invertidas
// ("Largemouth Bass" -> largemouthbass, "Rock Bass" -> bassrock).
func speciesNamesFrom(b []byte) map[uint32]string {
	m := map[uint32]string{}
	fallback := map[uint32]string{}
	for _, s := range bytes.FieldsFunc(b, func(r rune) bool { return r < 32 }) {
		str := string(s)
		if len(str) > 40 || !titleRe.MatchString(str) {
			continue
		}
		var words []string
		for _, w := range strings.Fields(str) {
			w = strings.Map(func(r rune) rune {
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					return unicode.ToLower(r)
				}
				return -1
			}, w)
			if w != "" {
				words = append(words, w)
			}
		}
		if len(words) == 0 {
			continue
		}
		for _, key := range []string{strings.Join(words, ""), strings.Join(reversed(words), "")} {
			h := l3(key)
			if _, ok := m[h]; !ok {
				m[h] = str
			}
		}
		// chave de uma palavra só ("crappie" -> "Crappie"), sem sobrescrever nomes completos
		if len(words) > 1 {
			last := strings.Fields(str)[len(strings.Fields(str))-1]
			if h := l3(words[len(words)-1]); m[h] == "" {
				fallback[h] = last
			}
		}
	}
	for h, n := range fallback {
		if _, ok := m[h]; !ok {
			m[h] = n
		}
	}
	return m
}

func reversed(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

// levelTableFrom lê progression_curves.csvc (CSV: Level,XpIncrement,XpToNextLevel).
func levelTableFrom(b []byte) []int {
	var out []int
	next := 0.0
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 2 {
			continue
		}
		if _, err := strconv.Atoi(f[0]); err != nil {
			continue
		}
		inc, _ := strconv.ParseFloat(f[1], 64)
		if len(f) >= 3 && f[2] != "" {
			if v, err := strconv.ParseFloat(f[2], 64); err == nil {
				next = v
			}
		} else {
			next = math.Round(next * inc)
		}
		out = append(out, int(next))
	}
	return out
}

func levelFor(xp int, table []int) (level, inLevel, toNext int) {
	if len(table) == 0 {
		return 0, xp, 0
	}
	level = 1
	for _, need := range table {
		if xp < need {
			return level, xp, need
		}
		xp -= need
		level++
	}
	return len(table), xp, 0
}

// ---------- save ----------

func savesDir() string {
	base, err := windows.KnownFolderPath(windows.FOLDERID_SavedGames, 0)
	if err != nil || base == "" {
		base = filepath.Join(os.Getenv("USERPROFILE"), "Saved Games")
	}
	return filepath.Join(base, "Avalanche Studios", "CotWTheAngler", "Saves")
}

// findSave devolve o player_save_data mais recente e o SteamID da pasta.
func findSave() (path, steamID string, mod time.Time) {
	dirs, _ := os.ReadDir(savesDir())
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		p := filepath.Join(savesDir(), d.Name(), "player_save_data")
		if st, err := os.Stat(p); err == nil && st.ModTime().After(mod) {
			path, steamID, mod = p, d.Name(), st.ModTime()
		}
	}
	return
}

func readStats(env Env) Stats {
	path, steamID, mod := findSave()
	if path == "" {
		return Stats{Error: "Nenhum save encontrado. Jogue um pouco e volte aqui."}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Stats{Error: "Não consegui ler o save: " + err.Error()}
	}
	st, err := statsFromSave(raw, loadGameNames(env.GameDir))
	if err != nil {
		return Stats{Error: "Formato do save não reconhecido: " + err.Error()}
	}
	st.Modified = mod.Unix()
	st.Player.SteamID = steamID
	if !*flagAnonimo {
		st.Player.Name, st.Player.Avatar = steamPersona(steamID)
	}
	return st
}

func statsFromSave(raw []byte, gn gameNames) (Stats, error) {
	doc, err := loadADF(raw)
	if err != nil {
		return Stats{}, err
	}
	var root interface{}
	for _, in := range doc.instances {
		if t := doc.types[in.typeHash]; t != nil && strings.HasPrefix(t.name, "GameSaveData") {
			if root, err = doc.decode(in); err != nil {
				return Stats{}, err
			}
		}
	}
	if root == nil {
		return Stats{}, errors.New("GameSaveData não encontrado")
	}
	profile := get(root, "PlayerProfileData")
	prog := get(profile, "PlayerProgressionData")
	pstats := get(profile, "PlayerStatisticsData")

	speciesName := func(id float64) string {
		if n, ok := gn.species[uint32(id)]; ok {
			return n
		}
		return "Espécie #" + strconv.FormatUint(uint64(uint32(id)), 16)
	}
	worldByHash := map[uint32]string{}
	for w := range mapNames {
		worldByHash[l3(w)] = w
	}
	reserveName := func(h float64) (string, string, string) {
		w := worldByHash[uint32(h)]
		if n, ok := mapNames[w]; ok {
			return w, n[0], n[1]
		}
		if w == "" {
			w = strconv.FormatUint(uint64(uint32(h)), 16)
		}
		return w, w, ""
	}
	catch := func(c interface{}, world, reserve string) CatchInfo {
		return CatchInfo{
			Species:    speciesName(num(c, "FishSpeciesId")),
			Reserve:    reserve,
			World:      world,
			Weight:     num(c, "Weight"),
			Length:     num(c, "Length"),
			TruScore:   num(c, "TruScore"),
			Rank:       int(num(c, "Rank")),
			Date:       int64(num(c, "DateTimeCaught")),
			CatchSecs:  num(c, "TimeToCatchSeconds"),
			GameTime:   num(c, "GameTimeOfDay"),
			WaterTempC: num(c, "WaterTemp"),
		}
	}

	var s Stats
	s.OK = true
	rep := map[uint32]int{}
	for _, r := range list(prog, "WorldReputation") {
		rep[uint32(num(r, "WorldHash"))] = int(num(r, "Reputation"))
	}

	p := &s.Player
	p.XP = int(num(prog, "XP"))
	p.Level, p.LevelXP, p.NextLevelXP = levelFor(p.XP, gn.levels)
	p.MaxLevel = len(gn.levels)
	for _, c := range list(prog, "Currencies") {
		if num(c, "Id") == 0 {
			p.Money = int(num(c, "Amount"))
		}
	}
	if p.Money == 0 {
		p.Money = int(num(prog, "Credits"))
	}
	p.Spent = int(num(pstats, "TotalCreditsSpent"))
	p.Earned = int(num(pstats, "TotalCreditsFromSelling"))
	p.Created = int64(num(root, "SaveDataMeta", "TimestampCreated"))
	p.Saved = int64(num(root, "SaveDataMeta", "Timestamp"))

	strikeOK, strikeAll := 0, 0
	speciesSeen := map[string]bool{}
	for _, w := range list(pstats, "WorldStatisticsData") {
		world, name, region := reserveName(num(w, "WorldNameHash"))
		r := ReserveStat{
			World: world, Name: name, Region: region,
			Reputation:     rep[uint32(num(w, "WorldNameHash"))],
			PerfectCasts:   int(num(w, "PerfectCasts")),
			LinesSnapped:   int(num(w, "LinesSnapped")),
			StrikeOK:       int(num(w, "StrikeSuccessCount")),
			StrikeFail:     int(num(w, "StrikeFailCount")),
			FallenFromBoat: int(num(w, "TimesFallenFromBoat")),
			DistFoot:       num(w, "DistanceTravelledByFoot") / 1000,
			DistBoat:       num(w, "DistanceTravelledByBoat") / 1000,
			DistCar:        num(w, "DistanceTravelledByCar") / 1000,
			DistFast:       num(w, "DistanceFastTravelled") / 1000,
			HighestHeight:  num(w, "HighestHeight"),
			Legendaries:    len(list(w, "LegendariesCaught")),
		}
		for _, l := range list(w, "LocationStatisticsData") {
			if num(l, "Discovered") > 0 {
				r.Discovered++
			}
		}
		for _, f := range list(w, "FishSpeciesStatisticsData") {
			sp := SpeciesStat{
				Species:     speciesName(num(f, "FishSpeciesId")),
				SpeciesID:   uint32(num(f, "FishSpeciesId")),
				Reserve:     name,
				World:       world,
				Caught:      int(num(f, "NumCaught")),
				Escaped:     int(num(f, "NumEscaped")),
				Day:         int(num(f, "NumCaughtByDay")),
				Night:       int(num(f, "NumCaughtByNight")),
				TotalWeight: num(f, "TotalCaughtWeight"),
				Biggest:     catch(get(f, "BiggestCatch"), world, name),
				BestScore:   catch(get(f, "HighestTruScoreCatch"), world, name),
			}
			for _, n := range list(f, "NumCaughtByRank") {
				sp.ByRank = append(sp.ByRank, int(n.(float64)))
			}
			if sp.Caught == 0 && sp.Escaped == 0 {
				continue
			}
			r.Caught += sp.Caught
			r.Escaped += sp.Escaped
			r.TotalWeight += sp.TotalWeight
			if sp.Caught > 0 {
				r.Species++
				speciesSeen[sp.Species] = true
			}
			s.Species = append(s.Species, sp)
		}
		for _, c := range list(w, "LastCaughtFish") {
			if ci := catch(c, world, name); ci.Date > 0 {
				s.Catches = append(s.Catches, ci)
			}
		}
		p.Caught += r.Caught
		p.Escaped += r.Escaped
		p.Legendaries += r.Legendaries
		p.PerfectCasts += r.PerfectCasts
		p.TotalWeight += r.TotalWeight
		strikeOK += r.StrikeOK
		strikeAll += r.StrikeOK + r.StrikeFail
		s.Reserves = append(s.Reserves, r)
	}
	p.Species = len(speciesSeen)
	if strikeAll > 0 {
		p.StrikeRate = float64(strikeOK) / float64(strikeAll)
	}

	sort.Slice(s.Catches, func(i, j int) bool { return s.Catches[i].Date > s.Catches[j].Date })
	sort.Slice(s.Species, func(i, j int) bool { return s.Species[i].Caught > s.Species[j].Caught })
	sort.Slice(s.Reserves, func(i, j int) bool { return s.Reserves[i].Caught > s.Reserves[j].Caught })
	for _, sp := range s.Species {
		if sp.Biggest.Weight > 0 {
			s.Best = append(s.Best, sp.Biggest)
		}
	}
	sort.Slice(s.Best, func(i, j int) bool { return s.Best[i].TruScore > s.Best[j].TruScore })
	if len(s.Best) > 6 {
		s.Best = s.Best[:6]
	}
	return s, nil
}

// ---------- perfil da Steam (nome e avatar locais) ----------

func steamRoot() string {
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE); err == nil {
		defer k.Close()
		if p, _, err := k.GetStringValue("SteamPath"); err == nil {
			return filepath.FromSlash(p)
		}
	}
	return ""
}

func steamPersona(steamID string) (name, avatar string) {
	root := steamRoot()
	if root == "" || steamID == "" {
		return "", ""
	}
	if b, err := os.ReadFile(filepath.Join(root, "config", "loginusers.vdf")); err == nil {
		txt := string(b)
		if i := strings.Index(txt, `"`+steamID+`"`); i >= 0 {
			block := txt[i:]
			if j := strings.Index(block, "}"); j > 0 {
				block = block[:j]
			}
			if m := regexp.MustCompile(`"PersonaName"\s+"([^"]*)"`).FindStringSubmatch(block); m != nil {
				name = m[1]
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "config", "avatarcache", steamID+".png")); err == nil && len(b) < 2<<20 {
		avatar = "data:image/png;base64," + base64.StdEncoding.EncodeToString(b)
	}
	return name, avatar
}
