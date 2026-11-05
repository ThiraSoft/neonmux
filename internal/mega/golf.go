package mega

import (
	"math"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Topo: mini golf on a contour map. The hills and hollows of the map bend
// the ball. q/d aim, hold space to charge, release to putt. Nine holes,
// par 3 each.

const (
	glHoles = 9
	glW     = 400.0
	glH     = 200.0
	glTop   = 24.0
)

type hill struct{ x, y, r, h float64 }

type golf struct {
	base
	hole       int
	hills      []hill
	bx, by     float64
	vx, vy     float64
	hx, hy     float64
	aim, power float64
	charging   bool
	strokes    int
	total      int
	sunk       int // frames since the ball dropped
	contour    [][2]int
}

func newGolf(seed uint64) *golf {
	g := &golf{base: newBase(seed, 0x746f706f)}
	g.newHole()
	return g
}

func (g *golf) newHole() {
	g.hole++
	g.hills = g.hills[:0]
	for range 3 + g.hole/2 {
		h := hill{x: g.rng.Float64() * glW, y: g.rng.Float64() * glH, r: 30 + g.rng.Float64()*50, h: (g.rng.Float64()*2 - 1) * 1.2}
		g.hills = append(g.hills, h)
	}
	g.bx, g.by = 30, glH/2+(g.rng.Float64()*2-1)*60
	g.hx, g.hy = glW-40, glH/2+(g.rng.Float64()*2-1)*70
	g.vx, g.vy, g.strokes, g.sunk, g.aim = 0, 0, 0, 0, math.Atan2(g.hy-g.by, g.hx-g.bx)
	// The contour lines, computed once per hole on a 4 px grid.
	g.contour = g.contour[:0]
	band := func(x, y float64) int { return int(math.Floor(g.height(x, y) * 6)) }
	for y := 0.0; y < glH; y += 4 {
		for x := 0.0; x < glW; x += 4 {
			b := band(x, y)
			if b != band(x+4, y) || b != band(x, y+4) {
				g.contour = append(g.contour, [2]int{int(x), int(y)})
			}
		}
	}
}

func (g *golf) height(x, y float64) float64 {
	s := 0.0
	for _, h := range g.hills {
		d := ((x-h.x)*(x-h.x) + (y-h.y)*(y-h.y)) / (h.r * h.r)
		s += h.h * math.Exp(-d)
	}
	return s
}

func (g *golf) moving() bool { return math.Hypot(g.vx, g.vy) > 0.02 }

func (g *golf) Key(name string, repeat bool) {
	if g.again(name, repeat) {
		*g = *newGolf(g.rng.Uint64())
		return
	}
	if !repeat && name == "space" && !g.over && !g.moving() && g.sunk == 0 {
		g.charging, g.power = true, 0
	}
}

func (g *golf) Step(dt float64, W, H int, paused bool) { g.loop(dt, W, H, paused, g.tick) }

func (g *golf) tick() {
	if g.over {
		return
	}
	if g.sunk > 0 {
		if g.sunk++; g.sunk > 70 {
			g.total += g.strokes
			if g.hole == glHoles {
				g.over = true
				if b, ok := bestTime["topo"]; !ok || float64(g.total) < b {
					bestTime["topo"] = float64(g.total)
				}
				return
			}
			g.newHole()
		}
		return
	}
	h := g.held
	if !g.moving() {
		if left(h) {
			g.aim -= 0.04
		}
		if right(h) {
			g.aim += 0.04
		}
	}
	if g.charging {
		if h("space") {
			g.power = min(1, g.power+0.012)
		} else {
			g.charging = false
			g.strokes++
			sp := 0.6 + 4.4*g.power
			g.vx, g.vy = sp*math.Cos(g.aim), sp*math.Sin(g.aim)
		}
	}
	if !g.moving() {
		g.vx, g.vy = 0, 0
		return
	}
	// The slope pushes the ball downhill.
	const e = 2.0
	gx := (g.height(g.bx+e, g.by) - g.height(g.bx-e, g.by)) / (2 * e)
	gy := (g.height(g.bx, g.by+e) - g.height(g.bx, g.by-e)) / (2 * e)
	g.vx -= gx * 0.9
	g.vy -= gy * 0.9
	g.vx *= 0.985
	g.vy *= 0.985
	g.bx += g.vx
	g.by += g.vy
	if g.bx < 2 || g.bx > glW-2 {
		g.vx = -g.vx * 0.7
		g.bx = min(max(g.bx, 2), glW-2)
	}
	if g.by < 2 || g.by > glH-2 {
		g.vy = -g.vy * 0.7
		g.by = min(max(g.by, 2), glH-2)
	}
	if math.Hypot(g.bx-g.hx, g.by-g.hy) < 4 && math.Hypot(g.vx, g.vy) < 2.4 {
		g.sunk = 1
		g.bx, g.by = g.hx, g.hy
		g.sparks(g.hx, g.hy, 16, 2, 1.2)
	}
}

func (g *golf) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	x0 := math.Floor((v.vw() - glW) / 2)
	o := theme.Mix(th.BG, black, 0.6)
	v.hud(x0-2, glTop-2, glW+4, glH+4, th.Accent2, 0.5)
	v.hud(x0, glTop, glW, glH, theme.Mix(th.BG, black, 0.25), 0.8)
	for _, c := range g.contour {
		hgt := g.height(float64(c[0]), float64(c[1]))
		col := th.Accent2
		if hgt < 0 {
			col = th.Accent
		}
		v.hud(x0+float64(c[0]), glTop+float64(c[1]), 2, 2, col, 0.55)
	}
	// The hole and its flag.
	hx, hy := x0+g.hx, glTop+g.hy
	v.hud(hx-3, hy-2, 6, 4, o, 1)
	v.hud(hx-2, hy-3, 4, 6, o, 1)
	v.hud(hx, hy-16, 1, 14, th.FG, 1)
	v.hud(hx+1, hy-16, 7, 4, th.Warn, 1)
	if g.sunk == 0 {
		bx, by := x0+math.Floor(g.bx), glTop+math.Floor(g.by)
		if !g.moving() && !g.over {
			n := 6 + int(g.power*10)
			for k := 1; k <= n; k++ {
				d := float64(k) * 4
				v.hud(bx+d*math.Cos(g.aim), by+d*math.Sin(g.aim), 1, 1, th.FG, float32(1-float64(k)/float64(n+2)))
			}
		}
		v.hud(bx-2, by-1, 4, 3, o, 1)
		v.hud(bx-1, by-2, 3, 4, th.FG, 1)
	}
	if g.charging {
		v.hud(x0, glTop+glH+6, glW, 4, o, 1)
		v.hud(x0, glTop+glH+6, glW*g.power, 4, th.Warn, 1)
	}
	g.drawParts(v, x0, glTop)
	v.text("hole "+strconv.Itoa(g.hole)+"/"+strconv.Itoa(glHoles)+"   strokes "+strconv.Itoa(g.strokes)+"   total "+strconv.Itoa(g.total), x0, 6, th.FG, 1)
	par := 3 * glHoles
	line := strconv.Itoa(g.total) + " strokes, par " + strconv.Itoa(par)
	if b, ok := bestTime["topo"]; ok {
		line += "   best " + strconv.Itoa(int(b))
	}
	g.ending(v, "COURSE DONE", line)
}
