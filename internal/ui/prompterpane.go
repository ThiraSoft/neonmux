package ui

import (
	"image"
	"image/color"
	"path/filepath"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/prompt"
	"github.com/ThiraSoft/neonmux/internal/server"
	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/vt"
)

// Prompter: the final replies of the agents, one word at a time on an
// anchor, as tracks of a playlist. The daemon detects the replies and keeps
// the queue (internal/prompt, internal/server/prompter.go); the pane only
// plays it. Without a daemon, the window keeps its own queue (localHub).

const promptDevice = "window"

// prompterButton is a clickable area of the pane.
type prompterButton struct {
	r  image.Rectangle
	do func(a *App)
}

// PrompterPane plays the queue.
type PrompterPane struct {
	paneBase
	pl       *player
	ctxWords int
	content  image.Rectangle
	buttons  []prompterButton
	last     time.Time // last Update, for the clock
	needOpen bool      // open the queue at the next Update (a client may not exist yet)
	hold     bool      // prompter_hold: space plays while it is held
	held     bool      // space is held and plays
}

// spaceDown reads the space key (a variable for the tests).
var spaceDown = func() bool { return ebiten.IsKeyPressed(ebiten.KeySpace) }

// holdPoll is how often a held space is looked at, to stop on its release.
const holdPoll = 25 * time.Millisecond

func NewPrompterPane(id int, c Config) *PrompterPane {
	p := &PrompterPane{pl: newPlayer(c.PrompterWPM), needOpen: true}
	p.id = id
	p.setConfig(c)
	return p
}

// setConfig takes the switches the window reads itself.
func (p *PrompterPane) setConfig(c Config) {
	p.ctxWords = c.PrompterContext
	p.pl.blank = time.Duration(c.PrompterBlankMs) * time.Millisecond
	p.hold = c.PrompterHold
}

func (p *PrompterPane) Kind() string  { return "prompter" }
func (p *PrompterPane) Title() string { return "prompter" }
func (p *PrompterPane) Mode() string  { return "" }
func (p *PrompterPane) Info() string {
	if n := len(p.pl.tracks); n > 0 {
		return plural(n, "track")
	}
	return ""
}
func (p *PrompterPane) Layout(ctx *Ctx, content image.Rectangle) { p.content = content }
func (p *PrompterPane) AnimFPS(ctx *Ctx) int                     { return 0 }
func (p *PrompterPane) Close()                                   {}
func (p *PrompterPane) Done() bool                               { return false }

func (p *PrompterPane) NextWake(ctx *Ctx) time.Time {
	var t time.Time
	if d := p.pl.untilNext(); d > 0 {
		t = ctx.Now.Add(d)
	}
	if p.held {
		if l := ctx.Now.Add(holdPoll); t.IsZero() || l.Before(t) {
			t = l
		}
	}
	if ctx.App.client == nil {
		// Without a daemon, the window looks at its agents every second.
		if l := ctx.Now.Add(time.Second); t.IsZero() || l.Before(t) {
			t = l
		}
	}
	return t
}

func (p *PrompterPane) Update(ctx *Ctx) bool {
	a := ctx.App
	if p.needOpen {
		p.needOpen = false
		a.promptCmd(server.PromptCmd{Op: "open"})
	}
	if a.client == nil {
		a.tickLocalPrompt(ctx.Now)
	}
	dt := time.Duration(0)
	if !p.last.IsZero() {
		dt = ctx.Now.Sub(p.last)
	}
	p.last = ctx.Now
	changed := p.pl.advance(dt)
	if p.held && !spaceDown() {
		p.held = false
		p.pl.hold(false)
		changed = true
	}
	return changed
}

func (p *PrompterPane) Key(ctx *Ctx, ev KeyEvent) bool {
	a := ctx.App
	switch ev.Name {
	case "space":
		if p.hold {
			p.held = true
			p.pl.hold(true)
		} else {
			p.pl.toggle()
		}
	case "enter":
		p.pl.toggle()
	case "left":
		p.pl.step(-1)
	case "right":
		p.pl.step(+1)
	case "up":
		p.speed(a, +1)
	case "down":
		p.speed(a, -1)
	default:
		return false
	}
	p.last = ctx.Now
	a.needRedraw = true
	return true
}

func (p *PrompterPane) Chars(ctx *Ctx, rs []rune) {
	for _, r := range rs {
		if r == 'n' {
			p.next(ctx.App)
		}
	}
}

func (p *PrompterPane) speed(a *App, d int) {
	p.pl.speed(d)
	a.cfg.PrompterWPM = p.pl.wpm
	a.saveConfig()
}

func (p *PrompterPane) next(a *App) {
	if seq, ok := p.pl.next(); ok {
		a.promptCmd(server.PromptCmd{Op: "dismiss", Seq: seq})
	}
	p.last = time.Now()
	a.needRedraw = true
}

func (p *PrompterPane) Mouse(ctx *Ctx, ev MouseEvent) bool {
	if ev.Kind != MousePress {
		return true
	}
	pt := image.Pt(ev.X, ev.Y)
	for _, b := range p.buttons {
		if pt.In(b.r) {
			b.do(ctx.App)
			p.last = ctx.Now
			ctx.App.needRedraw = true
			break
		}
	}
	return true
}

// agentLabel names the agent of a track: number and name of its pane if
// the window has it, else the program.
func (a *App) agentLabel(tr prompt.Track) string {
	name := tr.Agent
	for i, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.key == tr.Key {
			name = displayName(t)
			if !isParked(t) {
				name = "[" + itoa(i+1) + "] " + name
			}
			break
		}
	}
	if tr.Cwd != "" {
		name += " · " + filepath.Base(tr.Cwd)
	}
	return name
}

func (p *PrompterPane) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	if content != p.content {
		return
	}
	a := ctx.App
	f := ctx.Term
	th := ctx.Theme
	P := ctx.P
	cw, ch := float32(f.CellW), float32(f.CellH)
	x0, y0 := float32(content.Min.X)+cw, float32(content.Min.Y)
	cols := content.Dx()/f.CellW - 2
	p.buttons = p.buttons[:0]

	if a.client != nil && a.client.Alive() && a.client.ServerProto() > 0 && !a.client.PromptCapable() {
		P.Text(f, x0, y0+ch, gfx.Truncate("the daemon is older than the prompter: restart it (neonmux restart-server)", cols), th.Warn, 1)
		return
	}
	tr := p.pl.cur()
	if tr.Seq == 0 {
		lines := []string{"waiting for agents", "", "Each final reply of an agent becomes a track here.",
			"space play · ← → word · ↑ ↓ speed · n next track"}
		y := y0 + float32(content.Dy())/2 - ch*2
		for i, l := range lines {
			c := th.Dim
			if i == 0 {
				c = th.FG
			}
			l = gfx.Truncate(l, cols)
			P.Text(f, float32(content.Min.X)+(float32(content.Dx())-float32(gfx.TextCells(l))*cw)/2, y+ch*float32(i), l, c, 1)
		}
		return
	}

	// Header: agent, place in the queue, speed.
	idx := p.pl.index()
	right := "track " + itoa(idx+1) + "/" + itoa(len(p.pl.tracks)) + " · " + itoa(p.pl.wpm) + " wpm"
	rw := gfx.TextCells(right)
	P.Text(f, x0, y0, gfx.Truncate(a.agentLabel(tr), max(cols-rw-2, 4)), th.Accent2, 1)
	P.Text(f, x0+float32(cols-rw)*cw, y0, right, th.Dim, 1)

	// The line of words, the current one larger, its anchor at the center.
	scale := float32(1)
	if content.Dy() >= f.CellH*9 {
		scale = 2
	}
	before, cur, after, progress := p.pl.view(p.ctxWords)
	cx := float32(content.Min.X) + float32(content.Dx())/2
	wy := y0 + (float32(content.Dy())-ch*scale)/2 - ch
	runes := []rune(cur.T)
	anchor := min(cur.A, max(len(runes)-1, 0))
	startX := cx - (float32(gfx.TextCells(string(runes[:anchor])))+0.5)*cw*scale
	curW := float32(gfx.TextCells(cur.T)) * cw * scale
	minX, maxX := float32(content.Min.X)+cw/2, float32(content.Max.X)-cw/2
	// Ticks above and below the anchor.
	tick := ch * scale * 0.25
	P.Rect(cx-ctx.Px, wy-tick-ctx.Px*2, ctx.Px*2, tick, th.Accent, 1)
	P.Rect(cx-ctx.Px, wy+ch*scale+ctx.Px*2, ctx.Px*2, tick, th.Accent, 1)
	wordColor := th.FG
	if cur.M {
		wordColor = th.Accent2
	}
	// prompter_blank_ms: the anchor stays empty at the start of each word,
	// the context and the ticks stay.
	blank := p.pl.blanking()
	x := startX
	for i, r := range runes {
		if blank {
			break
		}
		c := wordColor
		if i == anchor && !cur.M {
			c = th.Accent
		}
		if x >= minX && x+cw*scale <= maxX {
			P.GlyphScaled(f.Glyph(r, 0, false), x, wy, scale, c, 1)
		}
		x += float32(gfx.TextCells(string(r))) * cw * scale
	}
	// Context: aligned on the bottom of the current word.
	cy := wy + ch*(scale-1)
	x = startX + curW + cw
	for i, w := range after {
		ww := float32(gfx.TextCells(w.T)) * cw
		if x+ww > maxX {
			break
		}
		P.Text(f, x, cy, w.T, contextColor(th.Dim, th.FG, w), fade(i, len(after)))
		x += ww + cw
	}
	x = startX - cw
	for i := len(before) - 1; i >= 0; i-- {
		w := before[i]
		ww := float32(gfx.TextCells(w.T)) * cw
		if x-ww < minX {
			break
		}
		P.Text(f, x-ww, cy, w.T, contextColor(th.Dim, th.FG, w), fade(len(before)-1-i, len(before)))
		x -= ww + cw
	}

	// Progress bar.
	by := wy + ch*scale + tick + ch*1.2
	bw := float32(content.Dx()) - cw*2
	P.Rect(x0, by, bw, ctx.Px*2, th.Dim, 0.5)
	P.Rect(x0, by, bw*float32(progress), ctx.Px*2, th.Accent, 1)

	// Buttons, drawn.
	p.drawButtons(ctx, x0, x0+bw, by+ch*0.8, cw, ch)

	// What comes next.
	if idx+1 < len(p.pl.tracks) {
		var parts []string
		for _, t := range p.pl.tracks[idx+1:] {
			parts = append(parts, t.Agent+" ("+plural(len(t.Words), "word")+")")
		}
		P.Text(f, x0, float32(content.Max.Y)-ch*1.2, gfx.Truncate("next: "+strings.Join(parts, " · "), cols), th.Dim, 1)
	}
}

func contextColor(dim, fg color.RGBA, w prompt.Word) color.RGBA {
	if w.M {
		return dim
	}
	return fg
}

// fade dims the context words more as they get further from the anchor.
func fade(dist, n int) float32 {
	if n <= 1 {
		return 0.45
	}
	return 0.5 - 0.3*float32(dist)/float32(n-1)
}

type promptIcon int

const (
	iconPrevWord promptIcon = iota
	iconPlay
	iconPause
	iconNextWord
	iconNextTrack
	iconMinus
	iconPlus
)

// drawButtons lays the speed out on the left, between left and right, and
// the reading controls on the right, as on the phone.
func (p *PrompterPane) drawButtons(ctx *Ctx, left, right, y, cw, ch float32) {
	th := ctx.Theme
	P := ctx.P
	size := ch
	gap := cw * 2
	play := iconPlay
	if p.pl.playing {
		play = iconPause
	}
	items := []struct {
		icon promptIcon
		do   func(a *App)
	}{
		{iconMinus, func(a *App) { p.speed(a, -1) }},
		{iconPlus, func(a *App) { p.speed(a, +1) }},
		{iconPrevWord, func(*App) { p.pl.step(-1) }},
		{play, func(*App) { p.pl.toggle() }},
		{iconNextWord, func(*App) { p.pl.step(+1) }},
		{iconNextTrack, func(a *App) { p.next(a) }},
	}
	// The controls end at the right edge; too narrow, they follow the speed.
	ctl := 4*size + 3*gap
	x := left
	for i, it := range items {
		if i == 2 {
			x = max(right-ctl, x+gap*2)
		}
		drawPromptIcon(P, it.icon, x, y, size, ctx.Px, th.FG)
		p.buttons = append(p.buttons, prompterButton{
			r:  image.Rect(int(x-cw/2), int(y-ch/4), int(x+size+cw/2), int(y+size+ch/4)),
			do: it.do,
		})
		x += size + gap
	}
}

// drawPromptIcon draws an icon with rectangles: no font glyph, no emoji.
func drawPromptIcon(P *gfx.Painter, icon promptIcon, x, y, s, px float32, c color.RGBA) {
	tri := func(x, y, w, h float32, right bool) {
		steps := max(int(h/px/2), 4)
		sh := h / float32(steps)
		for i := range steps {
			mid := (float32(i) + 0.5) / float32(steps)
			l := w * (1 - 2*abs32(mid-0.5))
			if right {
				P.Rect(x, y+float32(i)*sh, l, sh, c, 1)
			} else {
				P.Rect(x+w-l, y+float32(i)*sh, l, sh, c, 1)
			}
		}
	}
	bar := s * 0.18
	switch icon {
	case iconPlay:
		tri(x+s*0.2, y, s*0.7, s, true)
	case iconPause:
		P.Rect(x+s*0.2, y, bar*1.4, s, c, 1)
		P.Rect(x+s*0.6, y, bar*1.4, s, c, 1)
	case iconPrevWord:
		P.Rect(x, y+s*0.15, bar, s*0.7, c, 1)
		tri(x+bar, y+s*0.15, s*0.6, s*0.7, false)
	case iconNextWord:
		tri(x, y+s*0.15, s*0.6, s*0.7, true)
		P.Rect(x+s*0.6, y+s*0.15, bar, s*0.7, c, 1)
	case iconNextTrack:
		tri(x, y, s*0.45, s, true)
		tri(x+s*0.4, y, s*0.45, s, true)
		P.Rect(x+s*0.85, y, bar, s, c, 1)
	case iconMinus:
		P.Rect(x, y+s/2-bar/2, s, bar, c, 1)
	case iconPlus:
		P.Rect(x, y+s/2-bar/2, s, bar, c, 1)
		P.Rect(x+s/2-bar/2, y, bar, s, c, 1)
	}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// togglePrompter opens the prompter. Already open, the same shortcut gives
// it the focus (out of the garage if needed), then closes it.
func (a *App) togglePrompter() {
	for i, p := range a.panes {
		if _, ok := p.(*PrompterPane); ok {
			if i == a.focus {
				a.closePane(i)
			} else {
				a.goTo(p)
			}
			return
		}
	}
	pp := NewPrompterPane(a.newID(), a.cfg)
	a.addPane(pp)
	if prompterOf(a) == pp {
		pp.needOpen = false
		a.promptCmd(server.PromptCmd{Op: "open"})
	}
}

// prompterOf returns the prompter pane, nil if none is open.
func prompterOf(a *App) *PrompterPane {
	for _, p := range a.panes {
		if pp, ok := p.(*PrompterPane); ok {
			return pp
		}
	}
	return nil
}

// promptCmd drives the window's queue: in the daemon, or in the window
// itself without one.
func (a *App) promptCmd(cmd server.PromptCmd) {
	if a.client != nil {
		a.client.Prompt(cmd)
		return
	}
	if a.localHub == nil {
		a.localHub = prompt.NewHub(a.cfg.PromptOpts())
		a.localDet = prompt.NewDetector(a.localHub, prompt.DefaultReaders(), a.cfg.PrompterIntermediate)
	}
	switch cmd.Op {
	case "open":
		if a.localCancel == nil {
			hub := a.localHub
			a.localCancel = hub.Watch(promptDevice, func() {
				a.post(func() { a.setPromptQueue(hub.Queue(promptDevice)) })
			})
		}
		a.localHub.Open(promptDevice)
	case "close":
		a.localHub.Close(promptDevice)
		if a.localCancel != nil {
			a.localCancel()
			a.localCancel = nil
		}
	case "dismiss":
		a.localHub.Dismiss(promptDevice, cmd.Seq)
	}
}

// setPromptQueue gives the prompter the queue the daemon (or the local
// hub) sent.
func (a *App) setPromptQueue(q prompt.Queue) {
	for _, p := range a.panes {
		if pp, ok := p.(*PrompterPane); ok {
			pp.pl.setQueue(q)
			a.needRedraw = true
		}
	}
}

// localSource shows a terminal of the window to the local detector.
type localSource struct{ t *TermPane }

func (s localSource) Key() string                   { return s.t.key }
func (s localSource) Status() (vt.ProgStatus, bool) { return s.t.t.ProgramStatus() }
func (s localSource) Agent() string                 { return s.t.s.ForegroundName() }
func (s localSource) Pgid() int {
	if ss, ok := s.t.s.(*shell.Session); ok {
		return ss.ForegroundPgid()
	}
	return 0
}
func (s localSource) Cwd() string {
	if d := s.t.s.ForegroundCwd(); d != "" {
		return d
	}
	return s.t.s.Cwd()
}

// tickLocalPrompt runs the local detector once a second.
func (a *App) tickLocalPrompt(now time.Time) {
	if a.localDet == nil || now.Sub(a.localTick) < time.Second {
		return
	}
	a.localTick = now
	var srcs []prompt.Source
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.s != nil && t.t != nil {
			srcs = append(srcs, localSource{t})
		}
	}
	a.localDet.Tick(srcs, now)
}
