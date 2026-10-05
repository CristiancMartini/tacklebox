package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

const backupName = "settings.ini.backup-otimizador"

type kv struct{ key, val string }

// perfValues são os valores do preset oficial mais leve do jogo (tabela de presets
// decodificada do executável), com vegetação desligada, VSync desligado, escala de
// resolução manual (o FSR 2 reconstrói a imagem) e nitidez do FSR ligada.
// PostEffects e ChromaticAberration ficam em 1 porque é o valor de todos os presets.
func perfValues(scale int) []kv {
	if scale < 50 || scale > 100 {
		scale = 67
	}
	return []kv{
		{"MotionBlur", "0"},
		{"Aniso", "0"},
		{"GeometryLodFactor", "0"},
		{"GeometricDetail", "0"},
		{"VegetationDetail", "0"},
		{"PlayerSelfShadow", "0"},
		{"ShadowedLights", "0"},
		{"ShadowResolution", "0"},
		{"PostEffects", "1"},
		{"TextureDetail", "1"},
		{"WaterDetail", "1"},
		{"SSAOQuality", "0"},
		{"EdgeFade", "0"},
		{"GlobalIllumination", "0"},
		{"SSReflection", "0"},
		{"DynamicReflections", "0"},
		{"VolumetricFog", "0"},
		{"DOF", "0"},
		{"BokehDOF", "0"},
		{"AntiAliasing", "2"},
		{"FidelityFXSuperResolution2Mode", "3"},
		{"LightShadingQuality", "0"},
		{"SoftParticles", "0"},
		{"ChromaticAberration", "1"},
		{"TerrainAnisotropic", "0"},
		{"FidelityFXSharpeningUpsampling", "1"},
		{"FrameScaleMode", "2"},
		{"FrameScaleMinimum_V2", strconv.Itoa(scale)},
		{"VSync", "0"},
	}
}

func defaultIniPath() string {
	base, err := windows.KnownFolderPath(windows.FOLDERID_SavedGames, 0)
	if err != nil || base == "" {
		base = filepath.Join(os.Getenv("USERPROFILE"), "Saved Games")
	}
	return filepath.Join(base, "Avalanche Studios", "CotWTheAngler", "settings.ini")
}

// graphicsSection devolve o intervalo [start, end) das linhas da seção [Graphics].
func graphicsSection(lines []string) (int, int, error) {
	start, end := -1, len(lines)
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		if strings.EqualFold(t, "[Graphics]") {
			start = i
		}
	}
	if start < 0 {
		return 0, 0, errors.New("seção [Graphics] não encontrada no settings.ini")
	}
	return start, end, nil
}

func splitLines(content string) (lines []string, nl string) {
	nl = "\n"
	if strings.Contains(content, "\r\n") {
		nl = "\r\n"
	}
	return strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n"), nl
}

func readGraphics(iniPath string) (map[string]string, error) {
	b, err := os.ReadFile(iniPath)
	if err != nil {
		return nil, err
	}
	lines, _ := splitLines(string(b))
	start, end, err := graphicsSection(lines)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, l := range lines[start+1 : end] {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m, nil
}

// patchGraphics troca os valores dentro de [Graphics], preservando o resto do arquivo.
// Chaves que não existirem são adicionadas no fim da seção.
func patchGraphics(content string, vals []kv) (string, error) {
	lines, nl := splitLines(content)
	start, end, err := graphicsSection(lines)
	if err != nil {
		return "", err
	}
	want := map[string]string{}
	for _, p := range vals {
		want[p.key] = p.val
	}
	for i := start + 1; i < end; i++ {
		k, _, ok := strings.Cut(lines[i], "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if nv, found := want[k]; found {
			lines[i] = k + "=" + nv
			delete(want, k)
		}
	}
	var missing []string
	for _, p := range vals {
		if _, still := want[p.key]; still {
			missing = append(missing, p.key+"="+p.val)
			delete(want, p.key)
		}
	}
	if len(missing) > 0 {
		at := end
		for at > start+1 && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
		lines = append(lines[:at], append(missing, lines[at:]...)...)
	}
	return strings.Join(lines, nl), nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp-otimizador"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func setGraphics(iniPath string, vals []kv) error {
	b, err := os.ReadFile(iniPath)
	if err != nil {
		return err
	}
	out, err := patchGraphics(string(b), vals)
	if err != nil {
		return err
	}
	if err := writeAtomic(iniPath, []byte(out)); err != nil {
		return fmt.Errorf("não consegui salvar o settings.ini: %w", err)
	}
	g, err := readGraphics(iniPath)
	if err != nil {
		return err
	}
	for _, p := range vals {
		if g[p.key] != p.val {
			return fmt.Errorf("verificação falhou em %s", p.key)
		}
	}
	return nil
}

// applyPerf grava os gráficos de desempenho; antes da primeira vez guarda um backup.
func applyPerf(iniPath string, scale int) error {
	backup := filepath.Join(filepath.Dir(iniPath), backupName)
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		b, err := os.ReadFile(iniPath)
		if err != nil {
			return err
		}
		if err := writeAtomic(backup, b); err != nil {
			return fmt.Errorf("não consegui criar o backup: %w", err)
		}
	}
	return setGraphics(iniPath, perfValues(scale))
}

// restoreGraphics devolve só as opções gráficas que o otimizador mexe, a partir do
// backup; o resto do arquivo (controles, som etc.) fica como está agora.
func restoreGraphics(iniPath string) (bool, error) {
	orig, err := readGraphics(filepath.Join(filepath.Dir(iniPath), backupName))
	if err != nil {
		return false, nil // nunca aplicou: nada a restaurar
	}
	var vals []kv
	for _, p := range perfValues(67) {
		if v, ok := orig[p.key]; ok {
			vals = append(vals, kv{p.key, v})
		}
	}
	return true, setGraphics(iniPath, vals)
}

// graphicsApplied diz se o settings.ini está com os valores de desempenho.
func graphicsApplied(iniPath string) (bool, int) {
	g, err := readGraphics(iniPath)
	if err != nil {
		return false, 0
	}
	scale, _ := strconv.Atoi(g["FrameScaleMinimum_V2"])
	for _, p := range perfValues(scale) {
		if g[p.key] != p.val {
			return false, scale
		}
	}
	return true, scale
}
