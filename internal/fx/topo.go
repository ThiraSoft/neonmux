package fx

import (
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Living topographic map: terrain that breathes slowly, drawn as contour
// lines one big pixel wide, one index contour out of five, shading in three
// flat levels and a map grid. Now and then a sweep crosses the map and
// lights up the contours as it passes.
//
// Flow carries the drift of the terrain (two warps, then the terrain
// itself), reduced to the noise period; Sweep the progress of the sweep,
// from 0 to 1. The octaves exactly double from one to the next: a shift of
// one period stays a multiple of the period at every octave.
const topoSrc = kageHead + kageNoise + kageFlat + `
var Flow vec3
var Sweep float

func fbm(p vec2) float {
	v := 0.0
	a := 0.5
	for i := 0; i < 4; i++ {
		v += a * noise(p)
		p = p*2.0 + vec2(17.1, 9.2)
		a *= 0.42
	}
	return v
}

func height(q vec2) float {
	w := vec2(noise(q*0.55+vec2(Flow.x, 3.1)), noise(q*0.55+vec2(7.7, -Flow.y)))
	return fbm(q + w*1.4 + vec2(Flow.z, 0))
}

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := dstPos.xy - imageDstOrigin()
	uv := p / Size
	aspect := Size.x / Size.y
	x := uv.x * aspect
	h := height(vec2(x, uv.y)*2.0)*1.25 - 0.08

	// Elevation tint and shading, barely noticeable.
	col := BG + mix(Accent2, Accent, h)*0.04*flat(smoothstep(0.25, 0.8, h), 3.0)
	g := vec2(dfdx(h), dfdy(h)) * Size.y
	shade := clamp(dot(normalize(vec3(-g*1.6, 1.0)), normalize(vec3(-0.6, -0.8, 0.9))), 0.0, 1.0)
	col *= 0.86 + 0.22*flat(shade, 3.0)

	// The map grid, dotted, with a cross at each intersection.
	cell := Size.y / 6.0
	gp := mod(p, cell)
	dot := step(mod(p.x+p.y, 4.0), 1.0)
	grid := max(step(gp.x, 1.0), step(gp.y, 1.0)) * dot
	cross := step(min(gp.x, cell-gp.x), 3.0) * step(min(gp.y, cell-gp.y), 3.0) * max(step(gp.x, 1.0)+step(cell-1.0, gp.x), step(gp.y, 1.0)+step(cell-1.0, gp.y))
	col = mix(col, FG, 0.05*grid+0.12*min(cross, 1.0))

	// Contour lines: the distance to the nearest contour, in pixels, comes
	// from the derivative of the elevation.
	lv := h * 13.0
	d := abs(fract(lv+0.5)-0.5) / max(fwidth(lv), 0.0001)
	major := 1.0 - step(0.5, mod(floor(lv+0.5), 5.0))
	line := step(d, 0.55)

	// The sweep: a line that crosses the map and leaves a trail behind.
	sx := Sweep*(aspect+1.2) - 0.6
	behind := sx - x
	trail := 0.0
	if behind > 0 {
		trail = floor(exp(-behind*5.0)*3.0) / 3.0
	}
	scan := exp(-abs(behind) * 160.0)

	lc := mix(Accent2, Accent, smoothstep(0.3, 0.75, h))
	lc = mix(lc, Warn, smoothstep(0.78, 0.9, h)*0.6)
	a := (0.3 + major*0.35) * (1.0 + trail*1.2)
	col = mix(col, lc, line*min(a, 1.0))
	col += Accent * scan * 0.10
	return vec4(col, 1)
}
`

type topo struct {
	t  float64
	lr lowres
}

func newTopo(r *rand.Rand) decor { return &topo{t: r.Float64() * 300} }

func (o *topo) fps() int        { return 15 }
func (o *topo) step(dt float64) { o.t += dt }
func (o *topo) release()        { o.lr.release() }

func (o *topo) draw(dst *ebiten.Image, s *stage) {
	type key struct {
		n  int64
		th *theme.Theme
	}
	n, t := frameStep(o.t, o.fps())
	o.lr.render(lazyShader(topoSrc), s, s.grain(), key{n, s.th}, func() map[string]any { return o.vars(t, s.th) })
	o.lr.blit(dst, 1)
}

// topoSweep is the period of the sweep, in seconds.
const topoSweep = 26.0

func (o *topo) vars(t float64, th *theme.Theme) map[string]any {
	u := uniforms(th)
	u["Flow"] = []float32{drift(t, 0.018), drift(t, 0.015), drift(t, 0.01)}
	u["Sweep"] = float32(math.Mod(t, topoSweep) / topoSweep)
	return u
}
