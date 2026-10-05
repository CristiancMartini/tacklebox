package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// logFn recebe o nível ("ok", "info", "warn", "err") e a mensagem.
type logFn func(level, msg string)

// Options é o que a interface mostra; cada item representa o estado desejado
// (desligado = volta ao original).
type Options struct {
	Graphics   bool   `json:"graphics"`
	Scale      int    `json:"scale"`      // escala de resolução, 50 a 100
	Vegetation string `json:"vegetation"` // vegNormal, vegGrass ou vegAll
	Windows    bool   `json:"windows"`
	Priority   bool   `json:"priority"`
}

func defaultOptions() Options {
	return Options{Graphics: true, Scale: 67, Vegetation: vegAll, Windows: true, Priority: true}
}

func (o *Options) normalize() {
	if o.Scale < 50 || o.Scale > 100 {
		o.Scale = 67
	}
	switch o.Vegetation {
	case vegNormal, vegGrass, vegAll:
	default:
		o.Vegetation = vegAll
	}
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "AnglerOtimizador", "config.json")
}

func loadOptions() Options {
	o := defaultOptions()
	if b, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(b, &o)
	}
	o.normalize()
	return o
}

func saveOptions(o Options) {
	os.MkdirAll(filepath.Dir(configPath()), 0o755)
	if b, err := json.MarshalIndent(o, "", "  "); err == nil {
		os.WriteFile(configPath(), b, 0o644)
	}
}

// Env são os caminhos detectados no PC.
type Env struct {
	IniPath string
	GameExe string
	GameDir string
	ModDir  string // onde gravar o pacote do mod (normalmente = GameDir)
	Test    bool   // não mexe no registro nem abre o jogo
}

func detectEnv(iniOverride, modOverride string, test bool) Env {
	e := Env{IniPath: iniOverride, Test: test}
	if e.IniPath == "" {
		e.IniPath = defaultIniPath()
	}
	e.GameExe = findGameExe()
	if e.GameExe != "" {
		e.GameDir = filepath.Dir(e.GameExe)
	}
	e.ModDir = modOverride
	if e.ModDir == "" {
		e.ModDir = e.GameDir
	}
	return e
}

var errGameOpen = errors.New("o jogo está aberto; feche o jogo antes")

// Apply deixa o PC no estado pedido em o e guarda as opções para a próxima vez.
// Devolve false se algum passo falhou.
func Apply(env Env, o Options, log logFn) bool {
	o.normalize()
	saveOptions(o)
	return applyState(env, o, log)
}

// Undo volta tudo ao original, sem mexer nas opções salvas.
func Undo(env Env, log logFn) bool {
	o := loadOptions()
	o.Graphics, o.Windows, o.Vegetation = false, false, vegNormal
	return applyState(env, o, log)
}

func applyState(env Env, o Options, log logFn) bool {
	ok := true
	step := func(okMsg string, err error) {
		if err != nil {
			ok = false
			log("err", err.Error())
		} else if okMsg != "" {
			log("ok", okMsg)
		}
	}

	// gráficos
	if _, err := os.Stat(env.IniPath); err != nil {
		step("", errors.New("o jogo ainda não criou o settings.ini; abra o jogo uma vez e tente de novo"))
	} else if o.Graphics {
		step("Gráficos de desempenho aplicados (resolução "+itoa(o.Scale)+"% + FSR 2)", applyPerf(env.IniPath, o.Scale))
	} else {
		restored, err := restoreGraphics(env.IniPath)
		if restored || err != nil {
			step("Gráficos originais restaurados", err)
		}
	}

	// Windows
	if !env.Test {
		if o.Windows {
			if env.GameExe != "" {
				step("Jogo usando a placa de vídeo dedicada", setHighPerfGPU(env.IniPath, env.GameExe))
			}
			step("Gravação em segundo plano do Game Bar desligada", disableGameDVR(env.IniPath))
		} else if windowsTweaksApplied(env.IniPath) {
			restoreRegistry(env.IniPath)
			log("ok", "Ajustes do Windows desfeitos")
		}
	}

	// vegetação
	switch {
	case env.GameDir == "":
		step("", errors.New("não achei a pasta do jogo na Steam"))
	case o.Vegetation == vegNormal:
		if err := uninstallVegMod(env.ModDir); err == nil {
			log("ok", "Vegetação de volta ao normal")
		}
	default:
		what := "Grama, mato e pedras escondidos (árvores ficam)"
		if o.Vegetation == vegAll {
			what = "Grama, mato, pedras e árvores escondidos"
		}
		log("info", "Gerando o pacote de vegetação a partir dos arquivos do jogo...")
		step(what, installVegMod(env.GameDir, env.ModDir, o.Vegetation, log))
	}
	return ok
}

// Status é o que a interface mostra no topo.
type Status struct {
	GameFound     bool   `json:"gameFound"`
	GamePath      string `json:"gamePath"`
	IniFound      bool   `json:"iniFound"`
	GameRunning   bool   `json:"gameRunning"`
	GraphicsOn    bool   `json:"graphicsOn"`
	VegInstalled  bool   `json:"vegInstalled"`
	WindowsTweaks bool   `json:"windowsTweaks"`
}

func getStatus(env Env) Status {
	s := Status{GameFound: env.GameExe != "", GamePath: env.GameDir, GameRunning: gameRunning()}
	if _, err := os.Stat(env.IniPath); err == nil {
		s.IniFound = true
		s.GraphicsOn, _ = graphicsApplied(env.IniPath)
		s.WindowsTweaks = windowsTweaksApplied(env.IniPath)
	}
	if env.ModDir != "" {
		s.VegInstalled = vegModInstalled(env.ModDir)
	}
	return s
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
