package fx

import (
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Northern lights: curtains of light ripple under a starry sky, above a
// mountain ridge and a line of fir trees. The bottom of the curtains is
// sharp and takes the theme accent, the top fades toward the second accent,
// the way real auroras go from green to purple.
//
// Time comes in as phases that are already reduced (see drift and phase):
// Wave for the ripples, Flow for the noise drift (curtain bottom, height,
// rays, hue), Rise for the rays going up, Twinkle for the stars.
const auroraSrc = kageHead + kageNoise + kageFlat + `
var Wave vec2
var Flow vec4
var Rise float
var Twinkle float

// curtain returns the height of a curtain's bottom edge at x.
func curtain(x, fi float) float {
	w := sin(x*1.5+Wave.x+fi*2.4)*0.075 + sin(x*3.7-Wave.y+fi)*0.03
	w += (noise(vec2(x*1.1+Flow.x, fi*7.0)) - 0.5) * 0.14
	return 0.58 + fi*0.1 + w
}

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := dstPos.xy - imageDstOrigin()
	uv := p / Size
	aspect := Size.x / Size.y
	x := uv.x * aspect
	y := 1 - uv.y

	// The sky in four flat bands rather than a continuous gradient.
	col := mix(BG*1.5+Accent*0.05, BG*0.65, flat(smoothstep(0.1, 0.95, y), 4.0))

	// The curtains: two rippling sheets. The bottom is sharp and almost
	// white, the rays slide along the sheet and fade toward the top; where
	// the sheet folds over, it is seen edge-on and shines brighter.
	glow := vec3(0)
	for i := 0; i < 2; i++ {
		fi := float(i)
		base := curtain(x, fi)
		slope := (curtain(x+0.02, fi) - base) / 0.02
		d := y - base
		hgt := 0.10 + 0.16*noise(vec2(x*0.8-Flow.y, fi*5.0+2.0))
		fall := exp(-max(d, 0.0) / hgt * 2.4)
		under := exp(-max(-d, 0.0) * 160.0)
		rays := noise(vec2(x*46.0+fi*31.0-Flow.z, Rise+fi*3.0))
		rays = 0.2 + 0.8*rays*rays
		fold := 0.45 + clamp(abs(slope)*0.9, 0.0, 1.4)
		inten := under * fall * rays * fold * (1.0 - fi*0.35)
		c := mix(Accent, Accent2, flat(smoothstep(0.0, hgt*1.4, d), 3.0))
		c = mix(c, Accent2, 0.45*flat(smoothstep(0.55, 0.85, noise(vec2(x*0.6+Flow.w, fi*9.0))), 2.0))
		glow += c * inten
		// The bottom fringe, almost white, unbroken even between the rays.
		glow += mix(Accent, FG, 0.6) * exp(-abs(d)*220.0) * (0.35 + 0.5*rays) * fold * (1.0 - fi*0.35)
		// Diffuse halo in the atmosphere, around the sheet.
		glow += Accent * 0.05 * exp(-abs(d)*7.0) * (1.0 - fi*0.4)
	}
	// The aurora's glow, snapped to six steps.
	glow *= 1.25
	lum := max(glow.r, max(glow.g, glow.b))
	if lum > 0.002 {
		glow *= flat(min(lum, 1.0), 6.0) / lum
	}
	col += glow

	// Stars, hidden by the aurora's glow. Their frequency is a multiple of
	// 0.01: Twinkle can wrap every 200π without a jump.
	h := hash(floor(p) + 0.5)
	if h > 0.991 && y > 0.3 {
		k := floor((1.2 + h*4.0) * 100.0)
		tw := 0.55 + 0.45*sin(Twinkle*k/100.0+h*90.0)
		lum := (h - 0.991) / 0.009
		col += FG * lum * 0.6 * tw * (1.0 - clamp(length(glow)*1.5, 0.0, 1.0))
	}

	// Distant mountains: the snow on the ridges reflects a little aurora.
	ridge := 0.25 + 0.10*noise(vec2(x*1.1, 3.0)) + 0.05*noise(vec2(x*3.7, 5.0)) + 0.018*noise(vec2(x*13.0, 7.0))
	if y < ridge {
		depth := flat(clamp((ridge-y)*5.0, 0.0, 1.0), 3.0)
		col = mix(BG*0.9+glow*0.1, BG*0.62, depth)
		snow := smoothstep(ridge-0.035, ridge, y) * step(0.42, noise(vec2(x*38.0, y*70.0)))
		col += (glow*0.35 + FG*0.07) * snow * smoothstep(0.25, 0.32, ridge)
	}

	// Mist at the foot of the mountains.
	col += Accent2 * 0.035 * flat(exp(-abs(y-0.15)*18.0), 3.0)

	// A line of fir trees in the foreground: two offset, overlapping rows,
	// each tree in three tiers.
	ground := 0.09 + 0.035*noise(vec2(x*2.2, 11.0)) + 0.01*noise(vec2(x*9.0, 13.0))
	tree := 0.0
	for k := 0; k < 2; k++ {
		fk := float(k)
		tx := x*15.0 + fk*0.5
		r := hash(vec2(floor(tx), 2.0+fk))
		tf := abs(fract(tx)-0.5) * 2.0
		tall := 0.04 + r*0.07 - fk*0.012
		ty := (y - ground + 0.004) / tall
		wdt := (1.0 - ty) * (0.5 + 0.5*(1.0-fract(ty*3.2+0.1)))
		if r > 0.08 && ty > 0.0 && ty < 1.0 && tf < wdt {
			tree = 1.0
		}
		if ty > -0.2 && ty < 0.12 && tf < 0.12 {
			tree = 1.0
		}
	}
	if y < ground || tree > 0.5 {
		col = BG * 0.38
	}
	return vec4(col, 1)
}
`

type aurora struct {
	t  float64
	lr lowres
}

func newAurora(r *rand.Rand) decor { return &aurora{t: r.Float64() * 200} }

func (a *aurora) fps() int        { return 20 }
func (a *aurora) step(dt float64) { a.t += dt }
func (a *aurora) release()        { a.lr.release() }

func (a *aurora) draw(dst *ebiten.Image, s *stage) {
	type key struct {
		n  int64
		th *theme.Theme
	}
	n, t := frameStep(a.t, a.fps())
	a.lr.render(lazyShader(auroraSrc), s, s.grain(), key{n, s.th}, func() map[string]any { return a.vars(t, s.th) })
	a.lr.blit(dst, 1)
}

func (a *aurora) vars(t float64, th *theme.Theme) map[string]any {
	u := uniforms(th)
	u["Wave"] = []float32{phase(t, 0.05, 0), phase(t, 0.08, 0)}
	u["Flow"] = []float32{drift(t, 0.04), drift(t, 0.025), drift(t, 0.5), drift(t, 0.02)}
	u["Rise"] = drift(t, 0.12)
	u["Twinkle"] = float32(math.Mod(t, 200*math.Pi))
	return u
}
