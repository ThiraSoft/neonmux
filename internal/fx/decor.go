package fx

import (
	"image"
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// decor is a self-contained background: it keeps its own state, picks its
// own rate and draws itself. The first four backgrounds (grid, rain, stars,
// dust) live in scene.go, the later ones each have their own file.
type decor interface {
	fps() int
	step(dt float64)
	draw(dst *ebiten.Image, s *stage)
}

// releaser frees the video memory of a background being left.
type releaser interface{ release() }

// stage gathers what it takes to paint a background.
type stage struct {
	clip  image.Rectangle // repainted area, in screen coordinates
	size  image.Point     // size of the whole screen
	p     *gfx.Painter
	f     *gfx.Font
	th    *theme.Theme
	pixel float32 // size of a big pixel, in screen pixels
	// settled is true when the screen size has not moved for
	// resizeSettle: a background only rebuilds for a new size at that
	// point, and keeps going on the old one until then.
	settled bool
}

// resizeSettle is the quiet time that marks the end of a mouse resize.
// Rebuilding a background on every frame of the gesture costs up to 25 ms
// per frame (the swarm).
const resizeSettle = 0.18

// grain returns the integer size of a big pixel.
func (s *stage) grain() int { return max(1, int(s.pixel)) }

var decors = map[string]func(r *rand.Rand) decor{
	"aurora":  newAurora,
	"topo":    newTopo,
	"lava":    newLava,
	"fire":    newFire,
	"life":    newLife,
	"swarm":   newSwarm,
	"circuit": newCircuit,
	"scope":   newScope,
	"city":    newCity,
}

// pickDecor returns the self-contained background of the requested kind,
// created on first request. Switching kinds frees the previous one.
func (b *Background) pickDecor() decor {
	if b.decorKind == b.Kind {
		return b.decor
	}
	if r, ok := b.decor.(releaser); ok {
		r.release()
	}
	b.decor, b.decorKind = nil, b.Kind
	if mk := decors[b.Kind]; mk != nil {
		b.decor = mk(b.rng)
	}
	return b.decor
}

// Kage snippets shared by the background shaders. Gradients fall into hard
// flat bands, with no dithering: the pixel art grain comes from the big
// pixels and the color steps.
const kageHead = `//kage:unit pixels

package main

var Size vec2
var BG vec3
var FG vec3
var Accent vec3
var Accent2 vec3
var Warn vec3
`

const kageNoise = `
func hash(p vec2) float {
	p = fract(p * vec2(123.34, 456.21))
	p += dot(p, p+45.32)
	return fract(p.x * p.y)
}

// noise loops every 256 (noisePeriod): shifting its argument by a
// multiple of 256 changes nothing, and hash only gets small numbers.
func noise(p vec2) float {
	i := floor(p)
	f := fract(p)
	u := f * f * (3 - 2*f)
	a := hash(mod(i, 256.0))
	b := hash(mod(i+vec2(1, 0), 256.0))
	c := hash(mod(i+vec2(0, 1), 256.0))
	d := hash(mod(i+vec2(1, 1), 256.0))
	return mix(mix(a, b, u.x), mix(c, d, u.x), u.y)
}
`

// Backgrounds keep their clock in float64. The GPU computes in float32:
// after a day, a raw clock only has a few bits left after the decimal
// point and the noise freezes. So the shaders get phases that are already
// reduced, with no jump when they wrap: an angle modulo one turn, a noise
// offset modulo its period.
const noisePeriod = 256

// drift returns the noise offset t*speed, reduced to its period.
func drift(t, speed float64) float32 { return float32(math.Mod(t*speed, noisePeriod)) }

// phase returns the angle t*speed + at, reduced to one turn.
func phase(t, speed, at float64) float32 { return float32(math.Mod(t*speed+at, 2*math.Pi)) }

// frameStep cuts a background's clock at its rate: the number of the
// current step, used as a cache key, and the instant it started.
func frameStep(t float64, fps int) (int64, float64) {
	n := math.Floor(t * float64(fps))
	return int64(n), n / float64(fps)
}

const kageFlat = `
// flat snaps v (0 to 1) to n hard flat levels.
func flat(v float, n float) float {
	return clamp(floor(v*n+0.5)/n, 0.0, 1.0)
}
`

// shaderCache compiles each background shader on first use: a background
// that is never shown costs nothing at startup. A shader that does not
// compile (the tests check for it) simply leaves a plain backdrop.
var shaderCache = map[string]*ebiten.Shader{}

func lazyShader(src string) *ebiten.Shader {
	if sh, ok := shaderCache[src]; ok {
		return sh
	}
	sh, _ := ebiten.NewShader([]byte(src))
	shaderCache[src] = sh
	return sh
}

// lowres runs a shader into an image shrunk by an integer factor, then
// scales it up without smoothing. There are grain² times fewer fragments to
// compute, and the pixel art look comes for free.
type lowres struct {
	img   *ebiten.Image
	scale int
	key   any // what the last render used: nothing to redo if it has not changed
}

// render recomputes the image if key has changed. vars provides the
// uniforms, completed with Size, the size of the shrunk image.
func (l *lowres) render(sh *ebiten.Shader, s *stage, scale int, key any, vars func() map[string]any) {
	w, h := (s.size.X+scale-1)/scale, (s.size.Y+scale-1)/scale
	if l.img == nil || l.img.Bounds().Dx() != w || l.img.Bounds().Dy() != h {
		l.release()
		l.img = ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), &ebiten.NewImageOptions{Unmanaged: true})
		l.key = nil
	}
	l.scale = scale
	if sh == nil || (key == l.key && key != nil) {
		return
	}
	l.key = key
	u := vars()
	u["Size"] = []float32{float32(w), float32(h)}
	l.img.DrawRectShader(w, h, sh, &ebiten.DrawRectShaderOptions{Uniforms: u})
}

// blit scales the shrunk image up onto dst (clipped to the repainted area).
func (l *lowres) blit(dst *ebiten.Image, alpha float32) {
	if l.img == nil {
		return
	}
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(l.scale), float64(l.scale))
	op.ColorScale.ScaleAlpha(alpha)
	dst.DrawImage(l.img, op)
}

func (l *lowres) release() {
	if l.img != nil {
		l.img.Deallocate()
		l.img = nil
	}
}

// pixels is an RGBA buffer written by the CPU, then scaled up without
// smoothing (fire, game of life).
type pixels struct {
	img  *ebiten.Image
	buf  []byte
	w, h int
}

func (px *pixels) resize(w, h int) bool {
	if px.img != nil && px.w == w && px.h == h {
		return false
	}
	px.release()
	px.w, px.h = w, h
	px.buf = make([]byte, w*h*4)
	px.img = ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), &ebiten.NewImageOptions{Unmanaged: true})
	return true
}

func (px *pixels) release() {
	if px.img != nil {
		px.img.Deallocate()
		px.img = nil
	}
}

// uniforms builds the table of colors shared by the background shaders.
func uniforms(th *theme.Theme) map[string]any {
	return map[string]any{
		"BG":      vec3(th.BG),
		"FG":      vec3(th.FG),
		"Accent":  vec3(th.Accent),
		"Accent2": vec3(th.Accent2),
		"Warn":    vec3(th.Warn),
	}
}

// valueNoise is a smooth one-dimensional noise for the backgrounds
// computed on the CPU.
type valueNoise [256]float64

func newValueNoise(r *rand.Rand) valueNoise {
	var n valueNoise
	for i := range n {
		n[i] = r.Float64()
	}
	return n
}

func (n *valueNoise) at(x float64) float64 {
	i := math.Floor(x)
	f := x - i
	u := f * f * (3 - 2*f)
	a, b := n[int(i)&255], n[int(i+1)&255]
	return a + (b-a)*u
}

var quadVerts = make([]ebiten.Vertex, 4)

// scaleShader scales src up by a factor scale onto dst through a shader:
// the shader gets the position in src, in src pixels.
func scaleShader(dst, src *ebiten.Image, scale float32, sh *ebiten.Shader, u map[string]any, blend ebiten.Blend) {
	sw, sh2 := float32(src.Bounds().Dx()), float32(src.Bounds().Dy())
	W, H := sw*scale, sh2*scale
	for i, c := range [4][4]float32{{0, 0, 0, 0}, {W, 0, sw, 0}, {0, H, 0, sh2}, {W, H, sw, sh2}} {
		quadVerts[i] = ebiten.Vertex{DstX: c[0], DstY: c[1], SrcX: c[2], SrcY: c[3], ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1}
	}
	op := &ebiten.DrawTrianglesShaderOptions{Uniforms: u, Blend: blend}
	op.Images[0] = src
	dst.DrawTrianglesShader(quadVerts, []uint16{0, 1, 2, 1, 3, 2}, sh, op)
}
