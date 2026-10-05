package main

// Imagem do mapa de uma reserva: a cor do terreno do próprio jogo
// (terrain_color_gpu_2048, 16384 m em 2048 px = 8 m por pixel, norte em cima),
// escurecida para combinar com a interface, com lagos e rios em azul.

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"math"
	"sync"
)

const (
	mapWorldSize = 16384.0 // metros (WorldSize do .world de cada reserva)
	mapPixels    = 2048
)

var (
	mapMu    sync.Mutex
	mapCache = map[string]string{}
)

// mapImage devolve a imagem do mapa como data URL JPEG ("" se não houver terreno).
func mapImage(gameDir string, res *GuideReserve) string {
	mapMu.Lock()
	if v, ok := mapCache[res.World]; ok {
		mapMu.Unlock()
		return v
	}
	mapMu.Unlock()

	out := ""
	if img, err := gameTexture(gameDir, "worlds/"+res.World+"/terrain/terrain_color_gpu_2048.ddsc"); err == nil && img.Rect.Dx() == mapPixels {
		water := waterMask(res.Lakes)
		// dentro dos blocos de água, só o que tem cor de fundo de lago (pouco saturado);
		// a vegetação da margem fica de fora. Depois a borda é suavizada (maioria 3x3).
		for i := range water {
			if water[i] {
				p := img.Pix[i*4:]
				mx := math.Max(float64(p[0]), math.Max(float64(p[1]), float64(p[2])))
				mn := math.Min(float64(p[0]), math.Min(float64(p[1]), float64(p[2])))
				water[i] = mx > 0 && (mx-mn)/mx < 0.22
			}
		}
		smooth := make([]bool, len(water))
		for y := 1; y < mapPixels-1; y++ {
			for x := 1; x < mapPixels-1; x++ {
				n := 0
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if water[(y+dy)*mapPixels+x+dx] {
							n++
						}
					}
				}
				smooth[y*mapPixels+x] = n >= 5
			}
		}
		water = smooth
		dst := image.NewRGBA(img.Rect)
		for i := 0; i < len(img.Pix); i += 4 {
			r, g, b := float64(img.Pix[i]), float64(img.Pix[i+1]), float64(img.Pix[i+2])
			gray := 0.3*r + 0.59*g + 0.11*b
			// terra: menos saturada e mais escura
			r, g, b = (r*0.7+gray*0.3)*0.72, (g*0.7+gray*0.3)*0.72, (b*0.7+gray*0.3)*0.72
			if water[i/4] {
				r, g, b = r*0.35+28*0.65, g*0.35+86*0.65, b*0.35+128*0.65
			}
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r), uint8(g), uint8(b), 255
		}
		var buf bytes.Buffer
		if jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}) == nil {
			out = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
		}
	}
	mapMu.Lock()
	mapCache[res.World] = out
	mapMu.Unlock()
	return out
}

// waterMask marca os pixels cobertos pelos blocos de lagos e rios.
func waterMask(tiles []WaterTile) []bool {
	mask := make([]bool, mapPixels*mapPixels)
	scale := mapPixels / mapWorldSize
	for _, t := range tiles {
		// cantos do retângulo em pixels
		bx, bz := -t.AZ, t.AX // eixo Z local
		minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, s := range [][2]float64{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
			x := t.X + s[0]*t.HX*t.AX + s[1]*t.HZ*bx
			z := t.Z + s[0]*t.HX*t.AZ + s[1]*t.HZ*bz
			px, py := (x+mapWorldSize/2)*scale, (z+mapWorldSize/2)*scale
			minX, maxX = math.Min(minX, px), math.Max(maxX, px)
			minY, maxY = math.Min(minY, py), math.Max(maxY, py)
		}
		for py := int(math.Max(0, minY)); py <= int(math.Min(mapPixels-1, maxY)); py++ {
			for px := int(math.Max(0, minX)); px <= int(math.Min(mapPixels-1, maxX)); px++ {
				x := (float64(px)+0.5)/scale - mapWorldSize/2
				z := (float64(py)+0.5)/scale - mapWorldSize/2
				dx, dz := x-t.X, z-t.Z
				if math.Abs(dx*t.AX+dz*t.AZ) <= t.HX && math.Abs(dx*bx+dz*bz) <= t.HZ {
					mask[py*mapPixels+px] = true
				}
			}
		}
	}
	return mask
}
