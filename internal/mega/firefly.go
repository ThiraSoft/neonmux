package mega

import (
	"math"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/sfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Dust: a firefly rises through rows of thorns. q/d steer, space flutters
// upward against its weight. Air currents push it sideways, motes give
// points. Touching a thorn or falling off the bottom ends the flight.

type thornRow struct {
	y, gap, gapW float64
	wind         float64 // sideways push in the band above the row
	mote         bool
	moteX        float64
}

type firefly struct {
	base
	x, y, vx, vy float64 // world coordinates, y grows downward
	cam          float64 // world y of the view's top
	rows         []thornRow
	top          float64
	motes        int
	minY         float64
}

func newFirefly(seed uint64) *firefly {
	g := &firefly{base: newBase(seed, 0x64757374)}
	g.x, g.y = 240, 160
	g.cam, g.top, g.minY = 0, 100, 160
	return g
}

func (g *firefly) score() int { return int((160-g.minY)/10) + 25*g.motes }

func (g *firefly) Key(name string, repeat bool) {
	if g.again(name, repeat) {
		*g = *newFirefly(g.rng.Uint64())
		return
	}
	if !repeat && !g.over && (name == "space" || name == "z" || name == "up" || name == "k") {
		g.vy = -2.6
		sfx.Play(sfx.Jump)
	}
}

func (g *firefly) Step(dt float64, W, H int, paused bool) { g.loop(dt, W, H, paused, g.tick) }

func (g *firefly) grow() {
	for g.top > g.cam-60 {
		h := (160 - g.top) / 10
		g.top -= 70 - min(20, h/20)
		w := max(34, 60-h/15)
		r := thornRow{y: g.top, gapW: w, gap: 10 + g.rng.Float64()*(g.vw-w-20)}
		if g.rng.IntN(3) == 0 {
			r.wind = (g.rng.Float64()*2 - 1) * 0.06
		}
		if g.rng.IntN(2) == 0 {
			r.mote, r.moteX = true, 10+g.rng.Float64()*(g.vw-20)
		}
		g.rows = append(g.rows, r)
	}
}

func (g *firefly) tick() {
	if g.over {
		return
	}
	g.grow()
	h := g.held
	if left(h) {
		g.vx -= 0.12
	}
	if right(h) {
		g.vx += 0.12
	}
	g.vx *= 0.94
	g.vy = min(g.vy+0.09, 2)
	for i := range g.rows {
		r := &g.rows[i]
		if g.y > r.y-70 && g.y < r.y {
			g.vx += r.wind
		}
		if r.mote && math.Abs(g.x-r.moteX) < 7 && math.Abs(g.y-(r.y-35)) < 7 {
			r.mote = false
			g.motes++
			sfx.Play(sfx.Pickup)
			g.sparks(g.x, g.y, 10, 2, 1)
		}
		// The row is a line of thorns with one gap.
		if math.Abs(g.y-r.y) < 4 && (g.x < r.gap+2 || g.x > r.gap+r.gapW-2) {
			g.die()
			return
		}
	}
	g.x = min(max(g.x+g.vx, 3), g.vw-3)
	g.y += g.vy
	g.minY = min(g.minY, g.y)
	// The view rises on its own, faster with the height.
	g.cam -= 0.25 + min(0.6, (160-g.minY)/4000)
	g.cam = min(g.cam, g.y-90)
	if g.y > g.cam+ViewH+4 {
		g.die()
	}
	j := 0
	for _, r := range g.rows {
		if r.y < g.cam+ViewH+40 {
			g.rows[j] = r
			j++
		}
	}
	g.rows = g.rows[:j]
}

func (g *firefly) die() {
	g.over = true
	record("dust", g.score())
	sfx.Play(sfx.Die)
	g.sparks(g.x, g.y, 24, 3, 1.5)
}

func (g *firefly) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	P.Rect(0, 0, v.W, v.H, th.BG, 0.3)
	cam := math.Floor(g.cam)
	o := theme.Mix(th.BG, black, 0.6)
	for _, r := range g.rows {
		y := r.y - cam
		for x := 0.0; x < g.vw; x += 6 {
			if x+6 > r.gap && x < r.gap+r.gapW {
				continue
			}
			v.hud(x, y-1, 6, 3, o, 1)
			v.hud(x+2, y-3, 2, 2, th.Accent, 1)
			v.hud(x, y, 6, 1, theme.Mix(th.Accent, th.BG, 0.3), 1)
		}
		if r.wind != 0 {
			for k := 0; k < 5; k++ {
				wx := math.Mod(float64(g.frame)*r.wind*40+float64(k)*g.vw/5+g.vw*10, g.vw)
				v.hud(wx, y-20-float64(k%3)*14, 10, 1, th.Dim, 0.5)
			}
		}
		if r.mote {
			a := float32(0.6 + 0.4*math.Sin(float64(g.frame)*0.15+r.moteX))
			v.hud(r.moteX-2, r.y-35-cam-2, 4, 4, th.Warn, a)
			v.hud(r.moteX-1, r.y-35-cam-1, 2, 2, th.FG, 1)
		}
	}
	if !g.over {
		x, y := math.Floor(g.x), math.Floor(g.y-cam)
		glow := float32(0.25 + 0.15*math.Sin(float64(g.frame)*0.2))
		v.hud(x-6, y-6, 12, 12, th.Warn, glow*0.5)
		v.hud(x-4, y-4, 8, 8, th.Warn, glow)
		wing := 1.0 + float64(g.frame/4%2)
		v.hud(x-3-wing, y-3, 2+wing, 2, th.FG, 0.7)
		v.hud(x+1, y-3, 2+wing, 2, th.FG, 0.7)
		v.hud(x-1, y-1, 3, 4, th.Warn, 1)
		v.hud(x-1, y-3, 3, 2, o, 1)
	}
	g.drawParts(v, 0, -cam)
	v.text(strconv.Itoa(g.score()), 8, 4, th.FG, 1)
	if b := best["dust"]; b > 0 {
		v.text("best "+strconv.Itoa(b), 8, 16, th.Dim, 1)
	}
	g.ending(v, "FADED", "score "+strconv.Itoa(g.score())+"   best "+strconv.Itoa(best["dust"]))
}
