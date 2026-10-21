package fx

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Oscilloscope in XY mode: a beam draws Lissajous figures on a persistent
// phosphor screen, in big pixels. The phosphor keeps the light in an image
// that is darkened at every step before the beam adds its own, in additive
// mode: where the beam slows down, at the edges of the figure, the trace is
// brighter, like on a real tube. The frequency ratios change every twenty
// seconds, gliding from one to the next, and a slight detuning makes the
// figure spin on itself.

// scopeSrc composites the phosphor: its light is snapped to five flat
// levels, at the grain of the big pixels.
const scopeSrc = `//kage:unit pixels

package main
` + kageFlat + `
func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	o := imageSrc0Origin()
	id := floor(srcPos - o)
	c := imageSrc0At(o + id + 0.5)
	lum := max(c.r, max(c.g, c.b))
	if lum < 0.01 {
		return vec4(0)
	}
	return c * (flat(min(lum, 1.0), 5.0) / lum)
}
`

var scopeRatios = [][2]float64{{3, 4}, {1, 2}, {2, 3}, {3, 5}, {1, 1}, {4, 5}, {1, 3}, {5, 6}, {2, 5}}

const (
	scopeHold  = 20.0 // how long a figure lasts
	scopeGlide = 3.0  // glide from one ratio to the next
	scopeBase  = 0.8  // base frequency, in Hz
)

var scopeErase = ebiten.Blend{
	BlendFactorSourceRGB:        ebiten.BlendFactorOne,
	BlendFactorSourceAlpha:      ebiten.BlendFactorOne,
	BlendFactorDestinationRGB:   ebiten.BlendFactorOne,
	BlendFactorDestinationAlpha: ebiten.BlendFactorOne,
	BlendOperationRGB:           ebiten.BlendOperationReverseSubtract,
	BlendOperationAlpha:         ebiten.BlendOperationReverseSubtract,
}

type scope struct {
	t, phx, phy float64
	mode        int
	size        image.Point
	scale       int
	phos        *ebiten.Image
	grat        *ebiten.Image
	gratFor     *theme.Theme
	beam        color.RGBA
	core        color.RGBA
	batch       gfx.Batch
}

func newScope(r *rand.Rand) decor {
	return &scope{mode: r.IntN(len(scopeRatios)), t: r.Float64() * scopeHold}
}

func (o *scope) fps() int { return 30 }

func (o *scope) release() {
	for _, img := range []*ebiten.Image{o.phos, o.grat} {
		if img != nil {
			img.Deallocate()
		}
	}
	o.phos, o.grat = nil, nil
}

// ratio returns the current frequency ratio, gliding from one figure to
// the next.
func (o *scope) ratio() (float64, float64) {
	cur := scopeRatios[o.mode%len(scopeRatios)]
	u := math.Mod(o.t, scopeHold)
	if u < scopeHold-scopeGlide {
		return cur[0], cur[1]
	}
	nxt := scopeRatios[(o.mode+1)%len(scopeRatios)]
	k := (u - (scopeHold - scopeGlide)) / scopeGlide
	k = k * k * (3 - 2*k)
	return cur[0] + (nxt[0]-cur[0])*k, cur[1] + (nxt[1]-cur[1])*k
}

func (o *scope) step(dt float64) {
	if o.phos == nil {
		return
	}
	dt = min(dt, 0.1)
	w, h := o.phos.Bounds().Dx(), o.phos.Bounds().Dy()

	// The phosphor fades: first proportionally, then by a small fixed
	// amount, otherwise the faintest traces would never fully disappear
	// on 8 bits.
	o.batch.Rect(0, 0, float32(w), float32(h), color.RGBA{0, 0, 0, 255}, float32(1-math.Exp(-dt/0.8)))
	o.batch.Flush(o.phos, gfx.White(), ebiten.BlendSourceOver)
	o.batch.Rect(0, 0, float32(w), float32(h), color.RGBA{255, 255, 255, 255}, 1.0/255)
	o.batch.Flush(o.phos, gfx.White(), scopeErase)

	a, b := o.ratio()
	fx := a * scopeBase * (1 + 0.0035)
	fy := b * scopeBase
	amp := float64(h) * (0.33 + 0.015*math.Sin(o.t*0.4))
	cx, cy := float64(w)/2, float64(h)/2
	n := int(min(max(dt*14000, 60), 1400))
	alpha := float32(0.1)
	for i := 0; i < n; i++ {
		f := float64(i) / float64(n) * dt
		x := cx + amp*math.Sin(o.phx+2*math.Pi*fx*f)
		y := cy + amp*math.Sin(o.phy+2*math.Pi*fy*f)
		o.batch.Rect(float32(math.Floor(x)), float32(math.Floor(y)), 1, 1, o.core, alpha)
		if i%3 == 0 {
			o.batch.Rect(float32(math.Floor(x))-1, float32(math.Floor(y))-1, 3, 3, o.beam, alpha*0.3)
		}
	}
	o.batch.Flush(o.phos, gfx.White(), ebiten.BlendLighter)
	o.phx = math.Mod(o.phx+2*math.Pi*fx*dt, 2*math.Pi)
	o.phy = math.Mod(o.phy+2*math.Pi*fy*dt, 2*math.Pi)
	before := int(o.t / scopeHold)
	o.t += dt
	if int(o.t/scopeHold) != before {
		o.mode++
	}
}

func (o *scope) init(s *stage) {
	o.release()
	o.size, o.scale = s.size, s.grain()
	w, h := (s.size.X+o.scale-1)/o.scale, (s.size.Y+o.scale-1)/o.scale
	opts := &ebiten.NewImageOptions{Unmanaged: true}
	o.phos = ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), opts)
	o.grat = ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), opts)
	o.gratFor = nil
}

// graticule draws the graticule: dotted divisions, and tick marks on the
// two central axes.
func (o *scope) graticule(th *theme.Theme) {
	o.gratFor = th
	o.beam = th.Accent
	o.core = theme.Mix(th.Accent, color.RGBA{255, 255, 255, 255}, 0.3)
	o.grat.Clear()
	w, h := o.grat.Bounds().Dx(), o.grat.Bounds().Dy()
	div := float64(h) / 8
	cx, cy := float64(w)/2, float64(h)/2
	dot := theme.Mix(th.BG, th.Dim, 0.6)
	tick := theme.Mix(th.BG, th.Dim, 0.9)
	for k := -8; k <= 8; k++ {
		x := math.Floor(cx + float64(k)*div)
		if x < 0 || x >= float64(w) {
			continue
		}
		for y := 0; y < h; y += 3 {
			o.batch.Rect(float32(x), float32(y), 1, 1, dot, 1)
		}
	}
	for k := -4; k <= 4; k++ {
		y := math.Floor(cy + float64(k)*div)
		if y < 0 || y >= float64(h) {
			continue
		}
		for x := 0; x < w; x += 3 {
			o.batch.Rect(float32(x), float32(y), 1, 1, dot, 1)
		}
	}
	for k := -40; k <= 40; k++ {
		x := math.Floor(cx + float64(k)*div/5)
		y := math.Floor(cy + float64(k)*div/5)
		if x >= 0 && x < float64(w) {
			o.batch.Rect(float32(x), float32(math.Floor(cy))-1, 1, 3, tick, 1)
		}
		if k >= -20 && k <= 20 && y >= 0 && y < float64(h) {
			o.batch.Rect(float32(math.Floor(cx))-1, float32(y), 3, 1, tick, 1)
		}
	}
	o.batch.Flush(o.grat, gfx.White(), ebiten.BlendSourceOver)
}

func (o *scope) draw(dst *ebiten.Image, s *stage) {
	if o.phos == nil || s.settled && (s.size != o.size || s.grain() != o.scale) {
		o.init(s)
		o.graticule(s.th)
		for i := 0; i < 45; i++ {
			o.step(1.0 / 30)
		}
	}
	if o.gratFor != s.th {
		o.graticule(s.th)
	}
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(o.scale), float64(o.scale))
	dst.DrawImage(o.grat, op)
	if sh := lazyShader(scopeSrc); sh != nil {
		scaleShader(dst, o.phos, float32(o.scale), sh, nil, ebiten.BlendLighter)
	}

	// The instrument's readouts, in the corners.
	p, f, th := s.p, s.f, s.th
	m := float32(f.CellH)
	c := theme.Mix(th.BG, th.Dim, 0.9)
	a, b := o.ratio()
	p.Text(f, m, m, "CH1 500mV  CH2 500mV  XY", c, 1)
	label := fmt.Sprintf("X:Y %.0f:%.0f", a, b)
	if a != math.Round(a) || b != math.Round(b) {
		label = fmt.Sprintf("X:Y %.2f:%.2f", a, b)
	}
	p.Text(f, float32(s.size.X)-m-float32(gfx.TextWidth(f, label)), m, label, c, 1)
	p.Text(f, m, float32(s.size.Y)-2*m-float32(f.CellH), fmt.Sprintf("%.2f Hz  TRIG AUTO", b*scopeBase), c, 1)
}
