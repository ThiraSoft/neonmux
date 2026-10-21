package fx

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
)

// Synthwave background: a striped sun and a scrolling perspective grid,
// quantized into big pixels.
const gridSrc = `//kage:unit pixels

package main

var Time float
var Size vec2
var Pixel float
var BG vec3
var Sky vec3
var Line vec3
var SunA vec3
var SunB vec3

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := floor(dstPos.xy/Pixel)*Pixel + Pixel*0.5
	uv := p / Size
	aspect := Size.x / Size.y
	h := 0.58
	col := BG
	if uv.y < h {
		t := uv.y / h
		col = mix(BG, Sky, t*t*0.7)
		R := 0.22
		c := vec2(0.5*aspect, h-R*0.55)
		q := vec2(uv.x*aspect, uv.y)
		d := length(q - c)
		col += SunB * 0.25 * exp(-max(d-R, 0.0)*7.0)
		if d < R {
			sy := (q.y - c.y) / R
			sun := mix(SunA, SunB, clamp(sy*0.5+0.5, 0.0, 1.0))
			gap := 0.0
			if sy > -0.1 {
				band := fract(sy*4.5 - Time*0.15)
				gap = step(band, (sy+0.1)*0.45)
			}
			col = mix(sun, col, gap)
		}
	} else {
		z := (uv.y - h) / (1.0 - h)
		depth := 1.0 / max(z, 0.002)
		wx := (uv.x - 0.5) * aspect * depth * 1.6
		wz := depth*0.9 + Time*0.9
		fx := aspect * depth * 1.6 / Size.x * Pixel
		fz := depth * depth * 0.9 / (1.0 - h) / Size.y * Pixel
		lx := step(abs(fract(wx+0.5)-0.5), fx*0.9)
		lz := step(abs(fract(wz+0.5)-0.5), fz*0.9)
		l := max(lx, lz) * smoothstep(0.0, 0.25, z)
		col = mix(BG*0.8, Line, l)
		col += Line * 0.55 * exp(-z*18.0)
	}
	return vec4(col, 1)
}
`

// Shaders holds the shaders compiled once at startup.
type Shaders struct {
	Grid *ebiten.Shader
}

// LoadShaders compiles the Kage shaders.
func LoadShaders() (*Shaders, error) {
	grid, err := ebiten.NewShader([]byte(gridSrc))
	if err != nil {
		return nil, err
	}
	return &Shaders{Grid: grid}, nil
}

func vec3(c color.RGBA) []float32 {
	return []float32{float32(c.R) / 255, float32(c.G) / 255, float32(c.B) / 255}
}
