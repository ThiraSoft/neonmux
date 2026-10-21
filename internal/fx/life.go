package fx

import (
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"
)

// Conway's Game of Life on an LED panel. A cell being born lights up with a
// white flash, a dying cell leaves a trail that fades out in one second. So
// the panel does not end up covered in frozen ashes, cells that get too old
// crumble, and now and then a seed drops in: a methuselah (R-pentomino,
// acorn, diehard) that relights a whole neighborhood, a fleet of gliders or
// a handful of soup.

const lifeGen = 1.0 / 8 // length of a generation

const lifeSrc = `//kage:unit pixels

package main

var Cell float
var BG vec3
var FG vec3
var Accent vec3
var Accent2 vec3

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	o := imageSrc0Origin()
	cp := srcPos - o
	id := floor(cp)
	f := fract(cp)
	c := imageSrc0At(o + id + 0.5)
	heat := c.r
	born := c.g
	alive := c.b

	// A square LED, a one-pixel gap around it, the corners clipped.
	px := f * Cell
	in := px.x >= 1.0 && px.y >= 1.0 && px.x < Cell-1.0 && px.y < Cell-1.0
	ex := min(px.x-1.0, Cell-2.0-px.x)
	ey := min(px.y-1.0, Cell-2.0-px.y)
	if ex < 1.0 && ey < 1.0 {
		in = false
	}
	col := BG
	if in {
		col = mix(BG, FG, 0.035)
		lit := mix(Accent2, Accent, heat) * heat * 0.6
		if alive > 0.5 {
			lit = mix(Accent, FG, born*0.7) * (0.75 + 0.25*born)
		}
		col += lit
	} else {
		col += Accent * heat * 0.12
	}
	return vec4(col, 1)
}
`

type life struct {
	w, h   int
	cell   int
	size   [2]int
	cur    []bool
	nxt    []bool
	heat   []float32
	born   []float32
	age    []uint16
	acc    float64
	seedIn float64
	rng    *rand.Rand
	px     pixels
	dirty  bool
}

func newLife(r *rand.Rand) decor { return &life{rng: r} }

func (l *life) fps() int { return 20 }
func (l *life) release() { l.px.release() }

func (l *life) init(s *stage) {
	l.size = [2]int{s.size.X, s.size.Y}
	l.cell = max(2*s.grain(), s.size.Y/130)
	l.w = (s.size.X + l.cell - 1) / l.cell
	l.h = (s.size.Y + l.cell - 1) / l.cell
	n := l.w * l.h
	l.cur, l.nxt = make([]bool, n), make([]bool, n)
	l.heat, l.born, l.age = make([]float32, n), make([]float32, n), make([]uint16, n)
	l.px.resize(l.w, l.h)
	for i := 0; i < 7; i++ {
		l.soup(l.rng.IntN(l.w), l.rng.IntN(l.h), 10+l.rng.IntN(14), 0.4)
	}
	for i := 0; i < 3; i++ {
		l.seed()
	}
	// A few generations ahead: the still background already shows life.
	for i := 0; i < 50; i++ {
		l.step(lifeGen)
	}
}

func (l *life) step(dt float64) {
	if l.cur == nil {
		return
	}
	l.acc += dt
	for l.acc >= lifeGen {
		l.acc -= lifeGen
		l.generation()
	}
	l.seedIn -= dt
	if l.seedIn <= 0 {
		l.seedIn = 4 + l.rng.Float64()*5
		l.seed()
	}
	up := float32(dt * 9)
	fade := float32(math.Exp(-dt * 2.4))
	flash := float32(math.Exp(-dt * 6))
	for i, a := range l.cur {
		if a {
			l.heat[i] = min(1, l.heat[i]+up)
		} else {
			l.heat[i] *= fade
		}
		l.born[i] *= flash
	}
	l.dirty = true
}

func (l *life) generation() {
	w, h := l.w, l.h
	pop := 0
	for y := 0; y < h; y++ {
		ym, yp := ((y+h-1)%h)*w, ((y+1)%h)*w
		yc := y * w
		for x := 0; x < w; x++ {
			xm, xp := (x+w-1)%w, (x+1)%w
			n := 0
			for _, j := range [8]int{ym + xm, ym + x, ym + xp, yc + xm, yc + xp, yp + xm, yp + x, yp + xp} {
				if l.cur[j] {
					n++
				}
			}
			i := yc + x
			alive := n == 3 || (n == 2 && l.cur[i])
			// Old cells crumble: the ashes do not pile up.
			if alive && l.cur[i] && l.age[i] > 260 && l.rng.IntN(150) == 0 {
				alive = false
			}
			l.nxt[i] = alive
			switch {
			case alive && !l.cur[i]:
				l.born[i], l.age[i] = 1, 0
			case alive:
				l.age[i] = min(l.age[i]+1, math.MaxUint16)
			}
			if alive {
				pop++
			}
		}
	}
	l.cur, l.nxt = l.nxt, l.cur
	if pop < l.w*l.h/60 && l.rng.IntN(4) == 0 {
		l.seed()
	}
}

// The seeds: methuselahs that keep churning for a long time from almost
// nothing, a glider and a lightweight spaceship.
var lifeShapes = [][]string{
	{".##", "##.", ".#."},                         // R-pentomino
	{".#.....", "...#...", "##..###"},             // acorn
	{"......#.", "##......", ".#...###"},          // diehard
	{".#.", "..#", "###"},                         // glider
	{".#..#", "#....", "#...#", "####."},          // lightweight spaceship
	{"###.#", "#....", "...##", ".##.#", "#.#.#"}, // infinite growth
}

func (l *life) seed() {
	x, y := l.rng.IntN(l.w), l.rng.IntN(l.h)
	switch r := l.rng.IntN(10); {
	case r < 5:
		l.stamp(lifeShapes[[]int{0, 1, 2, 5}[l.rng.IntN(4)]], x, y)
	case r < 8:
		shape := lifeShapes[3+l.rng.IntN(2)]
		for k := 0; k < 3; k++ {
			l.stamp(shape, x+k*7, y+k*5)
		}
	default:
		l.soup(x, y, 8, 0.45)
	}
}

// stamp places a pattern, flipped at random.
func (l *life) stamp(shape []string, x0, y0 int) {
	fx, fy, tr := l.rng.IntN(2) == 1, l.rng.IntN(2) == 1, l.rng.IntN(2) == 1
	for j, row := range shape {
		for i, c := range row {
			if c != '#' {
				continue
			}
			x, y := i, j
			if fx {
				x = len(row) - 1 - i
			}
			if fy {
				y = len(shape) - 1 - j
			}
			if tr {
				x, y = y, x
			}
			l.set(x0+x, y0+y)
		}
	}
}

func (l *life) soup(x0, y0, n int, density float64) {
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			if l.rng.Float64() < density {
				l.set(x0+i, y0+j)
			}
		}
	}
}

func (l *life) set(x, y int) {
	x, y = (x%l.w+l.w)%l.w, (y%l.h+l.h)%l.h
	i := y*l.w + x
	if !l.cur[i] {
		l.cur[i], l.born[i], l.age[i] = true, 1, 0
	}
}

func (l *life) draw(dst *ebiten.Image, s *stage) {
	if l.cur == nil || s.settled && l.size != [2]int{s.size.X, s.size.Y} {
		l.init(s)
	}
	if l.dirty {
		buf := l.px.buf
		for i := range l.cur {
			buf[i*4] = byte(l.heat[i] * 255)
			buf[i*4+1] = byte(l.born[i] * 255)
			buf[i*4+2] = 0
			if l.cur[i] {
				buf[i*4+2] = 255
			}
			buf[i*4+3] = 255
		}
		l.px.img.WritePixels(buf)
		l.dirty = false
	}
	shader := lazyShader(lifeSrc)
	if shader == nil {
		return
	}
	scaleShader(dst, l.px.img, float32(l.cell), shader, map[string]any{
		"Cell":    float32(l.cell),
		"BG":      vec3(s.th.BG),
		"FG":      vec3(s.th.FG),
		"Accent":  vec3(s.th.Accent),
		"Accent2": vec3(s.th.Accent2),
	}, ebiten.BlendSourceOver)
}
