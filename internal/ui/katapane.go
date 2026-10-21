package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/games"
	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/kata"
	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/internal/wiki"
	"github.com/ThiraSoft/neonmux/vt"
)

// Vim kata: missions of vim editing on Wikipedia text, played in the real
// Neovim. The pane draws the menus, the mission card and the end screen;
// in the middle runs nvim --clean with the harness of internal/kata, which
// counts the keys and tells the pane through OSC 7777 when the text is
// right.

type kataScreen int

const (
	kataChapters kataScreen = iota
	kataMissions
	kataLoading
	kataPlaying
	kataEnd
)

const (
	// kataStartTimeout is how long nvim may take to say it is ready
	// (the login shell reads the user's rc files first).
	kataStartTimeout = 20 * time.Second
	// maxBadPar is the number of missions regenerated in a row when the
	// harness finds that the par does not give the target.
	maxBadPar    = 3
	expectedRows = 6
)

var errNoSuitableText = errors.New("no suitable text")

// newKataTerm starts nvim with the harness through the login shell, which
// has the user's PATH (the app launched from the Dock does not). Replaced
// in tests.
var newKataTerm = func(a *App, p *KataPane) *TermPane {
	argv := []string{shell.DefaultShell(), "-l", "-i", "-c", kata.LaunchCommand(p.dir)}
	t := NewTermPane(a.newID(), argv, p.dir, append(a.paneEnv(), "NEONMUX_KATA_DIR="+p.dir))
	t.ephemeral = true // never saved, ends with the window
	return t
}

// showKata opens the vim kata, or goes back to it.
func (a *App) showKata() {
	a.showGame(func(p Pane) bool { _, ok := p.(*KataPane); return ok },
		func(id int) Pane { return NewKataPane(id) })
}

// kataResult is what the end screen shows.
type kataResult struct {
	keys, stars      int
	seq              string
	gaveUp, practice bool
	took, best       time.Duration
	newBest          bool
}

// KataPane is the vim kata.
type KataPane struct {
	paneBase
	screen  kataScreen
	chapter int // chapter of the mission list
	random  bool
	sel     int

	dir      string    // private directory: harness.lua, mission.json
	term     *TermPane // nvim, kept from one mission to the next
	ready    bool      // the harness is running
	startAt  time.Time
	quitting bool // the player quit nvim: it is ending

	def     kata.Def
	mission *kata.Mission
	loaded  bool // the harness has the mission and counts
	keys    int
	clock   games.Clock
	end     *kataResult
	gen     int // missions asked for: older answers are dropped
	badPars int

	content image.Rectangle
	shown   int
	dead    bool // a fatal toast was shown: the pane closes
	closed  bool
}

func NewKataPane(id int) *KataPane {
	p := &KataPane{}
	p.id = id
	return p
}

func (p *KataPane) Kind() string  { return "kata" }
func (p *KataPane) Title() string { return "vim kata" }
func (p *KataPane) Mode() string  { return "" }
func (p *KataPane) Done() bool    { return p.closed || p.dead }

func (p *KataPane) Info() string {
	if p.mission != nil && p.screen >= kataPlaying {
		return p.mission.Name
	}
	return ""
}

func (p *KataPane) Close() {
	p.closed = true
	if p.term != nil {
		p.term.Close()
		p.term = nil
	}
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
		p.dir = ""
	}
}

func (p *KataPane) Mouse(ctx *Ctx, ev MouseEvent) bool { return false }

func (p *KataPane) suspend(now time.Time) { p.clock.Pause(now) }

// fatal ends the kata with a toast; the app closes the pane (Done).
func (p *KataPane) fatal(a *App, msg string) {
	a.toast(msg, a.theme.Warn)
	p.dead = true
}

// Missions

// startMission asks for a text and builds the mission in the background.
func (p *KataPane) startMission(a *App, d kata.Def) {
	p.def, p.screen = d, kataLoading
	p.gen++
	gen, src := p.gen, a.gameSource()
	go func() {
		m, err := buildMission(src, d)
		a.post(func() { p.gotMission(a, gen, m, err) })
	}()
}

// buildMission tries the excerpts of a few articles until one suits d.
func buildMission(src articleSource, d kata.Def) (kata.Mission, error) {
	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	for range 5 {
		art, err := src.Next(context.Background())
		if err != nil {
			return kata.Mission{}, err
		}
		if m, ok := kata.Generate(d, kata.Excerpts(art), rng); ok {
			return m, nil
		}
	}
	return kata.Mission{}, errNoSuitableText
}

// gotMission applies the answer to startMission.
func (p *KataPane) gotMission(a *App, gen int, m kata.Mission, err error) {
	if p.Done() || gen != p.gen || !slices.Contains(a.panes, Pane(p)) {
		return
	}
	if err != nil {
		msg := "No suitable text found, try again"
		if errors.Is(err, wiki.ErrUnreachable) {
			msg = "Wikipedia unreachable"
		}
		a.toast(msg, a.theme.Warn)
		p.back()
		return
	}
	p.begin(a, m)
	a.gameSource().Prefetch()
}

// back returns to the list the mission was chosen from.
func (p *KataPane) back() {
	if p.random || p.chapter == 0 {
		p.screen, p.sel = kataChapters, max(p.chapter-1, 0)
		return
	}
	p.screen = kataMissions
}

// begin hands the mission to nvim, starting it if needed.
func (p *KataPane) begin(a *App, m kata.Mission) {
	p.mission, p.end = &m, nil
	p.loaded, p.keys, p.clock = false, 0, games.Clock{}
	p.screen = kataPlaying
	if p.dir == "" {
		d, err := os.MkdirTemp("", "neonmux-kata-")
		if err == nil {
			err = kata.Prepare(d)
		}
		if err != nil {
			p.fatal(a, "Vim kata: "+err.Error())
			return
		}
		p.dir = d
	}
	if p.term == nil {
		p.term = newKataTerm(a, p)
		p.term.onEvent = p.onEvent
		p.ready, p.quitting, p.startAt = false, false, time.Now()
	}
	p.layoutTerm(a)
	if err := kata.WriteMission(p.dir, m); err != nil {
		p.fatal(a, "Vim kata: "+err.Error())
	}
	a.needRedraw = true
}

// randomDef draws a mission among the open chapters.
func (p *KataPane) randomDef(a *App) kata.Def {
	open := kata.Open(a.gameRecords())
	var defs []kata.Def
	for _, d := range kata.Defs() {
		if open[d.Chapter-1] {
			defs = append(defs, d)
		}
	}
	return defs[rand.IntN(len(defs))]
}

// onEvent reads what nvim sends: the harness's messages. Bells and
// notifications of the inner terminal are dropped.
func (p *KataPane) onEvent(ctx *Ctx, ev vt.Event) bool {
	if ev.Kind != vt.EvFX {
		return ev.Kind == vt.EvBell || ev.Kind == vt.EvNotify
	}
	m, ok := kata.ParseMsg(ev.Body)
	if !ok || p.dead {
		return true
	}
	a := ctx.App
	a.needRedraw = true
	switch m.Kind {
	case "ready":
		p.ready = true
	case "missing":
		p.fatal(a, "Vim kata needs Neovim")
	case "old":
		p.fatal(a, "Vim kata needs Neovim 0.10 or later (found "+m.Version+")")
	case "loaded":
		if p.mission != nil && m.ID == p.mission.ID && p.screen == kataPlaying {
			p.loaded, p.keys, p.clock = true, 0, games.Clock{}
		}
	case "keys":
		if p.loaded {
			p.keys = m.Keys
		}
	case "done", "quit":
		p.quitting = m.Kind == "quit"
		if p.loaded && p.screen == kataPlaying {
			p.finish(a, ctx.Now, m, m.Kind == "quit")
		}
	case "badpar":
		fmt.Fprintf(os.Stderr, "neonmux: kata: the par of %s does not give the target, new mission\n", m.ID)
		if p.badPars++; p.badPars > maxBadPar {
			a.toast("No suitable text found, try again", a.theme.Warn)
			p.badPars = 0
			p.back()
		} else {
			p.startMission(a, p.def)
		}
	}
	return true
}

func (p *KataPane) finish(a *App, now time.Time, m kata.Msg, gaveUp bool) {
	p.clock.Pause(now)
	mi := p.mission
	r := &kataResult{keys: m.Keys, seq: m.Seq, gaveUp: gaveUp, practice: mi.Practice, took: p.clock.Elapsed(now)}
	st := a.gameRecords()
	switch {
	case gaveUp:
	case mi.Practice:
		r.stars = games.Stars(m.Keys, mi.Par.Count())
	default:
		r.stars, r.newBest = st.RecordKata(mi.ID, m.Keys, mi.Par.Count(), r.took)
		_ = st.Save() // without a writable file, records only live in memory
		if r.stars == 3 {
			a.runFX(p, "confetti")
		}
	}
	r.best = time.Duration(st.Kata[mi.ID].BestMillis) * time.Millisecond
	p.end, p.screen, p.loaded, p.badPars = r, kataEnd, false, 0
}

// termEnded handles nvim's exit: a give up (:q), or a start that failed.
func (p *KataPane) termEnded(a *App, now time.Time) {
	ready, quitting := p.ready, p.quitting
	p.term.Close()
	p.term, p.ready, p.quitting = nil, false, false
	switch {
	case p.dead:
	case !ready:
		p.fatal(a, "Vim kata: Neovim failed to start")
	case p.screen == kataPlaying && !quitting:
		// nvim died without saying goodbye: a give up all the same.
		p.finish(a, now, kata.Msg{Keys: p.keys}, true)
	}
}

func (p *KataPane) Update(ctx *Ctx) bool {
	a := ctx.App
	changed := false
	if t := p.term; t != nil {
		changed = t.Update(ctx)
		if p.term != nil && p.term.Done() {
			p.termEnded(a, ctx.Now)
			changed = true
		} else if p.term != nil && !p.ready && ctx.Now.Sub(p.startAt) > kataStartTimeout && !p.dead {
			p.fatal(a, "Vim kata: Neovim failed to start")
		}
	}
	if p.clock.Running() && !a.playing(p) {
		p.clock.Pause(ctx.Now)
	}
	if s := int(p.clock.Elapsed(ctx.Now).Seconds()); s != p.shown {
		p.shown, changed = s, true
	}
	return changed
}

func (p *KataPane) AnimFPS(ctx *Ctx) int { return 0 }

func (p *KataPane) NextWake(ctx *Ctx) time.Time {
	var wake time.Time
	if p.term != nil && p.screen == kataPlaying {
		wake = p.term.NextWake(ctx)
	}
	if p.clock.Running() {
		next := ctx.Now.Add(time.Second - p.clock.Elapsed(ctx.Now)%time.Second)
		if wake.IsZero() || next.Before(wake) {
			wake = next
		}
	}
	return wake
}

// Keys

func (p *KataPane) Key(ctx *Ctx, ev KeyEvent) bool {
	a := ctx.App
	a.needRedraw = true
	if p.screen == kataPlaying {
		if p.term == nil {
			return false
		}
		if p.loaded {
			p.clock.Resume(ctx.Now)
		}
		return p.term.Key(ctx, ev)
	}
	if ev.Mods&^ModShift != 0 {
		return false
	}
	switch p.screen {
	case kataChapters:
		return p.chaptersKey(a, ev.Name)
	case kataMissions:
		return p.missionsKey(a, ev.Name)
	case kataLoading:
		if ev.Name == "escape" {
			p.gen++
			p.back()
			return true
		}
	case kataEnd:
		return p.endKey(a, ev.Name)
	}
	return false
}

func moveSel(sel, n int, name string) (int, bool) {
	switch name {
	case "j", "down":
		return min(sel+1, n-1), true
	case "k", "up":
		return max(sel-1, 0), true
	}
	return sel, false
}

func (p *KataPane) chaptersKey(a *App, name string) bool {
	n := len(kata.Chapters) + 1 // the chapters, then random
	if sel, ok := moveSel(p.sel, n, name); ok {
		p.sel = sel
		return true
	}
	if name != "enter" {
		return false
	}
	if p.sel == len(kata.Chapters) {
		p.random = true
		p.startMission(a, p.randomDef(a))
		return true
	}
	if !kata.Open(a.gameRecords())[p.sel] {
		a.toast(fmt.Sprintf("win 5 missions of chapter %d to open this one", p.sel), a.theme.Dim)
		return true
	}
	p.chapter, p.random, p.screen, p.sel = p.sel+1, false, kataMissions, 0
	return true
}

func (p *KataPane) missionsKey(a *App, name string) bool {
	defs := kata.DefsOf(p.chapter)
	if sel, ok := moveSel(p.sel, len(defs), name); ok {
		p.sel = sel
		return true
	}
	switch name {
	case "enter":
		p.random = false
		p.startMission(a, defs[p.sel])
	case "escape", "q":
		p.screen, p.sel = kataChapters, p.chapter-1
	default:
		return false
	}
	return true
}

func (p *KataPane) endKey(a *App, name string) bool {
	switch name {
	case "enter":
		if p.random {
			p.startMission(a, p.randomDef(a))
		} else {
			p.startMission(a, p.def)
		}
	case "n":
		if p.random {
			p.startMission(a, p.randomDef(a))
			break
		}
		defs := kata.DefsOf(p.def.Chapter)
		i := slices.IndexFunc(defs, func(d kata.Def) bool { return d.ID == p.def.ID })
		p.sel = (i + 1) % len(defs)
		p.startMission(a, defs[p.sel])
	case "r":
		m := *p.mission
		m.Practice = true
		p.begin(a, m)
	case "escape", "q":
		p.back()
	default:
		return false
	}
	return true
}

func (p *KataPane) Chars(ctx *Ctx, rs []rune) {
	if p.screen != kataPlaying || p.term == nil {
		return
	}
	if p.loaded {
		p.clock.Resume(ctx.Now)
	}
	p.term.Chars(ctx, rs)
}

// Layout

func (p *KataPane) Layout(ctx *Ctx, content image.Rectangle) {
	p.content = content
	if p.term != nil {
		p.term.Layout(ctx, p.termRect(ctx, content))
	}
}

func (p *KataPane) layoutTerm(a *App) {
	if p.term == nil || p.content.Empty() || a.termFont == nil {
		return
	}
	lctx := a.newCtx(time.Now(), 0)
	p.term.Layout(&lctx, p.termRect(&lctx, p.content))
}

// termRect is where nvim goes: below the header and the instruction, above
// the expected text of an editing mission.
func (p *KataPane) termRect(ctx *Ctx, content image.Rectangle) image.Rectangle {
	ch, cw := ctx.Term.CellH, ctx.Term.CellW
	top := content.Min.Y + 3*ch
	bottom := content.Max.Y - 2*ch
	if p.mission != nil && p.mission.Goal == nil {
		bottom -= (expectedRows + 2) * ch
	}
	return image.Rect(content.Min.X+cw, top, content.Max.X-cw, max(top+ch*4, bottom))
}

// Drawing

func (p *KataPane) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	f := ctx.Term
	if content.Dx() < f.CellW*12 || content.Dy() < f.CellH*6 {
		return
	}
	switch p.screen {
	case kataChapters:
		p.drawChapters(ctx, content)
	case kataMissions:
		p.drawMissions(ctx, content)
	case kataLoading:
		p.line(ctx, content, 1, "vim kata", ctx.Theme.Accent)
		p.line(ctx, content, 3, "fetching a random Wikipedia article and building the mission…", ctx.Theme.Dim)
	case kataPlaying:
		p.drawPlaying(ctx, dst, content, focused)
	case kataEnd:
		p.drawEnd(ctx, content)
	}
}

// line writes s on row n of the content, truncated to its width.
func (p *KataPane) line(ctx *Ctx, content image.Rectangle, n int, s string, c color.RGBA) float32 {
	f := ctx.Term
	cols := content.Dx()/f.CellW - 2
	return ctx.P.Text(f, float32(content.Min.X+f.CellW), float32(content.Min.Y+n*f.CellH), gfx.Truncate(s, cols), c, 1)
}

// drawStars draws n stars out of 3 as pixel squares at the cell x, y.
func drawStars(ctx *Ctx, x, y float32, n int) float32 {
	f, th := ctx.Term, ctx.Theme
	size := float32(f.CellH) * 0.5
	step := float32(f.CellW) * 1.5
	for i := range 3 {
		sx, sy := x+float32(i)*step, y+(float32(f.CellH)-size)/2
		if i < n {
			ctx.P.Rect(sx, sy, size, size, th.Accent, 1)
		} else {
			ctx.P.Rect(sx, sy, size, size, th.Dim, 0.35)
		}
	}
	return step * 3
}

func fmtSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + " s"
}

func (p *KataPane) drawChapters(ctx *Ctx, content image.Rectangle) {
	th := ctx.Theme
	st := ctx.App.gameRecords()
	open := kata.Open(st)
	p.line(ctx, content, 1, "vim kata", th.Accent)
	p.line(ctx, content, 2, "j/k move · enter open · each mission is a gesture, scored in keys against a par", th.Dim)
	for i, c := range kata.Chapters {
		y := 4 + i
		won, total := 0, 0
		for _, d := range kata.DefsOf(c.N) {
			won += st.Kata[d.ID].Stars
			total += 3
		}
		col := th.FG
		label := fmt.Sprintf("%d. %s", c.N, c.Name)
		if !open[i] {
			col, label = th.Dim, label+"  (locked)"
		}
		p.selRow(ctx, content, y, i == p.sel)
		p.line(ctx, content, y, fmt.Sprintf("%-34s %2d/%d stars", label, won, total), col)
	}
	y := 4 + len(kata.Chapters) + 1
	p.selRow(ctx, content, y, p.sel == len(kata.Chapters))
	p.line(ctx, content, y, "random · missions drawn from the open chapters", th.Accent2)
}

// selRow highlights the selected row.
func (p *KataPane) selRow(ctx *Ctx, content image.Rectangle, n int, on bool) {
	if !on {
		return
	}
	f := ctx.Term
	y := float32(content.Min.Y + n*f.CellH)
	ctx.P.Rect(float32(content.Min.X+f.CellW/2), y, float32(content.Dx()-f.CellW), float32(f.CellH), ctx.Theme.Accent, 0.14)
	ctx.P.Rect(float32(content.Min.X+f.CellW/2), y, 2*ctx.Px, float32(f.CellH), ctx.Theme.Accent, 1)
}

func (p *KataPane) drawMissions(ctx *Ctx, content image.Rectangle) {
	th := ctx.Theme
	f := ctx.Term
	st := ctx.App.gameRecords()
	c := kata.Chapters[p.chapter-1]
	p.line(ctx, content, 1, fmt.Sprintf("%d. %s", c.N, c.Name), th.Accent)
	p.line(ctx, content, 2, "j/k move · enter play · esc back", th.Dim)
	x0 := float32(content.Min.X + f.CellW)
	cw := float32(f.CellW)
	for i, d := range kata.DefsOf(p.chapter) {
		y := 4 + i
		r := st.Kata[d.ID]
		p.selRow(ctx, content, y, i == p.sel)
		p.line(ctx, content, y, d.Name, th.FG)
		yy := float32(content.Min.Y + y*f.CellH)
		x := x0 + cw*38
		x += drawStars(ctx, x, yy, r.Stars) + cw
		x += ctx.P.Text(f, x, yy, fmt.Sprintf("%-8s", d.Gesture), th.Accent2, 1)
		best := "-"
		if r.BestMillis > 0 {
			best = fmtSeconds(time.Duration(r.BestMillis) * time.Millisecond)
		}
		ctx.P.Text(f, x, yy, best, th.Dim, 1)
	}
}

func (p *KataPane) drawPlaying(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	th := ctx.Theme
	m := p.mission
	if m == nil {
		return
	}
	c := kata.Chapters[m.Chapter-1]
	head := fmt.Sprintf("%d. %s · %s · %d keys (par %d) · %d s", c.N, c.Name, m.Name, p.keys, m.Par.Count(), int(p.clock.Elapsed(ctx.Now).Seconds()))
	if m.Practice {
		head += " · practice"
	}
	p.line(ctx, content, 0, head, th.Accent)
	p.line(ctx, content, 1, m.Instruction, th.FG)
	if !p.loaded {
		p.line(ctx, content, 2, "starting nvim…", th.Dim)
	}
	if p.term != nil {
		// The terminal flushes the painter into its own clipped area: what
		// is queued so far must reach the whole pane first.
		ctx.P.Flush(dst)
		p.term.Draw(ctx, dst, p.termRect(ctx, content), focused)
	}
	f := ctx.Term
	rows := content.Dy() / f.CellH
	if m.Goal == nil {
		top := rows - 2 - expectedRows - 1
		p.line(ctx, content, top, "Expected", th.Dim)
		p.drawExpected(ctx, content, top+1)
	}
	p.line(ctx, content, rows-1, "from Wikipedia: "+m.Title+" · :q gives up and shows the solution", th.Dim)
}

// drawExpected shows the lines that change: removed ones, then added ones.
func (p *KataPane) drawExpected(ctx *Ctx, content image.Rectangle, row int) {
	th := ctx.Theme
	m := p.mission
	a, b := m.Start, m.Target
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var lines []string
	var cols []color.RGBA
	for _, s := range a[pre : len(a)-suf] {
		lines, cols = append(lines, "- "+s), append(cols, th.Warn)
	}
	for _, s := range b[pre : len(b)-suf] {
		lines, cols = append(lines, "+ "+s), append(cols, th.Accent)
	}
	if len(lines) > expectedRows {
		lines, cols = append(lines[:expectedRows-1], "  …"), append(cols[:expectedRows-1], th.Dim)
	}
	for i, l := range lines {
		p.line(ctx, content, row+i, l, cols[i])
	}
}

func (p *KataPane) drawEnd(ctx *Ctx, content image.Rectangle) {
	th := ctx.Theme
	f := ctx.Term
	r, m := p.end, p.mission
	if r == nil || m == nil {
		return
	}
	title := "Mission complete"
	switch {
	case r.gaveUp:
		title = "Given up"
	case r.practice:
		title = "Practice done"
	}
	p.line(ctx, content, 1, title+" · "+m.Name, th.Accent)
	if !r.gaveUp {
		drawStars(ctx, float32(content.Min.X+f.CellW), float32(content.Min.Y+2*f.CellH), r.stars)
		line := fmt.Sprintf("%d keys (par %d) · %s", r.keys, m.Par.Count(), fmtSeconds(r.took))
		if r.best > 0 && !r.practice {
			line += " (best " + fmtSeconds(r.best) + ")"
		}
		ctx.P.Text(f, float32(content.Min.X+f.CellW*7), float32(content.Min.Y+2*f.CellH), line, th.FG, 1)
		verdict := ""
		switch par := m.Par.Count(); {
		case r.keys < par:
			verdict = "You beat the par!"
		case r.keys == par:
			verdict = "Par matched."
		}
		if r.newBest && !r.practice {
			verdict = strings.TrimSpace(verdict + " New best time.")
		}
		p.line(ctx, content, 3, verdict, th.Accent2)
	}
	row := 5
	if r.seq != "" {
		p.line(ctx, content, row, "your keys   "+r.seq, th.Dim)
		row++
	}
	p.line(ctx, content, row, "par         "+m.Par.Keys(), th.Accent2)
	row += 2
	for _, s := range m.Par {
		if row >= content.Dy()/f.CellH-2 {
			break
		}
		p.line(ctx, content, row, fmt.Sprintf("  %-14s %s", s.Keys, s.Explain), th.FG)
		row++
	}
	p.line(ctx, content, content.Dy()/f.CellH-1,
		"enter same mission, new text · n next mission · r try this text again · esc back", th.Dim)
}
