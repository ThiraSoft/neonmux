package fx

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Printed circuit board: a randomly generated board, with its chips, its
// buses of parallel traces that turn together at 45°, its vias. Every now
// and then a flash runs along a trace from end to end, lighting it up
// entirely, jumps from via to via onto the neighboring traces, and turns on
// the LED of the chip where it ends. The board is drawn once in big pixels;
// only the pulses change from one frame to the next.

var circuitDirs = [8][2]int{{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}}

type trace struct {
	pts  [][2]int // nodes of the trace, in board pixels
	chip [2]int   // chip at the start and at the end, -1 for a via
	glow [2]float32
}

type pcbChip struct {
	x0, y0, x1, y1 int // in board pixels
	led            float32
}

type pulse struct {
	tr   int
	s, v float64 // position and speed, in board pixels along the trace
	rev  bool
	tail int // length of the trail, in board pixels
	hops int // via-to-via jumps it has left
}

// link points to the end of another trace (tr is -1 if there is none).
type link struct{ tr, end int }

const circuitFlashes = 5 // simultaneous flashes, at most

type circuit struct {
	rng      *rand.Rand
	size     image.Point
	grain    int
	lw, lh   int
	pitch    int
	nx, ny   int
	occ      []bool
	diag     []bool
	traces   []trace
	lens     []int
	links    [][2]link // for each trace end, the neighboring end to jump to
	chips    []pcbChip
	pulses   []pulse
	board    *ebiten.Image
	boardFor *theme.Theme
	spawn    float64
}

func newCircuit(r *rand.Rand) decor { return &circuit{rng: r} }

// fps goes up while a flash runs along its trace so the stroke stays
// smooth, then drops back to 20 while its trail fades, and to 15 when only
// pads that are dimming remain. Flashes are spaced out enough for the
// board to stay at 15 most of the time: the whole app follows the
// background's rate.
func (c *circuit) fps() int {
	f := 15
	for _, q := range c.pulses {
		if q.s < float64(c.lens[q.tr]) {
			return 30
		}
		f = 20
	}
	return f
}

func (c *circuit) release() {
	if c.board != nil {
		c.board.Deallocate()
		c.board = nil
	}
}

func (c *circuit) node(i, j int) [2]int {
	o := c.pitch / 2
	return [2]int{i*c.pitch + o, j*c.pitch + o}
}

func (c *circuit) free(i, j int) bool {
	return i >= 0 && j >= 0 && i < c.nx && j < c.ny && !c.occ[j*c.nx+i]
}

// generate draws a new random board.
func (c *circuit) generate(s *stage) {
	c.size, c.grain = s.size, s.grain()
	c.lw = (s.size.X + c.grain - 1) / c.grain
	c.lh = (s.size.Y + c.grain - 1) / c.grain
	c.pitch = max(4, int(math.Round(float64(c.lh)/62)))
	c.nx, c.ny = c.lw/c.pitch+1, c.lh/c.pitch+1
	c.occ = make([]bool, c.nx*c.ny)
	c.diag = make([]bool, c.nx*c.ny)
	c.traces, c.chips, c.pulses = c.traces[:0], c.chips[:0], c.pulses[:0]

	// The chips, then the buses that leave from their pins.
	for tries := 0; tries < 60 && len(c.chips) < 3+c.nx*c.ny/1400; tries++ {
		w, h := 6+c.rng.IntN(9), 3+c.rng.IntN(3)
		if c.rng.IntN(3) == 0 {
			w, h = h, w
		}
		i0, j0 := 2+c.rng.IntN(max(1, c.nx-w-4)), 2+c.rng.IntN(max(1, c.ny-h-4))
		ok := true
		for j := j0 - 3; j <= j0+h+3 && ok; j++ {
			for i := i0 - 3; i <= i0+w+3; i++ {
				if !c.free(i, j) {
					ok = false
					break
				}
			}
		}
		if !ok {
			continue
		}
		for j := j0; j <= j0+h; j++ {
			for i := i0; i <= i0+w; i++ {
				c.occ[j*c.nx+i] = true
			}
		}
		k := len(c.chips)
		a, b := c.node(i0, j0), c.node(i0+w, j0+h)
		c.chips = append(c.chips, pcbChip{x0: a[0], y0: a[1], x1: b[0], y1: b[1]})
		horiz := w >= h
		for side := 0; side < 2; side++ {
			plan := c.busPlan()
			if horiz {
				d, j := 6, j0-1
				if side == 1 {
					d, j = 2, j0+h+1
				}
				for i := i0 + 1; i < i0+w; i++ {
					if c.rng.IntN(5) > 0 {
						c.walk(i, j, d, plan, k)
					}
				}
			} else {
				d, i := 4, i0-1
				if side == 1 {
					d, i = 0, i0+w+1
				}
				for j := j0 + 1; j < j0+h; j++ {
					if c.rng.IntN(5) > 0 {
						c.walk(i, j, d, plan, k)
					}
				}
			}
		}
	}
	// Free traces, from via to via, to fill the board.
	for tries := 0; tries < c.nx*c.ny/6; tries++ {
		i, j := c.rng.IntN(c.nx), c.rng.IntN(c.ny)
		if !c.free(i, j) {
			continue
		}
		d := c.rng.IntN(4) * 2
		if c.rng.IntN(4) == 0 {
			d++
		}
		c.walk(i, j, d, c.busPlan(), -1)
	}
	c.lens = c.lens[:0]
	for _, t := range c.traces {
		n := 0
		for k := 1; k < len(t.pts); k++ {
			n += max(abs(t.pts[k][0]-t.pts[k-1][0]), abs(t.pts[k][1]-t.pts[k-1][1]))
		}
		c.lens = append(c.lens, n)
	}
	c.linkEnds()
	c.boardFor = nil
}

// busPlan picks the turns of a bus: at step n, turn by ±45°. All the
// traces of a bus follow the same plan and stay parallel.
type turn struct{ at, by int }

func (c *circuit) busPlan() []turn {
	var plan []turn
	at := 1 + c.rng.IntN(4)
	for k := 0; k < 4; k++ {
		by := 1
		if c.rng.IntN(2) == 0 {
			by = -1
		}
		plan = append(plan, turn{at, by})
		at += 2 + c.rng.IntN(5)
		plan = append(plan, turn{at, -by})
		at += 3 + c.rng.IntN(10)
	}
	return plan
}

// walk lays a trace from node (i, j) in direction d.
func (c *circuit) walk(i, j, d int, plan []turn, chip int) {
	if !c.free(i, j) {
		return
	}
	pts := [][2]int{{i, j}}
	c.occ[j*c.nx+i] = true
	maxLen := 8 + c.rng.IntN(36)
	p := 0
	for n := 1; n <= maxLen; n++ {
		if p < len(plan) && plan[p].at == n {
			d = (d + plan[p].by + 8) % 8
			p++
		}
		v := circuitDirs[d]
		ni, nj := i+v[0], j+v[1]
		if !c.free(ni, nj) {
			break
		}
		if v[0] != 0 && v[1] != 0 {
			k := min(j, nj)*c.nx + min(i, ni)
			if c.diag[k] {
				break
			}
			c.diag[k] = true
		}
		i, j = ni, nj
		c.occ[j*c.nx+i] = true
		pts = append(pts, [2]int{i, j})
	}
	if len(pts) < 3 || (chip < 0 && len(pts) < 6) {
		for _, q := range pts {
			c.occ[q[1]*c.nx+q[0]] = false
		}
		return
	}
	// Only corners matter: nodes in a straight line are dropped.
	t := trace{chip: [2]int{chip, -1}}
	for k, q := range pts {
		if k > 0 && k < len(pts)-1 {
			a, b := pts[k-1], pts[k+1]
			if q[0]-a[0] == b[0]-q[0] && q[1]-a[1] == b[1]-q[1] {
				continue
			}
		}
		t.pts = append(t.pts, c.node(q[0], q[1]))
	}
	c.traces = append(c.traces, t)
}

// linkEnds links each via to the nearest trace end, two nodes away at
// most: a flash that reaches the end of a trace can jump onto the
// neighboring one. Chip pins are not linked, the flash stops there.
func (c *circuit) linkEnds() {
	c.links = c.links[:0]
	end := func(t *trace, e int) [2]int { return t.pts[e*(len(t.pts)-1)] }
	for i := range c.traces {
		l := [2]link{{-1, 0}, {-1, 0}}
		for e := 0; e < 2; e++ {
			if c.traces[i].chip[e] >= 0 {
				continue
			}
			a := end(&c.traces[i], e)
			best := 2*c.pitch + 1
			for j := range c.traces {
				for oe := 0; oe < 2 && j != i; oe++ {
					if c.traces[j].chip[oe] >= 0 {
						continue
					}
					b := end(&c.traces[j], oe)
					if d := max(abs(a[0]-b[0]), abs(a[1]-b[1])); d < best {
						best, l[e] = d, link{j, oe}
					}
				}
			}
		}
		c.links = append(c.links, l)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// paint draws the board into its image, in board pixels.
func (c *circuit) paint(p *gfx.Painter, th *theme.Theme) {
	if c.board == nil || c.board.Bounds().Dx() != c.lw || c.board.Bounds().Dy() != c.lh {
		c.release()
		c.board = ebiten.NewImageWithOptions(image.Rect(0, 0, c.lw, c.lh), &ebiten.NewImageOptions{Unmanaged: true})
	}
	c.board.Clear()
	copper := theme.Mix(th.BG, th.Accent2, 0.18)
	pad := theme.Mix(th.BG, th.Accent2, 0.3)
	for _, t := range c.traces {
		for k := 1; k < len(t.pts); k++ {
			c.line(p, t.pts[k-1], t.pts[k], copper)
		}
		for e, q := range [2][2]int{t.pts[0], t.pts[len(t.pts)-1]} {
			if t.chip[e] >= 0 {
				p.Rect(float32(q[0]), float32(q[1]), 1, 1, pad, 1)
				continue
			}
			x, y := float32(q[0]-1), float32(q[1]-1)
			p.Rect(x, y, 3, 3, pad, 1)
			p.Rect(x+1, y+1, 1, 1, th.BG, 1)
		}
	}
	body := theme.Mix(th.BG, th.FG, 0.07)
	edge := theme.Mix(th.BG, th.FG, 0.16)
	for _, ch := range c.chips {
		x, y := float32(ch.x0-1), float32(ch.y0-1)
		w, h := float32(ch.x1-ch.x0+3), float32(ch.y1-ch.y0+3)
		p.Rect(x, y, w, h, edge, 1)
		p.Rect(x+1, y+1, w-2, h-2, body, 1)
		p.Rect(x+2, y+2, 1, 1, edge, 1) // pin 1 marker
	}
	p.Flush(c.board)
	c.boardFor = th
}

func (c *circuit) line(p *gfx.Painter, a, b [2]int, col color.RGBA) {
	dx, dy := sign(b[0]-a[0]), sign(b[1]-a[1])
	n := max(abs(b[0]-a[0]), abs(b[1]-a[1]))
	for k := 0; k <= n; k++ {
		p.Rect(float32(a[0]+dx*k), float32(a[1]+dy*k), 1, 1, col, 1)
	}
}

func sign(x int) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}

// at returns the point of the trace at distance s from its start.
func (c *circuit) at(t *trace, s int) [2]int {
	for k := 1; k < len(t.pts); k++ {
		a, b := t.pts[k-1], t.pts[k]
		n := max(abs(b[0]-a[0]), abs(b[1]-a[1]))
		if s <= n {
			return [2]int{a[0] + sign(b[0]-a[0])*s, a[1] + sign(b[1]-a[1])*s}
		}
		s -= n
	}
	return t.pts[len(t.pts)-1]
}

func (c *circuit) step(dt float64) {
	if len(c.traces) == 0 {
		return
	}
	for i := range c.traces {
		t := &c.traces[i]
		t.glow[0] *= float32(math.Exp(-dt * 2.5))
		t.glow[1] *= float32(math.Exp(-dt * 2.5))
	}
	for i := range c.chips {
		c.chips[i].led *= float32(math.Exp(-dt * 3))
	}
	var next []pulse
	j := 0
	for _, q := range c.pulses {
		q.s += q.v * dt
		if q.s-float64(q.tail) > float64(c.lens[q.tr]) {
			continue
		}
		if q.s >= float64(c.lens[q.tr]) && q.s-q.v*dt < float64(c.lens[q.tr]) {
			end := 1
			if q.rev {
				end = 0
			}
			t := &c.traces[q.tr]
			t.glow[end] = 1
			if ch := t.chip[end]; ch >= 0 {
				c.chips[ch].led = 1
			}
			if l := c.links[q.tr][end]; q.hops > 0 && l.tr >= 0 {
				next = append(next, pulse{tr: l.tr, v: q.v, rev: l.end == 1,
					tail: max(30, c.lens[l.tr]), hops: q.hops - 1})
			}
		}
		c.pulses[j] = q
		j++
	}
	c.pulses = append(c.pulses[:j], next...)
	c.spawn -= dt
	for c.spawn <= 0 {
		c.spawn += 1.2 + c.rng.Float64()*2.4 // one flash every 1.2 to 3.6 s
		if len(c.pulses) >= circuitFlashes {
			continue
		}
		// Flashes prefer long traces: out of five picked at random, the
		// longest wins. The trail covers the whole trace.
		tr := c.rng.IntN(len(c.traces))
		for k := 0; k < 4; k++ {
			if o := c.rng.IntN(len(c.traces)); c.lens[o] > c.lens[tr] {
				tr = o
			}
		}
		c.pulses = append(c.pulses, pulse{tr: tr, v: 190 + c.rng.Float64()*170,
			rev: c.rng.IntN(2) == 0, tail: max(30, c.lens[tr]), hops: 2 + c.rng.IntN(3)})
	}
}

func (c *circuit) draw(dst *ebiten.Image, s *stage) {
	if len(c.traces) == 0 || s.settled && (s.size != c.size || s.grain() != c.grain) {
		c.generate(s)
		for i := 0; i < 150; i++ {
			c.step(1.0 / 30)
		}
	}
	if c.boardFor != s.th {
		c.paint(s.p, s.th)
	}
	g := float32(c.grain)
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(g), float64(g))
	dst.DrawImage(c.board, op)

	p, th := s.p, s.th
	hot := theme.Mix(th.Accent, th.FG, 0.55)
	for i := range c.traces {
		t := &c.traces[i]
		for e, q := range [2][2]int{t.pts[0], t.pts[len(t.pts)-1]} {
			if a := t.glow[e]; a > 0.02 {
				x, y := float32(q[0]-1)*g, float32(q[1]-1)*g
				p.Rect(x-g, y-g, 5*g, 5*g, th.Accent, 0.12*a)
				p.Rect(x, y, 3*g, 3*g, th.Accent, 0.8*a)
				p.Rect(x+g, y+g, g, g, hot, a)
			}
		}
	}
	for _, ch := range c.chips {
		x, y := float32(ch.x1-1)*g, float32(ch.y0+1)*g
		p.Rect(x, y, g, g, theme.Mix(th.BG, th.Warn, 0.35), 1)
		if ch.led > 0.02 {
			p.Rect(x-g, y-g, 3*g, 3*g, th.Warn, 0.2*ch.led)
			p.Rect(x, y, g, g, th.Warn, ch.led)
		}
	}
	for _, q := range c.pulses {
		t := &c.traces[q.tr]
		n := c.lens[q.tr]
		head := int(q.s)
		for k := q.tail; k >= 0; k-- {
			d := head - k
			if d < 0 || d > n {
				continue
			}
			if q.rev {
				d = n - d
			}
			pt := c.at(t, d)
			x, y := float32(pt[0])*g, float32(pt[1])*g
			if k == 0 {
				p.Rect(x-g, y-g, 3*g, 3*g, th.Accent, 0.25)
				p.Rect(x, y, g, g, hot, 1)
				continue
			}
			f := 1 - float32(k)/float32(q.tail+1)
			col := theme.Mix(th.Accent2, th.Accent, f)
			if f > 0.85 {
				col = hot
			}
			p.Rect(x, y, g, g, col, 0.95*float32(math.Pow(float64(f), 1.3)))
		}
	}
}
