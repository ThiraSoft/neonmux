package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/games"
	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/server"
	"github.com/ThiraSoft/neonmux/internal/typing"
	"github.com/ThiraSoft/neonmux/internal/wiki"
)

// Typing test: a monkeytype-style game on a random English Wikipedia
// article, to pass the time while agents work. The engine and its scoring
// live in internal/typing; this pane asks for the text, feeds the keys and
// draws.

// articleSource hands out random articles (wiki.Source; a fake in tests).
type articleSource interface {
	Next(ctx context.Context) (wiki.Article, error)
	Prefetch()
}

// windowFocused reports whether the neonmux window has the system focus:
// a game pauses when the player switches to another application.
var windowFocused = ebiten.IsFocused

// gameSource returns the article source, created on first use: no request
// and no goroutine until a game opens.
func (a *App) gameSource() articleSource {
	if a.wikiSrc == nil {
		a.wikiSrc = wiki.NewSource(wiki.DefaultCacheDir())
	}
	return a.wikiSrc
}

// gameRecords returns the games' records (games.json, next to the layout).
func (a *App) gameRecords() *games.Store {
	if a.gameStore == nil {
		a.gameStore = games.Load(filepath.Join(server.StateDir(), "games.json"))
	}
	return a.gameStore
}

// showTyping opens the typing test, or goes back to it.
func (a *App) showTyping() {
	a.showGame(func(p Pane) bool { _, ok := p.(*TypingPane); return ok },
		func(id int) Pane { return NewTypingPane(id) })
}

const (
	caretGlide = 80 * time.Millisecond
	textLines  = 3 // lines of text on screen, as in monkeytype
	// moreBackoff is the wait after a failed request for more text.
	moreBackoff = 10 * time.Second
)

// TypingPane is the typing test.
type TypingPane struct {
	paneBase
	mode    typing.Mode
	words   []string // the whole article: a mode change starts again from it
	title   string
	test    *typing.Test
	waiting bool // a new text is on its way
	more    bool // more text (test against the clock) is on its way
	moreAt  time.Time
	gen     int // texts asked for: an older answer is dropped
	result  *typing.Result
	newBest bool
	content image.Rectangle
	shown   int // seconds left on screen (redraw when it changes)
	closed  bool

	// The caret glides from where it was to the next letter.
	caretFrom, caretTo float32
	caretRow           int
	caretAt            time.Time

	// Iosevka loaded at the size of the large text, by size in pixels:
	// a rasterized glyph scaled up on the GPU comes out blurry.
	fonts map[float64]*gfx.Font
}

func NewTypingPane(id int) *TypingPane {
	p := &TypingPane{mode: typing.Modes[1]}
	p.id = id
	return p
}

func (p *TypingPane) Kind() string  { return "typing" }
func (p *TypingPane) Title() string { return "typing test" }
func (p *TypingPane) Mode() string  { return "" }
func (p *TypingPane) Close()        { p.closed = true }
func (p *TypingPane) Done() bool    { return p.closed }

func (p *TypingPane) Info() string {
	if p.title == "" {
		return p.mode.String()
	}
	return p.mode.String() + " · " + p.title
}

func (p *TypingPane) Layout(ctx *Ctx, content image.Rectangle) { p.content = content }

func (p *TypingPane) Mouse(ctx *Ctx, ev MouseEvent) bool { return false }

func (p *TypingPane) suspend(now time.Time) {
	if p.test != nil {
		p.test.Pause(now)
	}
}

func (p *TypingPane) running() bool {
	return p.test != nil && p.result == nil && p.test.Started()
}

// request asks the source for an article in the background. more appends
// it to the running test instead of starting a new one.
func (p *TypingPane) request(a *App, more bool) {
	if more {
		p.more = true
	} else {
		p.waiting = true
		p.gen++
	}
	gen, src := p.gen, a.gameSource()
	go func() {
		art, err := src.Next(context.Background())
		a.post(func() { p.apply(a, gen, more, art, err) })
	}()
}

// gotText applies the answer to the latest request for a new text.
func (p *TypingPane) gotText(a *App, art wiki.Article, err error) { p.apply(a, p.gen, false, art, err) }

func (p *TypingPane) apply(a *App, gen int, more bool, art wiki.Article, err error) {
	if p.closed || gen != p.gen || !slices.Contains(a.panes, Pane(p)) {
		return // the pane was closed, or another text was asked for since
	}
	if more {
		p.more = false
		switch {
		case err != nil:
			p.moreAt = time.Now().Add(moreBackoff)
		case p.test != nil && p.result == nil:
			p.test.Append(art.Words())
		}
		return
	}
	p.waiting = false
	if err != nil {
		a.toast("Wikipedia unreachable", a.theme.Warn)
		a.closePane(slices.Index(a.panes, Pane(p)))
		return
	}
	p.title = art.Title
	p.words = art.Words()
	if m, ok := typing.ParseMode(a.gameRecords().Typing.Mode); ok {
		p.mode = m
	}
	p.restart()
	a.gameSource().Prefetch()
	a.needRedraw = true
}

// restart starts the test again on the current words.
func (p *TypingPane) restart() {
	p.test = typing.New(slices.Clone(p.words), p.mode)
	p.result, p.newBest = nil, false
	p.caretFrom, p.caretTo, p.caretRow = 0, 0, 0
}

// newText drops the test and asks for another article.
func (p *TypingPane) newText(a *App) {
	p.test, p.result, p.title = nil, nil, ""
	p.request(a, false)
}

func (p *TypingPane) finish(a *App, now time.Time) {
	r := p.test.Result(now)
	p.result = &r
	// A words test that never got its count of words is no record.
	if r.Seconds > 0 && (p.mode.Kind != "words" || len(p.test.Words) == p.mode.N) {
		st := a.gameRecords()
		p.newBest = st.RecordTyping(p.mode.String(), r.WPM, r.Accuracy, now)
		_ = st.Save() // without a writable file, the records only live in memory
	}
}

func (p *TypingPane) Update(ctx *Ctx) bool {
	a := ctx.App
	if p.test == nil {
		if !p.waiting && !p.closed {
			p.request(a, false)
		}
		return false
	}
	if p.result != nil {
		return false
	}
	if p.test.Started() && !a.playing(p) {
		p.test.Pause(ctx.Now)
	}
	if p.test.Started() && p.test.Done(ctx.Now) {
		p.finish(a, ctx.Now)
		return true
	}
	if p.test.NeedsMore() && !p.more && !ctx.Now.Before(p.moreAt) {
		p.request(a, true)
	}
	left := int(math.Ceil(p.test.Remaining(ctx.Now).Seconds()))
	changed := left != p.shown
	p.shown = left
	return changed || ctx.Now.Sub(p.caretAt) < caretGlide
}

func (p *TypingPane) AnimFPS(ctx *Ctx) int {
	if ctx.Now.Sub(p.caretAt) < caretGlide {
		return 60
	}
	return 0
}

func (p *TypingPane) NextWake(ctx *Ctx) time.Time {
	if !p.running() || p.test.Mode.Kind != "time" {
		return time.Time{}
	}
	left := p.test.Remaining(ctx.Now)
	step := left % time.Second
	if step == 0 {
		step = time.Second
	}
	return ctx.Now.Add(step)
}

func (p *TypingPane) Key(ctx *Ctx, ev KeyEvent) bool {
	a := ctx.App
	switch {
	case ev.Name == "tab":
		p.newText(a)
	case p.test == nil:
		return false
	case p.result != nil && ev.Name == "enter":
		p.newText(a)
	case p.result != nil:
		return false
	case !p.test.Started() && (ev.Name == "left" || ev.Name == "right"):
		i := slices.Index(typing.Modes, p.mode)
		if ev.Name == "left" {
			i += len(typing.Modes) - 1
		} else {
			i++
		}
		p.mode = typing.Modes[i%len(typing.Modes)]
		st := a.gameRecords()
		st.Typing.Mode = p.mode.String()
		_ = st.Save()
		p.restart()
	case ev.Name == "backspace" && ev.Mods&(ModAlt|ModCtrl) != 0:
		p.test.DeleteWord(ctx.Now)
		p.moved(ctx)
	case ev.Name == "backspace":
		p.test.Backspace(ctx.Now)
		p.moved(ctx)
	default:
		return false
	}
	a.needRedraw = true
	return true
}

func (p *TypingPane) Chars(ctx *Ctx, rs []rune) {
	if p.test == nil || p.result != nil || ctx.Mods&(ModCtrl|ModMeta) != 0 {
		return
	}
	for _, r := range rs {
		if r >= ' ' {
			p.test.Type(r, ctx.Now)
		}
	}
	p.moved(ctx)
	if p.test.Done(ctx.Now) {
		p.finish(ctx.App, ctx.Now)
	}
	ctx.App.needRedraw = true
}

// moved starts the caret's glide toward its new place.
func (p *TypingPane) moved(ctx *Ctx) {
	p.caretFrom = p.caretX(ctx.Now)
	p.caretAt = ctx.Now
}

// caretX returns where the caret is drawn, between its old and new place.
func (p *TypingPane) caretX(now time.Time) float32 {
	t := float32(now.Sub(p.caretAt)) / float32(caretGlide)
	if t >= 1 || t < 0 {
		return p.caretTo
	}
	return p.caretFrom + (p.caretTo-p.caretFrom)*t
}

// Drawing

// typingScale is the scale of the text: twice the terminal font when there
// is room for a readable line.
func typingScale(f *gfx.Font, content image.Rectangle) int {
	if content.Dx()/(f.CellW*2) >= 48 {
		return 2
	}
	return 1
}

// fontAt returns Iosevka at scale times the terminal font size, rasterized
// at that size. A font that does not load leaves the terminal font.
func (p *TypingPane) fontAt(ctx *Ctx, scale int) *gfx.Font {
	size := ctx.App.cfg.FontSize * float64(scale)
	if f := p.fonts[size*ctx.DSF]; f != nil {
		return f
	}
	f, err := gfx.LoadFont(gfx.FontIosevka, size, ctx.DSF)
	if err != nil {
		return ctx.Term
	}
	if p.fonts == nil {
		p.fonts = map[float64]*gfx.Font{}
	}
	p.fonts[size*ctx.DSF] = f
	return f
}

// drawRunes writes s one cell per rune and returns the width used.
func drawRunes(P *gfx.Painter, f *gfx.Font, x, y float32, s string, c color.RGBA, alpha float32) float32 {
	x0 := x
	for _, r := range s {
		if r != ' ' {
			gr := r
			if P.Runes != nil {
				gr = P.Runes(gr)
			}
			P.Glyph(f.Glyph(gr, 0, false), x, y, c, alpha)
		}
		x += float32(f.CellW)
	}
	return x - x0
}

// wordLines cuts the words into lines of at most cols cells. A word takes
// the room of what is typed when that is longer.
func (p *TypingPane) wordLines(cols int) [][2]int {
	var lines [][2]int
	start, n := 0, 0
	for i, w := range p.test.Words {
		l := max(len(w), len([]rune(p.test.Typed[i])))
		if n > 0 && n+1+l > cols {
			lines = append(lines, [2]int{start, i})
			start, n = i, 0
		}
		if n > 0 {
			n++
		}
		n += l
	}
	return append(lines, [2]int{start, len(p.test.Words)})
}

func (p *TypingPane) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	f, th, P := ctx.Term, ctx.Theme, ctx.P
	cw, ch := float32(f.CellW), float32(f.CellH)
	x0, y0 := float32(content.Min.X)+cw, float32(content.Min.Y)+ch/2
	cols := content.Dx()/f.CellW - 2
	if cols < 4 || content.Dy() < 3*f.CellH {
		return
	}

	// Header: mode, source, and the clock or the words typed.
	head := "typing test · " + p.mode.String()
	if p.title != "" {
		head += " · from Wikipedia: " + p.title
	}
	right := ""
	if p.test != nil && p.result == nil {
		if p.test.Mode.Kind == "time" {
			right = strconv.Itoa(int(math.Ceil(p.test.Remaining(ctx.Now).Seconds()))) + "s"
		} else {
			right = strconv.Itoa(p.test.Cur) + "/" + strconv.Itoa(len(p.test.Words))
		}
	}
	P.Text(f, x0, y0, gfx.Truncate(head, cols-len(right)-2), th.Dim, 1)
	if right != "" {
		P.Text(f, x0+float32(cols-len(right))*cw, y0, right, th.Accent, 1)
	}

	switch {
	case p.test == nil:
		msg := "fetching a random Wikipedia article…"
		P.Text(f, x0, float32(content.Min.Y+content.Dy()/2), gfx.Truncate(msg, cols), th.Dim, 1)
	case p.result != nil:
		p.drawResult(ctx, content)
	default:
		p.drawText(ctx, content)
	}
}

func (p *TypingPane) drawText(ctx *Ctx, content image.Rectangle) {
	f, th, P := ctx.Term, ctx.Theme, ctx.P
	s := typingScale(f, content)
	big := p.fontAt(ctx, s)
	cw, ch := float32(big.CellW), float32(big.CellH)
	cols := max(8, int(float32(content.Dx())/cw)-2)
	lines := p.wordLines(cols)
	cur := 0
	for i, l := range lines {
		if p.test.Cur >= l[0] && p.test.Cur < l[1] || p.test.Cur >= l[1] && i == len(lines)-1 {
			cur = i
		}
	}
	top := max(0, cur-1)
	x0 := float32(content.Min.X) + cw
	y0 := float32(content.Min.Y+content.Dy()/2) - ch*textLines/2
	for row := 0; row < textLines && top+row < len(lines); row++ {
		y := y0 + ch*float32(row)
		x := x0
		for i := lines[top+row][0]; i < lines[top+row][1]; i++ {
			word, typed := p.test.Words[i], []rune(p.test.Typed[i])
			for j := 0; j < max(len(word), len(typed)); j++ {
				var r rune
				c, alpha := th.Dim, float32(1)
				switch {
				case j >= len(typed):
					r = rune(word[j])
				case j >= len(word):
					r, c, alpha = typed[j], th.Warn, 0.6
				case typed[j] == rune(word[j]):
					r, c = rune(word[j]), th.FG
				default:
					r, c = rune(word[j]), th.Warn
				}
				if i == p.test.Cur && j == len(typed) {
					p.setCaret(ctx, x, top+row)
				}
				x += drawRunes(P, big, x, y, string(r), c, alpha)
			}
			if i == p.test.Cur && len(typed) >= len(word) {
				p.setCaret(ctx, x, top+row)
			}
			x += cw
		}
	}
	if p.caretRow >= top && p.caretRow < top+textLines {
		y := y0 + ch*float32(p.caretRow-top)
		P.Rect(p.caretX(ctx.Now), y, 2*ctx.Px, ch, th.Accent, 1)
	}
	if !p.test.Started() {
		hint := "tab new text · left/right mode"
		P.Text(f, x0, y0+ch*textLines+float32(f.CellH), gfx.Truncate(hint, cols*s), th.Dim, 0.8)
	}
}

// setCaret records the caret's destination; a new line skips the glide.
func (p *TypingPane) setCaret(ctx *Ctx, x float32, row int) {
	if row != p.caretRow {
		p.caretFrom, p.caretAt = x, time.Time{}
	}
	p.caretTo, p.caretRow = x, row
}

func (p *TypingPane) drawResult(ctx *Ctx, content image.Rectangle) {
	f, th, P := ctx.Term, ctx.Theme, ctx.P
	r := p.result
	s := typingScale(f, content)
	cw, ch := float32(f.CellW), float32(f.CellH)
	cols := content.Dx()/f.CellW - 2
	x0 := float32(content.Min.X) + cw
	y := float32(content.Min.Y) + ch*2

	big := strconv.Itoa(int(math.Round(r.WPM)))
	w := drawRunes(P, p.fontAt(ctx, 2*s), x0, y, big, th.Accent, 1)
	P.Text(f, x0+w+cw, y+ch*float32(2*s-1), "wpm", th.Dim, 1)
	if p.newBest {
		P.Text(f, x0+w+cw*5, y+ch*float32(2*s-1), "new best", th.Accent2, 1)
	}
	y += ch * float32(2*s+1)
	lines := []string{
		fmt.Sprintf("accuracy %.0f%%   raw %.0f   time %.0fs", r.Accuracy, r.Raw, r.Seconds),
		fmt.Sprintf("characters %d correct / %d incorrect / %d extra / %d missed", r.Correct, r.Incorrect, r.Extra, r.Missed),
	}
	for _, l := range lines {
		P.Text(f, x0, y, gfx.Truncate(l, cols), th.FG, 1)
		y += ch
	}

	// The speed curve, in blocks: raw speed dimmed behind, the speed in
	// front, a mark on the seconds with errors.
	hint := "tab or enter: next test"
	bottom := float32(content.Max.Y) - ch*2
	height := bottom - y - ch
	if n := len(r.Samples); n > 0 && height > ch*2 {
		top := 1.0
		for _, smp := range r.Samples {
			top = max(top, smp.WPM, smp.Raw)
		}
		bw := min(float32(cols)*cw/float32(n), cw*2)
		block := max(ctx.Px*3, float32(math.Floor(float64(bw/2))))
		quant := func(v float64) float32 {
			h := float32(v/top) * height
			return float32(math.Floor(float64(h/block))) * block
		}
		for i, smp := range r.Samples {
			x := x0 + float32(i)*bw
			if h := quant(smp.Raw); h > 0 {
				P.Rect(x, bottom-h, bw-ctx.Px, h, th.Dim, 0.35)
			}
			if h := quant(smp.WPM); h > 0 {
				P.Rect(x, bottom-h, bw-ctx.Px, h, th.Accent, 0.85)
			}
			if smp.Errors > 0 {
				P.Rect(x, bottom-height, bw-ctx.Px, block, th.Warn, 1)
			}
		}
	}
	P.Text(f, x0, float32(content.Max.Y)-ch*1.5, gfx.Truncate(hint, cols), th.Dim, 0.8)
}
