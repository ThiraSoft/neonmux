package ui

import (
	"image"
	"image/color"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Ctx is passed to the panes on every frame.
type Ctx struct {
	App   *App
	Theme *theme.Theme
	Term  *gfx.Font // terminal font
	UI    *gfx.Font // UI font
	P     *gfx.Painter
	Now   time.Time
	DT    float64
	DSF   float64
	Px    float32 // thickness of a UI line
	Mods  Mods
}

// MouseKind tells mouse events apart.
type MouseKind int

const (
	MousePress MouseKind = iota
	MouseRelease
	MouseMove
	MouseWheel
)

// MouseEvent is a mouse event in screen coordinates (physical pixels).
type MouseEvent struct {
	Kind   MouseKind
	X, Y   int
	Button ebiten.MouseButton
	Wheel  float64
	Mods   Mods
	Clicks int // 2 = double click, 3 = triple click
}

// Pane is a panel of the layout: terminal, file explorer, image or audio.
type Pane interface {
	Base() *paneBase
	Kind() string
	Title() string
	Info() string
	// Layout receives the new content area (header excluded).
	Layout(ctx *Ctx, content image.Rectangle)
	// Update returns true if the pane needs a redraw.
	Update(ctx *Ctx) bool
	Key(ctx *Ctx, ev KeyEvent) bool
	Chars(ctx *Ctx, rs []rune)
	Mouse(ctx *Ctx, ev MouseEvent) bool
	// Draw draws the content into content (which may be animated).
	Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool)
	// AnimFPS returns the desired animation frame rate (0 = static).
	AnimFPS(ctx *Ctx) int
	// NextWake returns the next time the pane changes its look.
	NextWake(ctx *Ctx) time.Time
	Close()
	// Done reports that the content is finished (process exited, close requested).
	Done() bool
	// Mode returns a special mode shown in the bar ("COPY"…).
	Mode() string
}

// paneBase holds the shared state: animated position, notifications, effects.
type paneBase struct {
	id        int
	target    image.Rectangle // area assigned by the layout
	from      image.Rectangle // start of the animation
	animAt    time.Time
	notify    string // unread notification
	activity  bool   // unseen output
	glitchAt  time.Time
	flashAt   time.Time     // the agent finished: the border flashes (agent.go)
	flashC    color.RGBA    // flash color
	flashN    int           // number of flashes
	flashEach time.Duration // length of each flash
	closing   bool
	closeAt   time.Time
	hidden    bool      // hidden by monocle mode, a maximized floating pane or the garage
	name      string    // name chosen by the user (empty: automatic)
	floating  bool      // above the tiles (float.go)
	geom      floatGeom // position of the pane when it floats (empty: never floated)
	z         int       // stacking order of floating panes
	parked    bool      // in the garage: runs without being shown (garage.go)
	away      int       // workspace of a parked pane put away with it, 0 in the garage (workspace.go)
	texts     paneTexts // header and bar texts, kept from one frame to the next
}

// memo keeps a computed value as long as its key does not change. The header
// and the status bar are redrawn on every full frame (animated background,
// layout animation), their texts almost never: without memo, every frame
// would build the same strings again.
type memo[K comparable, V any] struct {
	key K
	val V
	ok  bool
}

func (m *memo[K, V]) get(key K, build func() V) V {
	if !m.ok || m.key != key {
		m.key, m.val, m.ok = key, build(), true
	}
	return m.val
}

// paneTexts holds the computed texts of a pane (draw.go).
type paneTexts struct {
	bar  memo[barKey, string]  // status bar label
	name memo[cutKey, string]  // truncated header title
	sub  memo[cutKey, string]  // shortened header directory
	info memo[infoKey, string] // info or badge, truncated
}

// cutKey is a text to fit into n cells.
type cutKey struct {
	text string
	n    int
}

// subtitled is implemented by panes that show secondary information in
// their header (the current directory of a terminal).
type subtitled interface{ Subtitle() string }

// displayName returns the pane's custom name, or its automatic title.
func displayName(p Pane) string {
	if n := p.Base().name; n != "" {
		return n
	}
	return p.Title()
}

func (b *paneBase) Base() *paneBase { return b }

const layoutAnim = 180 * time.Millisecond

// rect returns the animated position of the pane.
func (b *paneBase) rect(now time.Time) image.Rectangle {
	t := float64(now.Sub(b.animAt)) / float64(layoutAnim)
	if t >= 1 || b.from.Empty() {
		return b.target
	}
	e := 1 - (1-t)*(1-t)*(1-t) // cubic ease-out
	lerp := func(a, c int) int { return a + int(float64(c-a)*e) }
	return image.Rect(lerp(b.from.Min.X, b.target.Min.X), lerp(b.from.Min.Y, b.target.Min.Y),
		lerp(b.from.Max.X, b.target.Max.X), lerp(b.from.Max.Y, b.target.Max.Y))
}

func (b *paneBase) animating(now time.Time) bool {
	return now.Sub(b.animAt) < layoutAnim || now.Sub(b.glitchAt) < glitchDur ||
		now.Sub(b.flashAt) < time.Duration(b.flashN)*b.flashEach ||
		(b.closing && now.Sub(b.closeAt) < closeDur)
}

func (b *paneBase) moveTo(r image.Rectangle, now time.Time) {
	if r == b.target {
		return
	}
	cur := b.rect(now)
	if b.target.Empty() {
		// Appearing: the pane grows from its center.
		c := r.Min.Add(r.Size().Div(2))
		cur = image.Rect(c.X-2, c.Y-2, c.X+2, c.Y+2)
	}
	b.from = cur
	b.target = r
	b.animAt = now
}

const (
	glitchDur = 420 * time.Millisecond
	closeDur  = 260 * time.Millisecond
)
