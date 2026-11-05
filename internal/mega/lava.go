package mega

import (
	"math"
	"math/rand/v2"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Lava: climb as high as possible while the lava rises. q/d move, space
// jumps through the ledges from below. Some ledges crumble once stood on,
// others slide. The lava speeds up with time.

const (
	lavaFW    = 256 // width of the shaft, in game pixels
	lavaStart = 200 // world y of the start floor
)

type ledge struct {
	x, y, w float64
	vx      float64
	crumble bool
	stood   int // frames stood on (crumbling ledges)
	fall    float64
}

type lava struct {
	rng    *rand.Rand
	held   func(string) bool
	vw     float64
	frame  int
	acc    float64
	paused bool

	p       player
	ledges  []*ledge
	top     float64 // world y of the highest ledge made
	cam     float64 // world y of the top of the view
	level   float64 // world y of the lava's surface
	speed   float64
	t       int
	jumpBuf int
	minY    float64
	over    bool
	overT   int
	parts   []part
}

func newLava(seed uint64) *lava {
	g := &lava{rng: rand.New(rand.NewPCG(seed, 0x6c617661)), vw: 480, held: func(string) bool { return false }}
	g.p = player{body: body{x: lavaFW/2 - 6, y: lavaStart - 20, w: 12, h: 20}, face: 1, hp: 1}
	g.ledges = []*ledge{{x: 0, y: lavaStart, w: lavaFW}}
	g.top = lavaStart
	g.cam = lavaStart + 40 - ViewH
	g.level = lavaStart + 120
	g.speed = 0.22
	g.minY = g.p.y
	g.grow()
	return g
}

func (g *lava) SetHeld(h func(string) bool) { g.held = h }
func (g *lava) Back() bool                  { return false }

func (g *lava) Key(name string, repeat bool) {
	if repeat {
		return
	}
	if g.over {
		if g.overT > 45 && (name == "space" || name == "enter") {
			*g = *newLava(g.rng.Uint64())
		}
		return
	}
	if name == "space" || name == "z" || name == "up" || name == "k" {
		g.jumpBuf = 6
	}
}

func (g *lava) Step(dt float64, W, H int, paused bool) {
	g.vw = float64(W) / float64(Scale(H))
	g.paused = paused
	g.frame++
	if paused {
		g.acc = 0
		return
	}
	g.acc += min(dt, 0.1)
	for n := 0; g.acc >= step && n < 6; n++ {
		g.acc -= step
		g.tick()
	}
}

// grow adds ledges above until well past the top of the view.
func (g *lava) grow() {
	for g.top > g.cam-120 {
		h := float64(g.height())
		gap := 34 + g.rng.Float64()*14 + min(10, h/40)
		g.top -= gap
		w := float64(T * (3 + g.rng.IntN(3)))
		if h > 150 && g.rng.IntN(3) == 0 {
			w -= T
		}
		l := &ledge{x: g.rng.Float64() * (lavaFW - w), y: g.top, w: w}
		switch r := g.rng.Float64(); {
		case h > 20 && r < 0.25:
			l.crumble = true
		case h > 50 && r < 0.42:
			l.vx = 0.5 + g.rng.Float64()*0.5
			if g.rng.IntN(2) == 0 {
				l.vx = -l.vx
			}
		}
		g.ledges = append(g.ledges, l)
	}
}

// height is the best climb, in meters (one tile per meter).
func (g *lava) height() int { return int((lavaStart - 20 - g.minY) / T) }

func (g *lava) tick() {
	g.updateParts()
	if g.over {
		g.overT++
		return
	}
	g.t++
	p := &g.p
	h := g.held
	p.vx = 0
	if left(h) != right(h) {
		p.face = 1
		if left(h) {
			p.face = -1
		}
		p.vx = p.face * 1.7
	}
	if g.jumpBuf > 0 {
		g.jumpBuf--
		if p.ground {
			p.vy, p.ground, g.jumpBuf = -6.3, false, 0
		}
	}
	if p.vy < 0 && !(h("space") || up(h)) {
		p.vy = max(p.vy, -1.5)
	}
	p.vy = min(p.vy+grav, maxFall)
	p.x = min(max(p.x+p.vx, 0), lavaFW-p.w)
	feet := p.y + p.h
	p.y += p.vy
	p.ground = false
	for _, l := range g.ledges {
		if l.fall > 0 {
			continue
		}
		if p.vy >= 0 && feet <= l.y+0.5 && p.y+p.h >= l.y && p.x+p.w > l.x+2 && p.x < l.x+l.w-2 {
			p.y, p.vy, p.ground = l.y-p.h, 0, true
			p.x = min(max(p.x+l.vx, 0), lavaFW-p.w)
			if l.crumble {
				l.stood++
			}
		}
	}
	if p.ground && p.vx != 0 {
		p.anim++
	} else {
		p.anim = 0
	}
	g.minY = min(g.minY, p.y)
	for _, l := range g.ledges {
		if l.vx != 0 && l.fall == 0 {
			l.x += l.vx
			if l.x < 0 || l.x+l.w > lavaFW {
				l.vx = -l.vx
			}
		}
		if l.crumble && l.stood > 30 && l.fall == 0 {
			l.fall = 0.5
		}
		if l.fall > 0 {
			l.fall += 0.2
			l.y += l.fall
		}
	}
	// The camera only goes up, the lava follows it.
	g.cam = min(g.cam, p.y-110)
	g.speed = min(1.1, 0.22+float64(g.t)/60*0.006)
	g.level -= g.speed
	g.level = min(g.level, g.cam+ViewH+70)
	g.grow()
	j := 0
	for _, l := range g.ledges {
		if l.y < g.level+40 {
			g.ledges[j] = l
			j++
		}
	}
	clear(g.ledges[j:])
	g.ledges = g.ledges[:j]
	if g.rng.IntN(4) == 0 {
		l := g.rng.IntN(60)
		g.parts = append(g.parts, part{x: g.rng.Float64() * lavaFW, y: g.level, vx: g.rng.Float64() - 0.5,
			vy: -0.5 - g.rng.Float64(), life: l + 30, max: l + 30, size: 1, col: 3})
	}
	if p.y+p.h > g.level+4 {
		g.over = true
		record("lava", g.height())
		for range 30 {
			a := g.rng.Float64() * math.Pi
			g.parts = append(g.parts, part{x: p.cx(), y: g.level, vx: math.Cos(a) * 2, vy: -math.Sin(a) * 3, life: 50, max: 50, size: 2, col: 3})
		}
	}
}

func (g *lava) updateParts() {
	j := 0
	for _, p := range g.parts {
		p.x += p.vx
		p.y += p.vy
		if p.size > 1 {
			p.vy += 0.1
		}
		if p.life--; p.life > 0 {
			g.parts[j] = p
			j++
		}
	}
	g.parts = g.parts[:j]
}

func (g *lava) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	x0 := math.Floor((v.vw() - lavaFW) / 2)
	cam := math.Floor(g.cam)
	at := func(x, y, w, h float64, c rgba, a float32) { v.hud(x0+math.Floor(x), math.Floor(y)-cam, w, h, c, a) }
	o := theme.Mix(th.BG, black, 0.6)
	// The rock walls of the shaft.
	rock := theme.Mix(th.BG, black, 0.4)
	v.hud(0, -float64(v.oy/v.s), x0, float64(v.H/v.s), rock, 0.85)
	v.hud(x0+lavaFW, -float64(v.oy/v.s), v.vw()-x0-lavaFW, float64(v.H/v.s), rock, 0.85)
	v.hud(x0-2, -float64(v.oy/v.s), 2, float64(v.H/v.s), th.Warn, 0.5)
	v.hud(x0+lavaFW, -float64(v.oy/v.s), 2, float64(v.H/v.s), th.Warn, 0.5)
	v.hud(x0, -float64(v.oy/v.s), lavaFW, float64(v.H/v.s), th.BG, 0.25)
	for _, l := range g.ledges {
		dx := 0.0
		if l.crumble && l.stood > 0 && l.fall == 0 {
			dx = float64(g.frame%3 - 1)
		}
		top := theme.Mix(th.Warn, th.FG, 0.2)
		body := theme.Mix(theme.Mix(th.BG, black, 0.2), th.Warn, 0.12)
		if l.vx != 0 {
			top = th.Accent2
		}
		at(l.x+dx-1, l.y-1, l.w+2, 8, o, 1)
		at(l.x+dx, l.y, l.w, 6, body, 1)
		at(l.x+dx, l.y, l.w, 1, top, 1)
		if l.crumble {
			for k := 6.0; k < l.w-4; k += 11 {
				at(l.x+dx+k, l.y+2, 1, 3, o, 1)
				at(l.x+dx+k+1, l.y+3, 2, 1, o, 1)
			}
		}
	}
	if !g.over {
		p := &g.p
		s := heroStand
		switch {
		case !p.ground:
			s = heroJump
		case p.vx != 0:
			s = heroRun[p.anim/8%2]
		}
		save := v.cam
		v.cam = -x0
		v.oy -= float32(cam) * v.s
		v.sprite(s, p.x-1, p.y+p.h-float64(s.h), p.face, v.palette(th.Accent2, false), 1)
		v.oy += float32(cam) * v.s
		v.cam = save
	}
	// The lava: a wavy crust, then the molten mass down to the bottom.
	surf := g.level
	for x := 0.0; x < lavaFW; x += 4 {
		w := 2 * math.Sin(x*0.09+float64(g.frame)*0.08)
		at(x, surf+w, 4, 3, th.FG, 0.9)
		at(x, surf+w+3, 4, 200, th.Warn, 0.95)
	}
	for k := 0.0; k < 6; k++ {
		at(0, surf+12+k*14, lavaFW, 6, theme.Mix(th.Warn, th.Accent, 0.4), 0.5)
	}
	for _, pt := range g.parts {
		at(pt.x, pt.y, pt.size+1, pt.size+1, th.Warn, float32(pt.life)/float32(pt.max))
	}
	th2 := th
	v.text(strconv.Itoa(g.height())+" m", x0+6, 6, th2.FG, 1)
	if b := best["lava"]; b > 0 {
		v.text("best "+strconv.Itoa(b)+" m", x0+6, 18, th2.Dim, 1)
	}
	if g.over {
		v.center("MELTED", ViewH/2-30, th.Warn, 1)
		v.center(strconv.Itoa(g.height())+" m    best "+strconv.Itoa(best["lava"])+" m", ViewH/2-14, th.FG, 1)
		if g.overT > 45 {
			v.center("space: again    esc: leave", ViewH/2+2, th.Dim, 1)
		}
	}
	if g.paused {
		v.center("PAUSE", ViewH/2-30, th.FG, 1)
	}
}
