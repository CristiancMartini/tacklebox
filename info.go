package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// ---------- tabela de gráficos (página "Gráficos") ----------

var (
	lvlOff    = []string{"Desligado", "Baixo", "Médio", "Alto", "Ultra"}
	lvlPotato = []string{"Batata", "Baixo", "Médio", "Alto", "Ultra"}
	lvlOnOff  = []string{"Desligado", "Ligado"}
	lvlShadow = []string{"Baixo", "Médio", "Alto", "Ultra"}
	lvlAniso  = []string{"1x", "2x", "4x", "8x", "16x"}
	lvlAA     = []string{"Nenhum", "FXAA", "TAA"}
	lvlPreset = []string{"Ultra", "Alto", "Médio", "Baixo", "Batata"}
	lvlScale  = []string{"Automático", "Desligado", "Manual"}
)

var settingInfo = map[string]struct {
	label  string
	values []string
}{
	"MotionBlur":                     {"Desfoque de movimento", lvlOnOff},
	"Aniso":                          {"Filtro anisotrópico", lvlAniso},
	"GeometryLodFactor":              {"Detalhe geométrico", lvlOff},
	"GeometricDetail":                {"Detalhe de objetos", lvlOff},
	"VegetationDetail":               {"Vegetação", lvlOff},
	"PlayerSelfShadow":               {"Sombra do personagem", lvlOnOff},
	"ShadowedLights":                 {"Luzes com sombra", lvlOff},
	"ShadowResolution":               {"Qualidade das sombras", lvlShadow},
	"PostEffects":                    {"Pós-processamento", lvlOnOff},
	"TextureDetail":                  {"Texturas", lvlPotato},
	"WaterDetail":                    {"Água", lvlPotato},
	"SSAOQuality":                    {"Oclusão de ambiente (SSAO)", lvlOff},
	"EdgeFade":                       {"Esmaecimento de bordas", lvlOnOff},
	"GlobalIllumination":             {"Iluminação global", lvlOnOff},
	"SSReflection":                   {"Reflexos de tela (SSR)", lvlOff},
	"DynamicReflections":             {"Reflexos dinâmicos", lvlOff},
	"VolumetricFog":                  {"Névoa volumétrica", lvlOnOff},
	"DOF":                            {"Profundidade de campo", lvlOnOff},
	"BokehDOF":                       {"Bokeh", lvlOnOff},
	"AntiAliasing":                   {"Anti-aliasing", lvlAA},
	"FidelityFXSuperResolution2Mode": {"FSR 2 (nível do preset)", lvlPreset},
	"LightShadingQuality":            {"Qualidade da iluminação", lvlOnOff},
	"SoftParticles":                  {"Partículas suaves", lvlOnOff},
	"ChromaticAberration":            {"Aberração cromática", lvlOnOff},
	"TerrainAnisotropic":             {"Filtro do terreno", lvlOnOff},
	"FidelityFXSharpeningUpsampling": {"Nitidez do FSR", lvlOnOff},
	"FrameScaleMode":                 {"Modo de escala", lvlScale},
	"FrameScaleMinimum_V2":           {"Resolução interna", nil},
	"VSync":                          {"VSync", lvlOnOff},
}

func formatSetting(key, v string) string {
	if v == "" {
		return "—"
	}
	if key == "FrameScaleMinimum_V2" {
		return v + "%"
	}
	if i, err := strconv.Atoi(v); err == nil {
		if vals := settingInfo[key].values; i >= 0 && i < len(vals) {
			return vals[i]
		}
	}
	return v
}

type GraphicsRow struct {
	Label   string `json:"label"`
	Current string `json:"current"`
	Target  string `json:"target"`
	Changed bool   `json:"changed"`
}

func graphicsTable(iniPath string, scale int) []GraphicsRow {
	cur, _ := readGraphics(iniPath)
	var rows []GraphicsRow
	for _, p := range perfValues(scale) {
		rows = append(rows, GraphicsRow{
			Label:   settingInfo[p.key].label,
			Current: formatSetting(p.key, cur[p.key]),
			Target:  formatSetting(p.key, p.val),
			Changed: cur[p.key] != p.val,
		})
	}
	return rows
}

// ---------- mapas (página "Vegetação") ----------

type MapInfo struct {
	World   string `json:"world"`
	Name    string `json:"name"`
	Region  string `json:"region"`
	Objects int    `json:"objects"`
	Grass   int    `json:"grass"`
	Found   bool   `json:"found"`
}

// Nomes das reservas, conferidos nos textos do próprio jogo (reserve_<mundo>_*).
var mapNames = map[string][2]string{
	"achelous": {"Golden Ridge Reserve", "EUA"},
	"alpheus":  {"Trollsporet Nature Reserve", "Trøndelag, Noruega"},
	"belisama": {"Aguas Claras", "Málaga, Espanha"},
	"ceto":     {"Izilo Zasendulo", "Mpumalanga, África do Sul"},
	"doris":    {"Kamuibetsu", "Hokkaido, Japão"},
}

// analyzeMaps simula os dois modos em cada mapa (sem gravar nada).
func analyzeMaps(gameDir string) []MapInfo {
	entries, _ := findEntries(gameDir, vegWant())
	var out []MapInfo
	for _, w := range vegWorlds {
		mi := MapInfo{World: w, Name: mapNames[w][0], Region: mapNames[w][1]}
		if e, ok := entries[vegFilePath(w)]; ok {
			if orig, err := readArcEntry(e); err == nil {
				_, mi.Objects, _ = patchVegetationInfo(orig, vegAll)
				_, mi.Grass, _ = patchVegetationInfo(orig, vegGrass)
				mi.Found = mi.Objects > 0
			}
		}
		out = append(out, mi)
	}
	return out
}

// ---------- jogo (página "Início") ----------

var buildRe = regexp.MustCompile(`"buildid"\s+"(\d+)"`)

func steamBuild(gameDir string) string {
	if gameDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(gameDir, "..", "..", "appmanifest_"+steamAppID+".acf"))
	if err != nil {
		return ""
	}
	if m := buildRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// gameBanner usa a arte que já vem na instalação do jogo (não é distribuída aqui).
func gameBanner(gameDir string) string {
	if gameDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(gameDir, "crash_handler_banner.png"))
	if err != nil || len(b) > 4<<20 {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b)
}
