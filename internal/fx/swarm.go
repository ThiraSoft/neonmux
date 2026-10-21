package fx

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Swarm: a murmuration of starlings, or a school of glowing fish. Each bird
// follows three rules (move away from those too close, align with its
// neighbors, stay with them) and the whole flock follows a point that
// wanders slowly across the screen. A bird's brightness depends on its
// heading: when the flock turns, waves of light run through it. Now and
// then a bird of prey crosses and the flock opens up in front of it.

const (
	swarmN    = 300
	swarmStep = 1.0 / 30
	swarmView = 0.075 // neighborhood radius, in screen heights
	swarmSep  = 0.02
	swarmMax  = 0.24
	swarmMin  = 0.1
	swarmFlee = 0.17
)

type boid struct {
	x, y, vx, vy float64
	z            float32 // depth: length of the streak and brightness
	tint         bool
}

type raptor struct {
	on           bool
	x, y, vx, vy float64
}

type swarm struct {
	t, acc   float64
	boids    []boid
	aspect   float64
	size     image.Point
	rng      *rand.Rand
	cells    [][]int32
	gw, gh   int
	pred     raptor
	nextPred float64
}

func newSwarm(r *rand.Rand) decor { return &swarm{rng: r, t: r.Float64() * 100} }

func (s *swarm) fps() int { return 30 }

func (s *swarm) init(st *stage) {
	s.size = st.size
	s.aspect = float64(st.size.X) / float64(st.size.Y)
	s.grid()
	s.boids = make([]boid, swarmN)
	cx, cy := s.aspect*0.5, 0.5
	for i := range s.boids {
		a := s.rng.Float64() * 2 * math.Pi
		r := math.Sqrt(s.rng.Float64()) * 0.25
		b := &s.boids[i]
		b.x, b.y = cx+math.Cos(a)*r*1.6, cy+math.Sin(a)*r
		b.vx, b.vy = 0.15, 0.02*(s.rng.Float64()-0.5)
		b.z = 0.35 + 0.65*s.rng.Float32()
		b.tint = s.rng.IntN(9) == 0
	}
	s.nextPred = 6 + s.rng.Float64()*8
	for i := 0; i < 240; i++ {
		s.advance()
	}
}

// grid splits the screen into cells the size of the neighborhood.
func (s *swarm) grid() {
	s.gw = int(math.Ceil(s.aspect / swarmView))
	s.gh = int(math.Ceil(1 / swarmView))
	s.cells = make([][]int32, s.gw*s.gh)
}

// reshape follows a new screen size without starting over: the flock keeps
// its relative position, only the aspect and the grid change.
func (s *swarm) reshape(st *stage) {
	aspect := float64(st.size.X) / float64(st.size.Y)
	k := aspect / s.aspect
	for i := range s.boids {
		b := &s.boids[i]
		b.x = min(b.x*k, aspect-1e-6)
	}
	s.pred.x *= k
	s.size, s.aspect = st.size, aspect
	s.grid()
}

func (s *swarm) step(dt float64) {
	if s.boids == nil {
		return
	}
	s.acc += dt
	for s.acc >= swarmStep {
		s.acc -= swarmStep
		s.advance()
	}
}

// advance moves the flock forward by one step.
func (s *swarm) advance() {
	const dt = swarmStep
	s.t += dt
	for i := range s.cells {
		s.cells[i] = s.cells[i][:0]
	}
	for i := range s.boids {
		k := s.cellOf(s.boids[i].x, s.boids[i].y)
		s.cells[k] = append(s.cells[k], int32(i))
	}
	// The point the flock chases: a slow Lissajous curve.
	tx := s.aspect * (0.5 + 0.3*math.Sin(s.t*0.071))
	ty := 0.5 + 0.24*math.Sin(s.t*0.113+1)
	s.raptorStep(dt)

	for i := range s.boids {
		b := &s.boids[i]
		var ax, ay, cx, cy, sx, sy float64
		n := 0
		gx, gy := int(b.x/swarmView), int(b.y/swarmView)
		for oy := -1; oy <= 1; oy++ {
			for ox := -1; ox <= 1; ox++ {
				x, y := gx+ox, gy+oy
				if x < 0 || y < 0 || x >= s.gw || y >= s.gh {
					continue
				}
				for _, j := range s.cells[y*s.gw+x] {
					if int(j) == i {
						continue
					}
					o := &s.boids[j]
					dx, dy := o.x-b.x, o.y-b.y
					d2 := dx*dx + dy*dy
					if d2 > swarmView*swarmView {
						continue
					}
					n++
					ax += o.vx
					ay += o.vy
					cx += o.x
					cy += o.y
					if d2 < swarmSep*swarmSep {
						d := math.Sqrt(d2) + 1e-6
						f := (swarmSep - d) / swarmSep
						sx -= dx / d * f
						sy -= dy / d * f
					}
				}
			}
		}
		fx, fy := 0.0, 0.0
		if n > 0 {
			fn := float64(n)
			fx += (ax/fn - b.vx) * 1.9
			fy += (ay/fn - b.vy) * 1.9
			fx += (cx/fn - b.x) * 1.1
			fy += (cy/fn - b.y) * 1.1
		}
		fx += sx * 7
		fy += sy * 7
		fx += (tx - b.x) * 0.22
		fy += (ty - b.y) * 0.22
		// The edges push back gently.
		const m = 0.07
		fx += edge(b.x, m) - edge(s.aspect-b.x, m)
		fy += edge(b.y, m) - edge(1-b.y, m)
		if s.pred.on {
			dx, dy := b.x-s.pred.x, b.y-s.pred.y
			if d := math.Hypot(dx, dy); d < swarmFlee {
				f := (1 - d/swarmFlee) * 4.5 / (d + 1e-3)
				fx += dx * f
				fy += dy * f
			}
		}
		b.vx += fx * dt
		b.vy += fy * dt
		sp := math.Hypot(b.vx, b.vy)
		if sp > swarmMax {
			b.vx, b.vy = b.vx/sp*swarmMax, b.vy/sp*swarmMax
		} else if sp < swarmMin {
			b.vx, b.vy = b.vx/(sp+1e-9)*swarmMin, b.vy/(sp+1e-9)*swarmMin
		}
		b.x += b.vx * dt
		b.y += b.vy * dt
		b.x = min(max(b.x, 0), s.aspect-1e-6)
		b.y = min(max(b.y, 0), 1-1e-6)
	}
}

func edge(d, m float64) float64 {
	if d >= m {
		return 0
	}
	return (m - d) / m * 2.2
}

func (s *swarm) cellOf(x, y float64) int {
	gx := min(max(int(x/swarmView), 0), s.gw-1)
	gy := min(max(int(y/swarmView), 0), s.gh-1)
	return gy*s.gw + gx
}

// raptorStep brings the bird of prey in, flies it and takes it out.
func (s *swarm) raptorStep(dt float64) {
	p := &s.pred
	if !p.on {
		s.nextPred -= dt
		if s.nextPred > 0 {
			return
		}
		s.nextPred = 16 + s.rng.Float64()*16
		// It comes in from an edge and aims at the heart of the flock.
		var cx, cy float64
		for _, b := range s.boids {
			cx += b.x
			cy += b.y
		}
		cx /= float64(len(s.boids))
		cy /= float64(len(s.boids))
		if s.rng.IntN(2) == 0 {
			p.x, p.y = -0.05, s.rng.Float64()
		} else {
			p.x, p.y = s.aspect+0.05, s.rng.Float64()
		}
		dx, dy := cx-p.x, cy-p.y
		d := math.Hypot(dx, dy)
		p.vx, p.vy = dx/d*0.5, dy/d*0.5
		p.on = true
		return
	}
	p.x += p.vx * dt
	p.y += p.vy * dt
	if p.x < -0.1 || p.x > s.aspect+0.1 || p.y < -0.1 || p.y > 1.1 {
		p.on = false
	}
}

func (s *swarm) draw(dst *ebiten.Image, st *stage) {
	switch {
	case s.boids == nil:
		s.init(st)
	case st.size != s.size:
		s.reshape(st)
	}
	p, th := st.p, st.th
	W, H := float32(st.size.X), float32(st.size.Y)
	g := float32(st.grain())
	for k := 0; k < 8; k++ {
		y0 := float32(math.Floor(float64(H*float32(k)/8/g))) * g
		y1 := float32(math.Floor(float64(H*float32(k+1)/8/g))) * g
		p.Rect(0, y0, W, y1-y0, th.Accent2, 0.005*float32(k))
	}
	snap := func(v float32) float32 { return float32(math.Floor(float64(v/g))) * g }
	// A streak of a few big pixels along the direction of flight, longer and
	// brighter for nearby birds.
	streak := func(x, y, vx, vy float64, n int, c color.RGBA, a float32) {
		sp := math.Hypot(vx, vy) + 1e-9
		dx, dy := float32(vx/sp)*g, float32(vy/sp)*g
		hx, hy := float32(x)*H, float32(y)*H
		for k := n; k >= 0; k-- {
			f := 1 - float32(k)/float32(n+1)
			p.Rect(snap(hx-dx*float32(k)), snap(hy-dy*float32(k)), g, g, c, a*f*f)
		}
	}
	for _, b := range s.boids {
		c := theme.Mix(th.Accent2, th.FG, 0.4*b.z)
		if b.tint {
			c = th.Accent
		}
		ang := math.Atan2(b.vy, b.vx)
		shine := float32(0.55 + 0.45*math.Abs(math.Sin(ang*2+s.t*0.2)))
		streak(b.x, b.y, b.vx, b.vy, 2+int(b.z*3), c, (0.4+0.6*b.z)*shine)
	}
	if s.pred.on {
		streak(s.pred.x, s.pred.y, s.pred.vx, s.pred.vy, 9, th.Warn, 0.95)
		x, y := snap(float32(s.pred.x)*H), snap(float32(s.pred.y)*H)
		p.Rect(x-g, y-g, 3*g, 3*g, th.Warn, 0.15)
	}
}
