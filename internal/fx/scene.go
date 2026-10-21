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

// Backgrounds lists the available backgrounds, in cycle order.
var Backgrounds = []string{"grid", "rain", "stars", "dust", "aurora", "topo", "lava", "fire",
	"life", "swarm", "circuit", "scope", "city", "none"}

// Background draws the backdrop behind the panes. When animated, it asks
// for frames at its own rate; when still, it only costs at render time.
type Background struct {
	Kind     string
	Animated bool

	t       float64
	shaders *Shaders
	rain    []rainDrop
	stars   []star
	motes   []mote
	size    image.Point
	rng     *rand.Rand

	decor     decor // current self-contained background (decor.go)
	decorKind string
	resizedAt float64 // value of t at the last size change
}

type rainDrop struct {
	x, y, speed float64
	length      int
	glyphs      []rune
}

type star struct{ x, y, z float64 }

// mote is a speck of dust that rises slowly, twinkling ("dust" background).
type mote struct{ x, y, speed, phase, size float64 }

// NewBackground creates a background.
func NewBackground(sh *Shaders) *Background {
	return &Background{shaders: sh, rng: rand.New(rand.NewPCG(7, 42))}
}

// FPS returns the rate the background wants (0 if it is still).
func (b *Background) FPS() int {
	if !b.Animated || b.Kind == "none" {
		return 0
	}
	switch b.Kind {
	case "rain":
		return 24
	case "stars":
		return 30
	case "dust":
		return 20 // slow motion: 20 fps is enough
	}
	if d := b.pickDecor(); d != nil {
		return d.fps()
	}
	return 30
}

// Update moves the animation forward by dt seconds.
func (b *Background) Update(dt float64) {
	if !b.Animated {
		return
	}
	b.t += dt
	switch b.Kind {
	case "rain":
		for i := range b.rain {
			d := &b.rain[i]
			d.y += d.speed * dt
			if b.rng.IntN(6) == 0 {
				d.glyphs[b.rng.IntN(len(d.glyphs))] = randKana(b.rng)
			}
		}
	case "stars":
		for i := range b.stars {
			s := &b.stars[i]
			s.x -= dt * 0.02 / s.z
			if s.x < 0 {
				s.x += 1
				s.y = b.rng.Float64()
			}
		}
	case "dust":
		for i := range b.motes {
			m := &b.motes[i]
			m.y -= dt * m.speed
			m.x += dt * 0.004 * math.Sin(b.t*0.6+m.phase)
			if m.y < -0.02 {
				m.y = 1.02
				m.x = b.rng.Float64()
			}
		}
	default:
		if d := b.pickDecor(); d != nil {
			d.step(dt)
		}
	}
}

func randKana(r *rand.Rand) rune {
	if r.IntN(5) == 0 {
		return '0' + rune(r.IntN(10))
	}
	return 0xff66 + rune(r.IntN(56)) // half-width katakana, like in the movie
}

func (b *Background) ensure(size image.Point, f *gfx.Font) {
	if size == b.size {
		return
	}
	b.size = size
	cols := size.X / f.CellW
	b.rain = b.rain[:0]
	for c := 0; c < cols; c += 2 {
		d := rainDrop{
			x:      float64(c * f.CellW),
			y:      b.rng.Float64() * float64(size.Y) * 1.5,
			speed:  float64(f.CellH) * (4 + b.rng.Float64()*10),
			length: 6 + b.rng.IntN(18),
		}
		d.glyphs = make([]rune, d.length)
		for i := range d.glyphs {
			d.glyphs[i] = randKana(b.rng)
		}
		b.rain = append(b.rain, d)
	}
	b.stars = b.stars[:0]
	for i := 0; i < 180; i++ {
		b.stars = append(b.stars, star{b.rng.Float64(), b.rng.Float64(), 0.3 + b.rng.Float64()*0.7})
	}
	b.motes = b.motes[:0]
	for i := 0; i < 70; i++ {
		b.motes = append(b.motes, mote{
			x: b.rng.Float64(), y: b.rng.Float64(),
			speed: 0.006 + b.rng.Float64()*0.014, phase: b.rng.Float64() * 2 * math.Pi,
			size: 1 + float64(b.rng.IntN(2)),
		})
	}
}

// Draw paints the background onto dst.
func (b *Background) Draw(dst *ebiten.Image, p *gfx.Painter, f *gfx.Font, th *theme.Theme, pixel float32) {
	// dst can be a sub-image (partial render): coordinates stay those of
	// the whole screen, the drawing is simply clipped.
	clip := dst.Bounds()
	size := image.Pt(clip.Max.X, clip.Max.Y)
	if full, ok := fullSize(dst); ok {
		size = full
	}
	if size != b.size {
		b.resizedAt = b.t
	}
	b.ensure(size, f)
	// Pick the self-contained background here, even for the first four,
	// which have none: the one being left is freed with its video memory.
	d := b.pickDecor()
	dst.Fill(th.BG)
	switch b.Kind {
	case "grid":
		op := &ebiten.DrawRectShaderOptions{}
		op.GeoM.Translate(float64(clip.Min.X), float64(clip.Min.Y))
		op.Uniforms = gridVars(b.t, size, pixel, th)
		dst.DrawRectShader(clip.Dx(), clip.Dy(), b.shaders.Grid, op)
	case "rain":
		h := float64(size.Y)
		for i := range b.rain {
			d := &b.rain[i]
			span := float64(d.length * f.CellH)
			if d.y-span > h {
				d.y = -b.rng.Float64() * h * 0.5
				d.speed = float64(f.CellH) * (4 + b.rng.Float64()*10)
			}
			for k := 0; k < d.length; k++ {
				y := d.y - float64(k*f.CellH)
				if y < -float64(f.CellH) || y > h {
					continue
				}
				c := th.Accent
				a := float32(1 - float64(k)/float64(d.length))
				if k == 0 {
					c = theme.Mix(th.Accent, color.RGBA{255, 255, 255, 255}, 0.7)
				}
				p.Glyph(f.Glyph(d.glyphs[k], 0, false), float32(d.x), float32(y), c, a*0.55)
			}
		}
		p.Flush(dst)
	case "stars":
		w, h := float32(size.X), float32(size.Y)
		for _, s := range b.stars {
			sz := float32(math.Max(1, float64(pixel)*(1.4-s.z)))
			tw := float32(0.5 + 0.5*math.Sin(b.t*2+s.y*40))
			c := theme.Mix(th.Accent2, th.FG, float32(s.z))
			p.Rect(float32(s.x)*w, float32(s.y)*h, sz, sz, c, (0.25+0.6*float32(1-s.z))*(0.6+0.4*tw))
		}
		p.Flush(dst)
	case "dust":
		// A veil that gets barely lighter toward the bottom, then gold and
		// amethyst specks of dust that twinkle softly.
		w, h := float32(size.X), float32(size.Y)
		for k := 0; k < 6; k++ {
			y := h * float32(k) / 6
			p.Rect(0, y, w, h/6, th.Accent, 0.008*float32(k))
		}
		for i, m := range b.motes {
			c := th.Accent2
			if i%3 == 0 {
				c = th.Accent
			}
			tw := float32(0.5 + 0.5*math.Sin(b.t*1.3+m.phase*3))
			s := pixel * float32(m.size) * 0.5
			x, y := float32(m.x)*w, float32(m.y)*h
			p.Rect(x-s, y-s, 3*s, 3*s, c, 0.05*tw)
			p.Rect(x, y, s, s, c, 0.18+0.3*tw)
		}
		p.Flush(dst)
	default:
		if d != nil {
			// When still, the background has no later frame to rebuild
			// in: it does it right away.
			settled := !b.Animated || b.t-b.resizedAt >= resizeSettle
			d.draw(dst, &stage{clip: clip, size: size, p: p, f: f, th: th, pixel: pixel, settled: settled})
			p.Flush(dst)
		}
	}
}

// ScreenSize is the size of the whole screen, used when the background is
// redrawn into a sub-image.
var ScreenSize image.Point

func fullSize(dst *ebiten.Image) (image.Point, bool) {
	return ScreenSize, ScreenSize != image.Point{}
}

// Particle is a free particle (confetti, sparks).
type Particle struct {
	X, Y, VX, VY float32
	Life, Max    float32
	Size         float32
	Color        color.RGBA
	Gravity      float32
}

// Particles manages a cloud of particles drawn as big pixels.
type Particles struct {
	list []Particle
	rng  *rand.Rand
}

// NewParticles creates an empty system.
func NewParticles() *Particles {
	return &Particles{rng: rand.New(rand.NewPCG(3, 9))}
}

// Active reports whether there are particles left to animate.
func (ps *Particles) Active() bool { return len(ps.list) > 0 }

// Burst throws n particles from (x, y), confetti style.
func (ps *Particles) Burst(x, y float32, n int, palette []color.RGBA, speed, size float32) {
	for i := 0; i < n; i++ {
		a := ps.rng.Float64() * 2 * math.Pi
		v := speed * (0.3 + float32(ps.rng.Float64())*0.9)
		life := 0.8 + float32(ps.rng.Float64())*0.9
		ps.list = append(ps.list, Particle{
			X: x, Y: y,
			VX: float32(math.Cos(a)) * v, VY: float32(math.Sin(a))*v - speed*0.6,
			Life: life, Max: life, Size: size * (1 + float32(ps.rng.IntN(2))),
			Color: palette[ps.rng.IntN(len(palette))], Gravity: speed * 1.4,
		})
	}
}

// Sparks throws a few short-lived sparks (turbo mode while typing).
func (ps *Particles) Sparks(x, y float32, palette []color.RGBA, size float32) {
	for i := 0; i < 5; i++ {
		a := -math.Pi/2 + (ps.rng.Float64()-0.5)*2.2
		v := size * (25 + float32(ps.rng.Float64())*35)
		life := 0.25 + float32(ps.rng.Float64())*0.25
		ps.list = append(ps.list, Particle{
			X: x, Y: y, VX: float32(math.Cos(a)) * v, VY: float32(math.Sin(a)) * v,
			Life: life, Max: life, Size: size, Color: palette[ps.rng.IntN(len(palette))], Gravity: v * 2,
		})
	}
}

// Update ages the particles.
func (ps *Particles) Update(dt float32) {
	j := 0
	for i := range ps.list {
		p := &ps.list[i]
		p.Life -= dt
		if p.Life <= 0 {
			continue
		}
		p.VY += p.Gravity * dt
		p.X += p.VX * dt
		p.Y += p.VY * dt
		ps.list[j] = *p
		j++
	}
	ps.list = ps.list[:j]
}

// Draw draws the particles.
func (ps *Particles) Draw(p *gfx.Painter) {
	for _, q := range ps.list {
		a := q.Life / q.Max
		s := q.Size
		x := float32(math.Floor(float64(q.X/s))) * s // snapped to a grid: pixel art look
		y := float32(math.Floor(float64(q.Y/s))) * s
		p.Rect(x, y, s, s, q.Color, a)
	}
}

// gridPeriod is a period shared by the sun bands (1/0.15 s) and the grid
// lines (1/0.9 s): the shader clock can wrap on it without a jump, and so
// keeps its precision in float32.
const gridPeriod = 20.0

// gridVars builds the uniforms of the synthwave background.
func gridVars(t float64, size image.Point, pixel float32, th *theme.Theme) map[string]any {
	return map[string]any{
		"Time":  float32(math.Mod(t, gridPeriod)),
		"Size":  []float32{float32(size.X), float32(size.Y)},
		"Pixel": pixel,
		"BG":    vec3(th.BG),
		"Sky":   vec3(theme.Mix(th.BG, th.Accent2, 0.35)),
		"Line":  vec3(theme.Mix(th.Accent, th.BG, 0.25)),
		"SunA":  vec3(th.Warn),
		"SunB":  vec3(th.Accent),
	}
}
