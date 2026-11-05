package mega

import (
	"math"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Grid: a lane runner on the synthwave grid, in false 3D. q/d change lane,
// space jumps the low bars; the tall blocks must be dodged. Orbs give
// points. The speed rises with the distance.

const (
	rnLanes   = 5
	rnHorizon = 96.0 // view y of the horizon
	rnFloor   = ViewH - 16.0
)

type rnThing struct {
	lane int
	d    float64 // depth: 1 at the horizon, 0 at the hero
	kind int     // 0 low bar, 1 tall block, 2 orb
}

type runner struct {
	base
	lane   float64 // drawn lane, slides toward target
	target int
	jump   int // frames left in the air
	speed  float64
	dist   float64
	orbs   int
	things []rnThing
	next   float64
}

func newRunner(seed uint64) *runner {
	g := &runner{base: newBase(seed, 0x67726964)}
	g.target, g.lane, g.speed = 2, 2, 0.008
	return g
}

func (g *runner) score() int { return int(g.dist) + 50*g.orbs }

func (g *runner) Key(name string, repeat bool) {
	if g.again(name, repeat) {
		*g = *newRunner(g.rng.Uint64())
		return
	}
	if repeat || g.over {
		return
	}
	switch name {
	case "q", "left", "h":
		g.target = max(0, g.target-1)
	case "d", "right", "l":
		g.target = min(rnLanes-1, g.target+1)
	case "space", "z", "up", "k":
		if g.jump == 0 {
			g.jump = 42
		}
	}
}

func (g *runner) Step(dt float64, W, H int, paused bool) { g.loop(dt, W, H, paused, g.tick) }

func (g *runner) tick() {
	if g.over {
		return
	}
	g.lane += (float64(g.target) - g.lane) * 0.25
	if g.jump > 0 {
		g.jump--
	}
	g.speed = min(0.03, 0.008+g.dist/60000)
	g.dist += g.speed * 100
	g.next -= g.speed
	if g.next <= 0 {
		g.next = 0.18 + g.rng.Float64()*0.2
		free := g.rng.IntN(rnLanes)
		n := 1 + g.rng.IntN(min(4, 1+int(g.dist/800)))
		for k := range n {
			l := (free + 1 + k) % rnLanes
			g.things = append(g.things, rnThing{lane: l, d: 1, kind: g.rng.IntN(2)})
		}
		if g.rng.IntN(2) == 0 {
			g.things = append(g.things, rnThing{lane: free, d: 1, kind: 2})
		}
	}
	j := 0
	for _, t := range g.things {
		t.d -= g.speed
		if t.d < 0.03 && t.d > -0.02 && t.lane == int(math.Round(g.lane)) {
			switch {
			case t.kind == 2:
				g.orbs++
				g.sparks(0, 0, 10, 2, 1.5)
				continue
			case t.kind == 1 || g.jump < 8 || g.jump > 34:
				g.over = true
				record("grid", g.score())
				g.sparks(0, 0, 30, 3, 2)
			}
		}
		if t.d > -0.05 {
			g.things[j] = t
			j++
		}
	}
	g.things = g.things[:j]
}

// project returns the view position and scale of depth d in a lane.
func (g *runner) project(lane, d float64) (x, y, s float64) {
	f := (1/(1+6*d) - 1.0/7) / (1 - 1.0/7)
	y = rnHorizon + (rnFloor-rnHorizon)*f
	laneW := 16 + 44*f
	return g.vw/2 + (lane-float64(rnLanes-1)/2)*laneW, y, 0.25 + 0.75*f
}

func (g *runner) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	P.Rect(0, 0, v.W, v.H, th.BG, 0.2)
	// The lane edges, from the horizon to the hero.
	for l := 0; l <= rnLanes; l++ {
		for k := 0; k < 30; k++ {
			d := float64(k) / 30
			x, y, s := g.project(float64(l)-0.5, d)
			v.hud(math.Floor(x), math.Floor(y), max(1, math.Round(s*2)), 2, th.Accent, float32(0.3+0.5*(1-d)))
		}
	}
	// Farthest first.
	order := append([]rnThing(nil), g.things...)
	for i := 1; i < len(order); i++ {
		for k := i; k > 0 && order[k].d > order[k-1].d; k-- {
			order[k], order[k-1] = order[k-1], order[k]
		}
	}
	o := theme.Mix(th.BG, black, 0.6)
	for _, t := range order {
		x, y, s := g.project(float64(t.lane), max(0, t.d))
		w := 36 * s
		switch t.kind {
		case 0:
			h := 8 * s
			v.hud(x-w/2-1, y-h-1, w+2, h+2, o, 1)
			v.hud(x-w/2, y-h, w, h, th.Warn, 1)
			v.hud(x-w/2, y-h, w, 1, th.FG, 1)
		case 1:
			h := 34 * s
			v.hud(x-w/2-1, y-h-1, w+2, h+2, o, 1)
			v.hud(x-w/2, y-h, w, h, theme.Mix(th.Accent, th.BG, 0.3), 1)
			v.hud(x-w/2, y-h, w, 2, th.Accent, 1)
			v.hud(x-w/2+2, y-h+4, w-4, 1, th.Accent2, 0.6)
		case 2:
			r := 10 * s
			v.hud(x-r/2, y-r*1.4, r, r, th.Accent2, 1)
			v.hud(x-r/4, y-r*1.2, r/2, r/2, th.FG, 1)
		}
	}
	x, y, _ := g.project(g.lane, 0)
	lift := 0.0
	if g.jump > 0 {
		t := float64(42-g.jump) / 42
		lift = 34 * 4 * t * (1 - t)
	}
	if !g.over {
		v.hud(x-8, y-3, 16, 3, o, 0.5) // shadow
		v.sprite(shipSpr, x-8, y-14-lift, 1, v.palette(th.Accent2, false), 1)
	}
	g.drawParts(v, x, y-8)
	v.text(strconv.Itoa(g.score()), 8, 4, th.FG, 1)
	if b := best["grid"]; b > 0 {
		v.text("best "+strconv.Itoa(b), 8, 16, th.Dim, 1)
	}
	g.ending(v, "CRASH", "score "+strconv.Itoa(g.score())+"   best "+strconv.Itoa(best["grid"]))
}
