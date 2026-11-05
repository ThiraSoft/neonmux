package mega

import (
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Rain: the glyphs of the digital rain fall in columns. A cannon at the
// bottom (q/d, j or space) breaks them before they land. Bright glyphs
// take two hits. Five glyphs landed and the game is over.

const glColW = 12.0

type glyph struct {
	x, y, vy float64
	r        rune
	hp       int
	flash    int
}

type glyphs struct {
	base
	x     float64
	cool  int
	lives int
	score int
	t     int
	next  int
	falls []glyph
	shots []body
}

func newGlyphs(seed uint64) *glyphs {
	g := &glyphs{base: newBase(seed, 0x7261696e)}
	g.x, g.lives, g.next = 240, 5, 30
	return g
}

func (g *glyphs) Key(name string, repeat bool) {
	if g.again(name, repeat) {
		*g = *newGlyphs(g.rng.Uint64())
	}
}

func (g *glyphs) Step(dt float64, W, H int, paused bool) { g.loop(dt, W, H, paused, g.tick) }

func (g *glyphs) tick() {
	if g.over {
		return
	}
	g.t++
	h := g.held
	if left(h) {
		g.x -= 2.6
	}
	if right(h) {
		g.x += 2.6
	}
	g.x = min(max(g.x, 8), g.vw-8)
	if g.cool > 0 {
		g.cool--
	}
	if (h("j") || h("space")) && g.cool == 0 {
		g.cool = 9
		g.shots = append(g.shots, body{x: g.x - 1, y: ViewH - 22, w: 2, h: 6, vy: -5})
	}
	if g.next--; g.next <= 0 {
		lvl := float64(g.t) / 3600
		g.next = max(10, 45-int(lvl*20))
		cols := int(g.vw / glColW)
		gl := glyph{x: float64(g.rng.IntN(cols))*glColW + 2, y: -12, vy: 0.35 + lvl*0.25 + g.rng.Float64()*0.3, hp: 1}
		gl.r = 0xff66 + rune(g.rng.IntN(56))
		if g.rng.IntN(5) == 0 {
			gl.hp = 2
		}
		g.falls = append(g.falls, gl)
	}
	j := 0
	for _, s := range g.shots {
		s.y += s.vy
		hit := false
		for i := range g.falls {
			f := &g.falls[i]
			if f.hp > 0 && s.x >= f.x-1 && s.x <= f.x+glColW-2 && s.y <= f.y+10 && s.y+s.h >= f.y {
				hit = true
				f.flash = 5
				if f.hp--; f.hp == 0 {
					g.score += 10 * (1 + int(f.vy*3))
					g.sparks(f.x+5, f.y+5, 8, 2, 1.2)
				}
				break
			}
		}
		if !hit && s.y > -10 {
			g.shots[j] = s
			j++
		}
	}
	g.shots = g.shots[:j]
	j = 0
	for _, f := range g.falls {
		if f.flash > 0 {
			f.flash--
		}
		f.y += f.vy
		if f.hp > 0 && f.y > ViewH-18 {
			g.lives--
			g.sparks(f.x+5, ViewH-14, 12, 3, 1.5)
			if g.lives == 0 {
				g.over = true
				record("rain", g.score)
			}
			continue
		}
		if f.hp > 0 {
			g.falls[j] = f
			j++
		}
	}
	g.falls = g.falls[:j]
}

func (g *glyphs) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	P.Rect(0, 0, v.W, v.H, th.BG, 0.45)
	sc := v.textScale()
	o := theme.Mix(th.BG, black, 0.6)
	for _, gl := range g.falls {
		c := th.Accent2
		if gl.hp > 1 {
			c = th.FG
		}
		if gl.flash > 0 {
			c = th.Warn
		}
		v.hud(gl.x-1, gl.y-1, glColW, 12, o, 0.7)
		// A short trail above the glyph.
		v.hud(gl.x+4, gl.y-14, 2, 12, c, 0.25)
		v.P.GlyphScaled(v.f.Glyph(gl.r, 0, false), float32(gl.x)*v.s, v.oy+float32(gl.y)*v.s, sc, c, 1)
	}
	for _, s := range g.shots {
		v.hud(s.x, s.y, s.w, s.h, th.FG, 1)
		v.hud(s.x-1, s.y+2, s.w+2, s.h, th.Accent2, 0.4)
	}
	// Ground line and the cannon.
	v.hud(0, ViewH-16, v.vw(), 1, th.Accent2, 0.6)
	if !g.over {
		x := g.x
		v.hud(x-8, ViewH-12, 16, 6, o, 1)
		v.hud(x-7, ViewH-11, 14, 4, th.Accent2, 1)
		v.hud(x-2, ViewH-17, 4, 6, o, 1)
		v.hud(x-1, ViewH-16, 2, 5, th.FG, 1)
	}
	g.drawParts(v, 0, 0)
	v.text(strconv.Itoa(g.score), 8, 4, th.FG, 1)
	for i := range g.lives {
		v.hud(v.vw()-12-float64(i)*8, 6, 5, 5, th.Accent2, 1)
	}
	g.ending(v, "OVERRUN", "score "+strconv.Itoa(g.score)+"   best "+strconv.Itoa(best["rain"]))
}
