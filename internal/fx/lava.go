package fx

import (
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Lava lamp: blobs of wax rise, cool down and sink back, merge and split
// above a hot pool. The blob field is cut into a few flat colors, like on
// screens with a limited palette.
//
// Bubble carries, for each blob, the already reduced phases of its rise,
// its two sways and its squash (see lava.vars).
const lavaSrc = kageHead + kageNoise + kageFlat + `
var Bubble [10]vec4

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := dstPos.xy - imageDstOrigin()
	uv := p / Size
	aspect := Size.x / Size.y
	q := vec2(uv.x*aspect, uv.y)

	// Ten blobs: each one rises and sinks at its own pace while drifting a
	// little, and swells when it is hot, at the bottom.
	f := 0.0
	for i := 0; i < 10; i++ {
		fi := float(i)
		b := Bubble[i]
		cyc := 0.5 - 0.5*cos(b.x)
		cy := 1.1 - cyc*1.2
		cx := (0.06 + 0.88*fract(fi*0.618+0.05)) * aspect
		cx += 0.06*sin(b.y) + 0.025*sin(b.z)
		r := (0.045 + 0.035*fract(fi*0.77+0.1)) * (0.8 + 0.35*(1.0-cyc))
		d := q - vec2(cx, cy)
		d.y *= 0.8 + 0.3*sin(b.w)
		f += r * r / (dot(d, d) + 0.0004)
	}
	// The hot pool at the bottom, and a thin film at the top.
	f += 0.026 / max(1.02-uv.y, 0.01)
	f += 0.004 / max(uv.y+0.02, 0.01)

	heat := smoothstep(0.2, 1.0, uv.y)

	// The liquid, tinted by the lamp heating it from below.
	col := mix(BG, mix(BG, Accent2, 0.13), uv.y*uv.y)

	// Two rings of halo around the wax.
	if f > 0.6 {
		col = mix(col, Accent2, 0.07)
	}
	if f > 0.8 {
		col = mix(col, Accent2, 0.09)
	}

	// The wax: four shades from edge to core.
	if f > 1.0 {
		k := floor(smoothstep(1.0, 3.4, f)*3.0 + 0.5)
		hot := mix(Accent, Warn, 0.25+heat*0.35)
		col = mix(BG, Accent, 0.36)
		if k >= 1.0 {
			col = mix(BG, Accent, 0.52)
		}
		if k >= 2.0 {
			col = mix(BG, hot, 0.66)
		}
		if k >= 3.0 {
			col = mix(BG, hot, 0.8)
		}
	}

	// Reflection on the glass wall: a light band on the left.
	col += FG * exp(-abs(uv.x-0.07)*45.0) * 0.03
	return vec4(col, 1)
}
`

type lava struct {
	t  float64
	lr lowres
}

func newLava(r *rand.Rand) decor { return &lava{t: r.Float64() * 500} }

func (l *lava) fps() int        { return 20 }
func (l *lava) step(dt float64) { l.t += dt }
func (l *lava) release()        { l.lr.release() }

func (l *lava) draw(dst *ebiten.Image, s *stage) {
	type key struct {
		n  int64
		th *theme.Theme
	}
	n, t := frameStep(l.t, l.fps())
	l.lr.render(lazyShader(lavaSrc), s, s.grain(), key{n, s.th}, func() map[string]any { return l.vars(t, s.th) })
	l.lr.blit(dst, 1)
}

func (l *lava) vars(t float64, th *theme.Theme) map[string]any {
	frac := func(v float64) float64 { return v - math.Floor(v) }
	b := make([]float32, 0, 40)
	for i := range 10 {
		fi := float64(i)
		sp := 0.04 + 0.035*frac(fi*0.2718+0.6)
		ph := frac(fi*0.4142+0.31) * 6.283
		b = append(b, phase(t, sp, ph), phase(t, 0.11, ph), phase(t, 0.29, fi), phase(t, 0.35, fi))
	}
	u := uniforms(th)
	u["Bubble"] = b
	return u
}
