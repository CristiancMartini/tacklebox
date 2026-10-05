package main

// Texturas do jogo (.ddsc, cabeçalho "AVTX") convertidas em PNG para a interface.
// As imagens dos peixes são BC3 (DXT5); BC1 e RGBA sem compressão também são lidos.

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/png"
	"sync"
)

const (
	dxgiRGBA8     = 28
	dxgiRGBA8SRGB = 29
	dxgiBC1       = 71
	dxgiBC1SRGB   = 72
	dxgiBC3       = 77
	dxgiBC3SRGB   = 78
	dxgiBGRA8     = 87
	dxgiBGRA8SRGB = 91
)

// decodeAVTX lê a maior imagem (mip 0) guardada dentro do .ddsc.
func decodeAVTX(b []byte) (*image.NRGBA, error) {
	if len(b) < 0xc0 || string(b[:4]) != "AVTX" {
		return nil, errors.New("não é uma textura AVTX")
	}
	format := binary.LittleEndian.Uint32(b[8:])
	w := int(binary.LittleEndian.Uint16(b[12:]))
	h := int(binary.LittleEndian.Uint16(b[14:]))
	off := int(binary.LittleEndian.Uint32(b[0x20:]))
	size := int(binary.LittleEndian.Uint32(b[0x28:]))
	if w == 0 || h == 0 || off+size > len(b) {
		return nil, errors.New("textura incompleta")
	}
	data := b[off : off+size]
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	switch format {
	case dxgiBC1, dxgiBC1SRGB:
		return img, decodeBC(img, data, 8, false)
	case dxgiBC3, dxgiBC3SRGB:
		return img, decodeBC(img, data, 16, true)
	case dxgiRGBA8, dxgiRGBA8SRGB, dxgiBGRA8, dxgiBGRA8SRGB:
		if len(data) < w*h*4 {
			return nil, errors.New("textura incompleta")
		}
		copy(img.Pix, data[:w*h*4])
		if format == dxgiBGRA8 || format == dxgiBGRA8SRGB {
			for i := 0; i < len(img.Pix); i += 4 {
				img.Pix[i], img.Pix[i+2] = img.Pix[i+2], img.Pix[i]
			}
		}
		return img, nil
	}
	return nil, errors.New("formato de textura não suportado")
}

// decodeBC descomprime blocos 4x4 de BC1 (8 bytes) ou BC3 (16 bytes: alfa + cor).
func decodeBC(img *image.NRGBA, data []byte, blockSize int, alpha bool) error {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	bw, bh := (w+3)/4, (h+3)/4
	if len(data) < bw*bh*blockSize {
		return errors.New("textura incompleta")
	}
	var colors [4][4]uint8
	var alphas [8]uint8
	for by := 0; by < bh; by++ {
		for bx := 0; bx < bw; bx++ {
			blk := data[(by*bw+bx)*blockSize:]
			var abits uint64
			if alpha {
				alphas[0], alphas[1] = blk[0], blk[1]
				a0, a1 := int(blk[0]), int(blk[1])
				if a0 > a1 {
					for i := 1; i < 7; i++ {
						alphas[i+1] = uint8(((7-i)*a0 + i*a1) / 7)
					}
				} else {
					for i := 1; i < 5; i++ {
						alphas[i+1] = uint8(((5-i)*a0 + i*a1) / 5)
					}
					alphas[6], alphas[7] = 0, 255
				}
				for i := 0; i < 6; i++ {
					abits |= uint64(blk[2+i]) << (8 * i)
				}
				blk = blk[8:]
			}
			c0 := binary.LittleEndian.Uint16(blk[0:])
			c1 := binary.LittleEndian.Uint16(blk[2:])
			rgb := func(c uint16) [4]uint8 {
				r, g, b := uint8(c>>11&31), uint8(c>>5&63), uint8(c&31)
				return [4]uint8{r<<3 | r>>2, g<<2 | g>>4, b<<3 | b>>2, 255}
			}
			colors[0], colors[1] = rgb(c0), rgb(c1)
			mix := func(a, b [4]uint8, wa, wb, d int) [4]uint8 {
				var o [4]uint8
				for i := 0; i < 3; i++ {
					o[i] = uint8((int(a[i])*wa + int(b[i])*wb) / d)
				}
				o[3] = 255
				return o
			}
			if c0 > c1 || alpha {
				colors[2] = mix(colors[0], colors[1], 2, 1, 3)
				colors[3] = mix(colors[0], colors[1], 1, 2, 3)
			} else {
				colors[2] = mix(colors[0], colors[1], 1, 1, 2)
				colors[3] = [4]uint8{0, 0, 0, 0}
			}
			idx := binary.LittleEndian.Uint32(blk[4:])
			for py := 0; py < 4; py++ {
				y := by*4 + py
				if y >= h {
					break
				}
				for px := 0; px < 4; px++ {
					x := bx*4 + px
					if x >= w {
						continue
					}
					k := py*4 + px
					c := colors[idx>>(2*k)&3]
					if alpha {
						c[3] = alphas[abits>>(3*k)&7]
					}
					copy(img.Pix[img.PixOffset(x, y):], c[:])
				}
			}
		}
	}
	return nil
}

var (
	fishImgMu    sync.Mutex
	fishImgCache = map[string]string{}
)

// fishImage devolve a foto do peixe como data URL PNG ("" se não achar).
// size é "medium" (480x256) ou "thumb" (a média reduzida a 160x85, para listas).
func fishImage(gameDir, icon, size string) string {
	if icon == "" || (size != "medium" && size != "thumb" && size != "small") {
		return ""
	}
	key := size + "/" + icon
	fishImgMu.Lock()
	if v, ok := fishImgCache[key]; ok {
		fishImgMu.Unlock()
		return v
	}
	fishImgMu.Unlock()
	src := size
	if size == "thumb" {
		src = "medium"
	}
	path := "ui/shared/textures/items/" + src + "/" + icon + ".ddsc"
	entries, _ := findEntries(gameDir, map[uint64]string{murmur3h1([]byte(path)): path})
	out := ""
	if e, ok := entries[path]; ok {
		if b, err := readArcEntry(e); err == nil {
			if img, err := decodeAVTX(b); err == nil {
				if size == "thumb" {
					img = shrink(img, 3)
				}
				var buf bytes.Buffer
				if png.Encode(&buf, img) == nil {
					out = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
				}
			}
		}
	}
	fishImgMu.Lock()
	fishImgCache[key] = out
	fishImgMu.Unlock()
	return out
}

// shrink reduz a imagem por um fator inteiro (média de cada bloco, com alfa).
func shrink(src *image.NRGBA, f int) *image.NRGBA {
	w, h := src.Rect.Dx()/f, src.Rect.Dy()/f
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b, a int
			for dy := 0; dy < f; dy++ {
				for dx := 0; dx < f; dx++ {
					p := src.Pix[src.PixOffset(x*f+dx, y*f+dy):]
					pa := int(p[3])
					r += int(p[0]) * pa
					g += int(p[1]) * pa
					b += int(p[2]) * pa
					a += pa
				}
			}
			o := dst.Pix[dst.PixOffset(x, y):]
			if a > 0 {
				o[0], o[1], o[2] = uint8(r/a), uint8(g/a), uint8(b/a)
			}
			o[3] = uint8(a / (f * f))
		}
	}
	return dst
}

// fishImages carrega várias fotos de uma vez (uma só leitura do índice dos pacotes).
func fishImages(gameDir string, icons []string, size string) map[string]string {
	out := map[string]string{}
	src := size
	if size == "thumb" {
		src = "medium"
	}
	want := map[uint64]string{}
	fishImgMu.Lock()
	for _, icon := range icons {
		if v, ok := fishImgCache[size+"/"+icon]; ok {
			out[icon] = v
		} else if icon != "" {
			p := "ui/shared/textures/items/" + src + "/" + icon + ".ddsc"
			want[murmur3h1([]byte(p))] = p
		}
	}
	fishImgMu.Unlock()
	if len(want) == 0 {
		return out
	}
	entries, _ := findEntries(gameDir, want)
	for _, icon := range icons {
		if _, ok := out[icon]; ok || icon == "" {
			continue
		}
		url := ""
		if e, ok := entries["ui/shared/textures/items/"+src+"/"+icon+".ddsc"]; ok {
			if b, err := readArcEntry(e); err == nil {
				if img, err := decodeAVTX(b); err == nil {
					if size == "thumb" {
						img = shrink(img, 3)
					}
					var buf bytes.Buffer
					if png.Encode(&buf, img) == nil {
						url = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
					}
				}
			}
		}
		fishImgMu.Lock()
		fishImgCache[size+"/"+icon] = url
		fishImgMu.Unlock()
		out[icon] = url
	}
	return out
}
