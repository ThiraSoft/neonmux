package mega

import (
	"image/color"
	"math/rand/v2"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/sfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

type rgba = color.RGBA

// Circuit: a pulse runs on the board's traces like a snake. zqsd, hjkl or
// the arrows turn it; each component eaten makes it longer and faster.
// Chips and the edge of the board burn it, and so does its own trace.

const (
	cellPx = 8
	snRows = 27
	snTop  = 20 // room for the score
)

type pt struct{ x, y int }

type chip struct{ x, y, w, h int }

type snake struct {
	rng    *rand.Rand
	vw     float64
	cols   int
	frame  int
	acc    float64
	paused bool

	body   []pt // head first
	dir    pt
	queue  []pt
	grow   int
	food   pt
	kind   int
	chips  []chip
	t      int
	every  int
	eaten  int
	score  int
	over   bool
	overT  int
	parts  []part
	inited bool
}

func newSnake(seed uint64) *snake {
	return &snake{rng: rand.New(rand.NewPCG(seed, 0x736e616b)), vw: 480}
}

func (g *snake) SetHeld(func(string) bool) {}
func (g *snake) Back() bool                { return false }

func (g *snake) reset() {
	g.cols = max(20, int(g.vw)/cellPx-2)
	g.body = []pt{{6, snRows / 2}, {5, snRows / 2}, {4, snRows / 2}}
	g.dir, g.queue, g.grow = pt{1, 0}, g.queue[:0], 2
	g.every, g.eaten, g.score, g.t = 7, 0, 0, 0
	g.over, g.overT = false, 0
	g.chips = g.chips[:0]
	for range 6 + g.cols/12 {
		for try := 0; try < 20; try++ {
			c := chip{x: 1 + g.rng.IntN(g.cols-6), y: 1 + g.rng.IntN(snRows-5), w: 3 + g.rng.IntN(3), h: 2 + g.rng.IntN(2)}
			if c.x < 12 && c.y <= snRows/2+2 && c.y+c.h >= snRows/2-2 {
				continue // keep the start free
			}
			g.chips = append(g.chips, c)
			break
		}
	}
	g.place()
	g.inited = true
}

func (g *snake) Key(name string, repeat bool) {
	if g.over {
		if !repeat && g.overT > 45 && (name == "space" || name == "enter") {
			g.reset()
		}
		return
	}
	var d pt
	switch name {
	case "z", "up", "k":
		d = pt{0, -1}
	case "s", "down", "j":
		d = pt{0, 1}
	case "q", "left", "h":
		d = pt{-1, 0}
	case "d", "right", "l":
		d = pt{1, 0}
	default:
		return
	}
	last := g.dir
	if n := len(g.queue); n > 0 {
		last = g.queue[n-1]
	}
	if d != last && d != (pt{-last.x, -last.y}) && len(g.queue) < 2 {
		g.queue = append(g.queue, d)
	}
}

func (g *snake) Step(dt float64, W, H int, paused bool) {
	g.vw = float64(W) / float64(Scale(H))
	if !g.inited {
		g.reset()
	}
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

func (g *snake) blocked(p pt) bool {
	if p.x < 0 || p.y < 0 || p.x >= g.cols || p.y >= snRows {
		return true
	}
	for _, c := range g.chips {
		if p.x >= c.x && p.x < c.x+c.w && p.y >= c.y && p.y < c.y+c.h {
			return true
		}
	}
	return false
}

func (g *snake) onBody(p pt) bool {
	for _, b := range g.body {
		if b == p {
			return true
		}
	}
	return false
}

// place puts the next component on a free cell.
func (g *snake) place() {
	for {
		p := pt{g.rng.IntN(g.cols), g.rng.IntN(snRows)}
		if !g.blocked(p) && !g.onBody(p) {
			g.food, g.kind = p, g.rng.IntN(3)
			return
		}
	}
}

func (g *snake) tick() {
	g.updateParts()
	if g.over {
		g.overT++
		return
	}
	g.t++
	if g.t%g.every != 0 {
		return
	}
	if len(g.queue) > 0 {
		g.dir, g.queue = g.queue[0], g.queue[1:]
	}
	h := g.body[0]
	n := pt{h.x + g.dir.x, h.y + g.dir.y}
	tail := g.body[len(g.body)-1]
	if g.blocked(n) || g.onBody(n) && !(n == tail && g.grow == 0) {
		g.over = true
		record("circuit", g.score)
		sfx.Play(sfx.Die)
		g.sparks(h, 30)
		return
	}
	g.body = append([]pt{n}, g.body...)
	if g.grow > 0 {
		g.grow--
	} else {
		g.body = g.body[:len(g.body)-1]
	}
	if n == g.food {
		g.grow += 3
		g.eaten++
		sfx.Play(sfx.Pickup)
		g.score += 10 * (8 - g.every)
		g.sparks(n, 12)
		if g.eaten%5 == 0 && g.every > 3 {
			g.every--
		}
		g.place()
	}
}

func (g *snake) sparks(p pt, n int) {
	x, y := float64(p.x*cellPx+4), float64(p.y*cellPx+4)
	for range n {
		vx, vy := g.rng.Float64()*4-2, g.rng.Float64()*4-2
		l := 12 + g.rng.IntN(16)
		g.parts = append(g.parts, part{x: x, y: y, vx: vx, vy: vy, life: l, max: l, size: 1, col: 2})
	}
}

func (g *snake) updateParts() {
	j := 0
	for _, p := range g.parts {
		p.x += p.vx
		p.y += p.vy
		if p.life--; p.life > 0 {
			g.parts[j] = p
			j++
		}
	}
	g.parts = g.parts[:j]
}

func (g *snake) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	if !g.inited {
		return
	}
	bw := float64(g.cols * cellPx)
	x0 := float64(int((v.vw() - bw) / 2))
	cell := func(p pt, in float64, c rgba, a float32) {
		v.hud(x0+float64(p.x*cellPx)+in, snTop+float64(p.y*cellPx)+in, cellPx-2*in, cellPx-2*in, c, a)
	}
	o := theme.Mix(th.BG, black, 0.6)
	v.hud(x0-2, snTop-2, bw+4, snRows*cellPx+4, th.Accent2, 0.6)
	v.hud(x0, snTop, bw, snRows*cellPx, theme.Mix(th.BG, black, 0.3), 0.7)
	for _, c := range g.chips {
		x, y := x0+float64(c.x*cellPx), snTop+float64(c.y*cellPx)
		w, h := float64(c.w*cellPx), float64(c.h*cellPx)
		for k := 2.0; k < w-2; k += 4 {
			v.hud(x+k, y, 2, h, th.Dim, 1) // pins
		}
		v.hud(x+1, y+2, w-2, h-4, o, 1)
		v.hud(x+2, y+3, w-4, h-6, theme.Mix(th.BG, th.FG, 0.12), 1)
		v.hud(x+3, y+4, 2, 2, th.Dim, 1)
	}
	// The component to eat.
	fc := [3]rgba{th.Warn, th.Accent, th.FG}[g.kind]
	if g.frame/8%2 == 0 {
		cell(g.food, -1, fc, 0.25)
	}
	cell(g.food, 1, o, 1)
	cell(g.food, 2, fc, 1)
	// The trace, with a bright pulse running from the head to the tail.
	run := g.frame / 2 % (len(g.body) + 8)
	for i := len(g.body) - 1; i >= 0; i-- {
		b := g.body[i]
		c := th.Accent2
		if i == run {
			c = th.FG
		}
		cell(b, 1, theme.Mix(th.Accent2, black, 0.4), 1)
		cell(b, 2, c, 1)
		if i > 0 {
			// Join the two cells, so that the trace looks continuous.
			pv := g.body[i-1]
			x := x0 + float64(min(b.x, pv.x)*cellPx) + 2
			y := snTop + float64(min(b.y, pv.y)*cellPx) + 2
			w, h := float64(cellPx-4), float64(cellPx-4)
			if b.x != pv.x {
				w += cellPx
			} else {
				h += cellPx
			}
			v.hud(x, y, w, h, c, 1)
		}
	}
	if len(g.body) > 0 && !g.over {
		cell(g.body[0], 2, th.FG, 1)
	}
	for _, p := range g.parts {
		v.hud(x0+p.x, snTop+p.y, 2, 2, v.partColor(p.col), float32(p.life)/float32(p.max))
	}
	v.text(strconv.Itoa(g.score), x0, 4, th.FG, 1)
	if b := best["circuit"]; b > 0 {
		v.text("best "+strconv.Itoa(b), x0+60, 4, th.Dim, 1)
	}
	if g.over {
		v.center("SHORT CIRCUIT", ViewH/2-30, th.Warn, 1)
		v.center("score "+strconv.Itoa(g.score), ViewH/2-14, th.FG, 1)
		if g.overT > 45 {
			v.center("space: again    esc: leave", ViewH/2+2, th.Dim, 1)
		}
	}
	if g.paused {
		v.center("PAUSE", ViewH/2-30, th.FG, 1)
	}
}
