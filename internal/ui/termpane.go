package ui

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

const blinkHalf = 530 * time.Millisecond

type selPos struct {
	row int64
	col int
}

func (a selPos) less(b selPos) bool { return a.row < b.row || a.row == b.row && a.col < b.col }

type selection struct {
	active bool
	a, b   selPos
	lines  bool // selection by whole lines
	cell   bool // a == b still selects that cell (copy mode), unlike a click
}

func (s selection) ordered() (selPos, selPos) {
	if s.b.less(s.a) {
		return s.b, s.a
	}
	return s.a, s.b
}

func (s selection) empty() bool { return !s.active || (!s.lines && !s.cell && s.a == s.b) }

// copyMode is vim-style navigation through the scrollback.
type copyMode struct {
	pos      selPos
	visual   int // 0 none, 1 characters (v), 2 lines (V)
	anchor   selPos
	pendingG bool
	object   rune    // 'i' or 'a' typed in visual mode, waiting for the object
	count    int     // count typed before a motion or an object (3j, v2iw)
	find     rune    // f F t or T typed, waiting for the character
	lastFind [2]rune // the last f F t T and its character, for ; and ,
	grow     bool    // the object being applied grows the selection

	// Search: / and ? type a pattern, n and N repeat it.
	prompt rune   // '/' or '?' while the pattern is typed, otherwise 0
	input  []rune // pattern being typed
	pat    []rune // last pattern searched
	back   bool   // the last search went up (?)
	word   bool   // the pattern comes from * or #: whole words, any case
	miss   bool   // the last search found nothing

	// Matches on the visible rows, recomputed when the view, the output or
	// the pattern changes.
	hits   []searchHit
	hitsAt hitsKey
	hitsOK bool
	rs     []rune // buffers for match
	xs     []int
	ms     [][2]int
}

type hitsKey struct {
	top  int64
	seq  uint64
	cols int
}

// searchHit is a match of the pattern on a visible line: columns [x0, x1).
type searchHit struct {
	row    int64
	x0, x1 int
}

// TermPane is a terminal in a pane.
type TermPane struct {
	paneBase
	argv      []string
	dir       string
	env       []string
	key       string // stable session id (persistence)
	ephemeral bool   // spare shell, not adopted yet
	// onEvent, when set, sees the program's events first (bell,
	// notifications, OSC 7777); true means handled. The vim kata reads its
	// harness through it (katapane.go).
	onEvent      func(ctx *Ctx, ev vt.Event) bool
	restoreTried bool      // restore from disk already attempted
	hideUntil    time.Time // content hidden right after adoption
	addedAt      time.Time // added to the layout (NEONMUX_TRACE trace)
	traced       bool
	quiet        atomic.Bool // closed: its output no longer wakes the renderer (reader goroutine)
	attach       bool        // reattach to an existing daemon session
	s            termBackend // local session or daemon session
	t            *vt.Terminal
	startEr      error

	r       termRenderer
	clip    subImage  // destination clipped to the content area
	labels  termTexts // header texts kept from one frame to the next
	cols    int
	rows    int
	content image.Rectangle
	origin  image.Point

	lastSeq   uint64
	dirty     bool
	offset    int
	lastTotal int64

	// Read at most once per frame, and only when the program's output has
	// changed: all of this needs the terminal lock.
	readSeq  uint64 // Seq at the last read
	syncing  bool   // synchronized update in progress (mode 2026)
	oscTitle string // title set by OSC 0/2
	titleKey [3]string
	title    string // Title() for titleKey: program, command, OSC title

	sel      selection
	cm       *copyMode
	textBuf  []vt.Cell
	dragging bool
	mouseBtn int
	lastCell image.Point
	wheelAcc float64

	prog      vt.ProgStatus // status announced by the program (OSC 7501, agent.go)
	hasProg   bool
	progSeen  bool      // done or failed, already seen
	progGone  bool      // the program has handed control back to the shell
	seenGen   uint64    // Gen already seen before a window restart (layout.json)
	badgeText string    // header badge at the last render
	badgeAt   time.Time // next badge change (its duration)
	fgCwd     string    // directory of the foreground program
	git       gitInfo

	lastOut time.Time
	epoch   time.Time
	blinkOn bool
	fg      string
	cwd     string
	procAt  time.Time
	cmdName string
	flashAt time.Time
}

// NewTermPane prepares a terminal. The process starts at the first Layout,
// once the grid size is known.
func NewTermPane(id int, argv []string, dir string, env []string) *TermPane {
	// epoch is the reference for blinking: a zero time would overflow
	// time.Duration and throw off the next wakeup.
	p := &TermPane{argv: argv, dir: dir, env: env, mouseBtn: -1, key: newKey(), epoch: time.Now()}
	p.id = id
	p.cmdName = filepath.Base(shell.DefaultShell())
	if len(argv) > 0 {
		p.cmdName = filepath.Base(argv[0])
	}
	return p
}

func (p *TermPane) Kind() string { return "term" }

// Title is asked for several times per frame (header, status bar): it is
// only rebuilt when the program, the command or the OSC title read by
// Update have changed.
func (p *TermPane) Title() string {
	osc := p.oscTitle
	if p.s == nil {
		osc = ""
	}
	if key := [3]string{p.fg, p.cmdName, osc}; key != p.titleKey {
		p.titleKey, p.title = key, composeTitle(key[0], key[1], key[2])
	}
	return p.title
}

func composeTitle(fg, cmdName, osc string) string {
	name := fg
	if name == "" {
		name = cmdName
	}
	name = strings.TrimPrefix(name, "-")
	// The OSC title is only added when it brings something other than a path,
	// which is already shown as the subtitle.
	if t := cleanTitle(osc); t != "" && t != name && !strings.EqualFold(t, "claude code") && !strings.ContainsAny(t, "/~") {
		return name + " · " + t
	}
	return name
}

// Subtitle returns the shell's current directory, shown discreetly in the header.
func (p *TermPane) Subtitle() string {
	d := p.cwd
	if d == "" {
		d = p.dir
	}
	if d == "" {
		return ""
	}
	return p.labels.sub.get(d, func() string { return shortPath(d) })
}

func (p *TermPane) Info() string {
	lost := p.daemonLost()
	return p.labels.info.get(infoState{p.cols, p.rows, p.offset, lost}, func() string {
		s := fmt.Sprintf("%d×%d", p.cols, p.rows)
		if p.offset > 0 {
			s = fmt.Sprintf("↑%d  %s", p.offset, s)
		}
		if lost {
			s = "offline  " + s // frozen while waiting for the daemon
		}
		return s
	})
}

type infoState struct {
	cols, rows, offset int
	lost               bool
}

// termTexts keeps a terminal's header texts as long as what they show
// does not change (see memo).
type termTexts struct {
	sub   memo[string, string]
	info  memo[infoState, string]
	badge memo[badgeKey, badgeText]
	stat  memo[statKey, []textPart]
}

type badgeKey struct {
	th   *theme.Theme
	prog vt.ProgStatus
	seen bool
	dur  int64 // the age as fmtDur shows it: seconds, then minutes
}

type badgeText struct {
	icon, text string
	c          color.RGBA
}

type statKey struct {
	st gitStat
	th *theme.Theme
}

// badge returns progBadge, rebuilt only when the agent state, the theme
// or the displayed duration change.
func (p *TermPane) badge(th *theme.Theme, now time.Time) (icon, text string, c color.RGBA) {
	d := now.Sub(p.prog.Since)
	dur := int64(-1)
	switch {
	case d >= time.Minute:
		dur = 1<<40 + int64(d/time.Minute)
	case d >= 0:
		dur = int64(d / time.Second)
	}
	v := p.labels.badge.get(badgeKey{th, p.prog, p.progSeen, dur}, func() badgeText {
		icon, text, c := p.progBadge(th, now)
		return badgeText{icon, text, c}
	})
	return v.icon, v.text, v.c
}

// statParts returns the git counter of the header, rebuilt only when it
// changes.
func (p *TermPane) statParts(th *theme.Theme) []textPart {
	st := p.git.gitStat
	return p.labels.stat.get(statKey{st, th}, func() []textPart { return st.statParts(th) })
}

func (p *TermPane) Mode() string {
	if p.cm == nil {
		return ""
	}
	if p.cm.prompt != 0 {
		return string(p.cm.prompt) + string(p.cm.input)
	}
	if p.cm.miss {
		return "NOT FOUND"
	}
	switch p.cm.visual {
	case 1:
		return "VISUAL"
	case 2:
		return "V-LINE"
	}
	return "COPY"
}

// NewTermPaneAttach reattaches to a daemon session, or restores it from
// disk if it no longer exists (after a restart).
func NewTermPaneAttach(id int, key, dir string, env []string) *TermPane {
	p := NewTermPane(id, nil, dir, env)
	p.key, p.attach = key, true
	return p
}

func (p *TermPane) Layout(ctx *Ctx, content image.Rectangle) {
	f := ctx.Term
	cols := max(2, content.Dx()/f.CellW)
	rows := max(1, content.Dy()/f.CellH)
	p.content = content
	p.origin = content.Min
	p.dirty = true
	if p.s == nil && p.startEr == nil {
		s, err := ctx.App.startSession(p, cols, rows, false)
		if err != nil {
			p.startEr = err
			ctx.App.toast("failed to start: "+err.Error(), ctx.Theme.Warn)
			return
		}
		p.s, p.t = s, s.Terminal()
		p.applyColors(ctx.Theme)
		p.cols, p.rows = cols, rows
		p.t.SetPixelSize(cols*f.CellW, rows*f.CellH)
		return
	}
	if p.s != nil && (cols != p.cols || rows != p.rows) {
		p.cols, p.rows = cols, rows
		p.s.Resize(cols, rows, cols*f.CellW, rows*f.CellH)
	}
}

func (p *TermPane) applyColors(th *theme.Theme) {
	if p.s == nil {
		return
	}
	p.t.SetReportColors(theme.RGB(th.FG), theme.RGB(th.BG), theme.RGB(th.Cursor), func(i int) (uint8, uint8, uint8) {
		c := th.Palette(uint8(i))
		return c.R, c.G, c.B
	})
}

func (p *TermPane) Update(ctx *Ctx) bool {
	if p.s == nil {
		return false
	}
	changed := false
	if rm, ok := p.s.(remoteBackend); ok {
		// Session unknown to the daemon (the Mac restarted): recreate it by
		// reloading the saved scrollback.
		// After a reboot, or a restarted daemon that no longer knows the
		// session (crash): a single attempt to restore from disk.
		if rm.Missing() && !p.restoreTried {
			p.restoreTried = true
			p.attach = false
			if s, err := ctx.App.startSession(p, p.cols, p.rows, true); err == nil {
				p.useSession(s)
			}
			return true
		}
		if msg := rm.TakePending(); msg != "" {
			ctx.App.notifyPane(p, "", msg)
		}
	}
	// Events, title and synchronized update are only read again when the
	// output has changed (or to see a synchronized block end). Seq is read
	// first: an event that arrives during the read will change it again.
	if seq := p.t.Seq(); seq != p.readSeq || p.syncing {
		p.readSeq = seq
		for _, ev := range p.t.TakeEvents() {
			p.handleEvent(ctx, ev)
			changed = true
		}
		p.syncing = p.t.Synchronizing()
		if title := p.t.Title(); title != p.oscTitle {
			p.oscTitle = title
			changed = true
		}
	}
	progChanged := false
	if seq := p.t.Seq(); seq != p.lastSeq && !p.syncing {
		p.lastSeq = seq
		progChanged = p.trackProg(ctx) // a status only arrives through the program's output
		p.dirty = true
		p.lastOut = ctx.Now
		if ctx.App.focused() != Pane(p) {
			p.activity = true
		}
		p.stabilizeView()
	}
	if ctx.Now.Sub(p.procAt) > time.Second {
		p.procAt = ctx.Now
		if name := p.s.ForegroundName(); name != p.fg {
			// The program that announced work in progress handed control back
			// to the shell without clearing it: that status goes away with it.
			if shell.LeftForShell(p.fg, name) {
				p.progGone = true
				p.t.DropStatus(vt.PSWorking, vt.PSBlocked, vt.PSIdle)
				progChanged = p.trackProg(ctx) || progChanged
			} else if !shell.IsShell(name) {
				p.progGone = false
			}
			p.fg = name
			changed = true
		}
		// An already seen result from a program that exited goes away too.
		if p.progGone && p.progSeen && p.hasProg {
			p.t.DropStatus(vt.PSDone, vt.PSError)
			progChanged = p.trackProg(ctx) || progChanged
		}
		if d := p.s.ForegroundCwd(); d != p.fgCwd {
			p.fgCwd = d
			if p.hasProg {
				ctx.App.refreshGit(p) // the agent changed directory (worktree)
			}
		}
		if d := p.s.Cwd(); d != "" && d != p.cwd {
			p.cwd = d
			changed = true
			if p.hasProg {
				ctx.App.refreshGit(p) // the counter follows the agent's directory
			}
		}
	}
	if traceReady && !p.traced && !p.addedAt.IsZero() {
		p.traceReady(ctx.Now)
	}
	if on := p.cursorOn(ctx); on != p.blinkOn {
		p.blinkOn = on
		changed = true
	}
	if progChanged {
		changed = true
	}
	p.gitWhileWorking(ctx)
	// The badge text (its duration) only changes at known times.
	if progChanged || !ctx.Now.Before(p.badgeAt) {
		p.badgeAt = p.progWake(ctx.Now)
		if p.badgeAt.IsZero() {
			p.badgeAt = ctx.Now.Add(time.Hour)
		}
		if icon, b, _ := p.progBadge(ctx.Theme, ctx.Now); icon+b != p.badgeText {
			p.badgeText = icon + b
			changed = true
		}
	}
	return p.dirty || changed
}

// useSession plugs the pane into a new session (restore, daemon coming
// back): everything read from the old terminal must be read again, and
// the texture redone. Lines no longer have the same numbers there: the
// view goes back to the bottom, with no selection and no copy mode.
func (p *TermPane) useSession(s termBackend) {
	p.s, p.t = s, s.Terminal()
	p.readSeq, p.lastSeq = ^uint64(0), ^uint64(0) // never reached by a new terminal
	p.syncing, p.oscTitle, p.lastTotal = false, "", 0
	p.offset, p.sel, p.cm = 0, selection{}, nil
	p.r.dispose()
	p.dirty = true
}

// stabilizeView keeps the view still while reading the scrollback as
// new lines come in.
func (p *TermPane) stabilizeView() {
	p.t.Lock()
	_, screen := p.t.AbsBase()
	hist := p.t.HistoryLen()
	p.t.Unlock()
	if p.offset > 0 && screen > p.lastTotal {
		p.offset += int(screen - p.lastTotal)
	}
	p.offset = min(p.offset, hist)
	p.lastTotal = screen
}

func (p *TermPane) blinking(ctx *Ctx) bool {
	if !ctx.App.cfg.CursorBlink || ctx.App.focused() != Pane(p) || p.cm != nil {
		return false
	}
	idle := ctx.Now.Sub(ctx.App.lastInput)
	if out := ctx.Now.Sub(p.lastOut); out < idle {
		idle = out
	}
	// After 15 s of inactivity the cursor freezes: no more wakeups.
	return idle < 15*time.Second
}

func (p *TermPane) cursorOn(ctx *Ctx) bool {
	if !p.blinking(ctx) {
		return true
	}
	return (ctx.Now.Sub(p.epoch)/blinkHalf)%2 == 0
}

func (p *TermPane) NextWake(ctx *Ctx) time.Time {
	if ctx.Now.Before(p.hideUntil) {
		return p.hideUntil
	}
	if p.s != nil && p.syncing {
		return ctx.Now.Add(160 * time.Millisecond)
	}
	prog := p.progWake(ctx.Now)
	if !p.blinking(ctx) {
		return prog
	}
	k := ctx.Now.Sub(p.epoch)/blinkHalf + 1
	if blink := p.epoch.Add(k * blinkHalf); prog.IsZero() || blink.Before(prog) {
		return blink
	}
	return prog
}

func (p *TermPane) AnimFPS(ctx *Ctx) int { return 0 }

func (p *TermPane) handleEvent(ctx *Ctx, ev vt.Event) {
	if p.onEvent != nil && p.onEvent(ctx, ev) {
		return
	}
	switch ev.Kind {
	case vt.EvBell:
		p.flashAt = ctx.Now
		ctx.App.notifyPane(p, "", "bell")
	case vt.EvNotify:
		ctx.App.notifyPane(p, ev.Title, ev.Body)
	case vt.EvClipboard:
		// Only the active, visible pane writes to the clipboard: a program that
		// is hidden, in the garage, masked by zen mode or without focus does not
		// replace it behind the user's back. vt caps the size.
		if a := ctx.App; a.focused() == Pane(p) && !p.hidden && !p.parked && !a.zen {
			copyOSC52(ev.Body)
			a.toast("clipboard updated (OSC 52)", ctx.Theme.Accent2)
		}
	case vt.EvFX:
		ctx.App.runFX(p, ev.Body)
	}
}

// copyOSC52 writes to the clipboard for OSC 52 (replaced in the tests,
// which must not touch the real one).
var copyOSC52 = CopyText

func (p *TermPane) write(b []byte) {
	if p.s != nil && len(b) > 0 {
		p.s.Write(b)
	}
}

func (p *TermPane) resetBlink(now time.Time) {
	p.epoch = now
	p.blinkOn = true
}

func (p *TermPane) Key(ctx *Ctx, ev KeyEvent) bool {
	if p.s == nil {
		return false
	}
	if p.cm != nil {
		return p.copyKey(ctx, ev)
	}
	b := EncodeKey(ev, p.t.Modes(), ctx.App.cfg.OptionAsMeta)
	if p.isClaude() && p.t.Modes()&vt.ModeKittyKeys == 0 {
		if cb := claudeKey(ev); cb != nil {
			b = cb
		}
	}
	if b == nil {
		return false
	}
	p.toBottom()
	p.resetBlink(ctx.Now)
	ctx.App.armEcho()
	p.write(b)
	return true
}

// isClaude tells whether Claude Code is in the foreground, from the
// process name or the app announced by OSC 7501.
func (p *TermPane) isClaude() bool {
	return p.fg == "claude" || p.hasProg && p.prog.App == "claude-code"
}

// claudeKey returns the Claude Code specific encoding of a key, or nil.
// There shift+enter inserts a newline: ESC CR, the way Claude's
// /terminal-setup configures it in iTerm2 and VS Code. Claude gets it no
// other way, since without the kitty keyboard protocol it stays on the
// classic encoding. Other programs keep a plain CR: in zsh, ESC CR would
// insert a line break instead of running the command.
func claudeKey(ev KeyEvent) []byte {
	if ev.Name == "enter" && ev.Mods == ModShift {
		return []byte("\x1b\r")
	}
	return nil
}

func (p *TermPane) Chars(ctx *Ctx, rs []rune) {
	if p.s == nil {
		return
	}
	if p.cm != nil {
		for _, r := range rs {
			p.copyChar(ctx, r)
		}
		return
	}
	p.toBottom()
	p.resetBlink(ctx.Now)
	ctx.App.armEcho()
	p.write(EncodeChars(rs, ctx.Mods, ctx.App.cfg.OptionAsMeta))
	if ctx.App.cfg.TypingFX {
		if x, y, ok := p.cursorPixel(); ok {
			ctx.App.sparks(float32(x), float32(y))
		}
	}
}

// Paste pastes text, as a bracketed paste if the program asked for it.
func (p *TermPane) Paste(s string) {
	if p.s == nil || s == "" {
		return
	}
	s = SanitizePaste(s)
	if p.t.Modes()&vt.ModeBracketedPaste != 0 {
		s = "\x1b[200~" + s + "\x1b[201~"
	}
	p.toBottom()
	p.write([]byte(s))
}

func (p *TermPane) toBottom() {
	if p.offset != 0 {
		p.offset = 0
		p.dirty = true
	}
	if p.cm == nil && p.sel.active && !p.dragging {
		p.sel.active = false
		p.dirty = true
	}
}

func (p *TermPane) scrollBy(n int) {
	p.t.Lock()
	hist := p.t.HistoryLen()
	p.t.Unlock()
	off := min(max(p.offset+n, 0), hist)
	if off != p.offset {
		p.offset = off
		p.dirty = true
	}
}

func (p *TermPane) cellAt(x, y int) (image.Point, bool) {
	if p.s == nil || p.t == nil {
		return image.Point{}, false
	}
	cols, rows := p.gridSize()
	cw := p.r.cellW()
	ch := p.r.cellH()
	if cw <= 0 || ch <= 0 {
		return image.Point{}, false
	}
	if x < p.origin.X || y < p.origin.Y {
		return image.Point{}, false
	}
	if !p.content.Empty() && (x >= p.content.Max.X || y >= p.content.Max.Y) {
		return image.Point{}, false
	}
	col := (x - p.origin.X) / cw
	row := (y - p.origin.Y) / ch
	if col >= cols || row >= rows {
		return image.Point{}, false
	}
	return image.Pt(col, row), true
}

func (p *TermPane) gridSize() (cols, rows int) {
	if p.r.gridCols > 0 && p.r.gridRows > 0 {
		return p.r.gridCols, p.r.gridRows
	}
	if p.t != nil {
		p.t.Lock()
		cols, rows = p.t.Size()
		p.t.Unlock()
		if cols > 0 && rows > 0 {
			return cols, rows
		}
	}
	return p.cols, p.rows
}

func (p *TermPane) gridCols() int {
	cols, _ := p.gridSize()
	return cols
}

func (p *TermPane) gridRows() int {
	_, rows := p.gridSize()
	return rows
}

func (r *termRenderer) cellW() int {
	if r.font == nil {
		return 8
	}
	return r.font.CellW
}

func (r *termRenderer) cellH() int {
	if r.font == nil {
		return 16
	}
	return r.font.CellH
}

func (p *TermPane) absRow(viewRow int) int64 {
	p.t.Lock()
	_, screen := p.t.AbsBase()
	p.t.Unlock()
	return screen - int64(p.offset) + int64(viewRow)
}

func mouseButtonCode(b ebiten.MouseButton) int {
	switch b {
	case ebiten.MouseButtonMiddle:
		return 1
	case ebiten.MouseButtonRight:
		return 2
	}
	return 0
}

func (p *TermPane) Mouse(ctx *Ctx, ev MouseEvent) bool {
	if p.s == nil {
		return false
	}
	modes := p.t.Modes()
	cell, ok := p.cellAt(ev.X, ev.Y)
	appMouse := modes&vt.ModeMouseMask != 0 && ev.Mods&ModShift == 0 && p.cm == nil
	switch ev.Kind {
	case MouseWheel:
		p.wheelAcc += ev.Wheel
		steps := int(p.wheelAcc)
		p.wheelAcc -= float64(steps)
		if steps == 0 {
			return true
		}
		n, btn := steps, 64
		if steps < 0 {
			n, btn = -steps, 65
		}
		switch {
		case appMouse:
			if !ok {
				return true
			}
			for i := 0; i < n; i++ {
				p.write(EncodeMouse(modes, btn, cell.X, cell.Y, true, false, ev.Mods))
			}
		case modes&vt.ModeAltScreen != 0:
			name := "up"
			if steps < 0 {
				name = "down"
			}
			for i := 0; i < n; i++ {
				p.write(EncodeKey(KeyEvent{Name: name}, modes, false))
			}
		default:
			p.scrollBy(steps * 3)
		}
	case MousePress:
		if appMouse {
			if !ok {
				return true
			}
			p.mouseBtn = mouseButtonCode(ev.Button)
			p.lastCell = cell
			p.write(EncodeMouse(modes, p.mouseBtn, cell.X, cell.Y, true, false, ev.Mods))
			return true
		}
		switch ev.Button {
		case ebiten.MouseButtonLeft:
			if !ok {
				return true
			}
			pos := selPos{p.absRow(cell.Y), cell.X}
			p.sel = selection{active: true, a: pos, b: pos, lines: ev.Clicks >= 3}
			if ev.Clicks == 2 {
				p.selectWord(pos)
			}
			p.dragging = true
			p.dirty = true
		case ebiten.MouseButtonMiddle:
			ctx.App.pasteInto(p)
		}
	case MouseMove:
		if appMouse {
			if !ok {
				return true
			}
			if cell != p.lastCell && (modes&vt.ModeMouseAny != 0 || (modes&vt.ModeMouseButton != 0 && p.mouseBtn >= 0)) {
				p.lastCell = cell
				btn := p.mouseBtn
				if btn < 0 {
					btn = 3
				}
				p.write(EncodeMouse(modes, btn, cell.X, cell.Y, true, true, ev.Mods))
			}
			return true
		}
		if p.dragging {
			cols, rows := p.gridSize()
			cw, ch := p.r.cellW(), p.r.cellH()
			// Dragging outside the grid scrolls through the history.
			if ev.Y < p.origin.Y {
				p.scrollBy(1)
			} else if ev.Y >= p.origin.Y+rows*ch {
				p.scrollBy(-1)
			}
			dragCol := min(max((ev.X-p.origin.X)/cw, 0), cols-1)
			dragRow := min(max((ev.Y-p.origin.Y)/ch, 0), rows-1)
			np := selPos{p.absRow(dragRow), dragCol}
			if np != p.sel.b {
				p.sel.b = np
				p.dirty = true
			}
		}
	case MouseRelease:
		if appMouse {
			if modes&vt.ModeMouseX10 == 0 && ok {
				p.write(EncodeMouse(modes, p.mouseBtn, cell.X, cell.Y, false, false, ev.Mods))
			}
			p.mouseBtn = -1
			return true
		}
		if p.dragging {
			p.dragging = false
			if p.sel.empty() {
				p.sel.active = false
			} else if ctx.App.cfg.CopyOnSelect {
				ctx.App.copyNotify(p.SelectedText(), "")
			}
			p.dirty = true
		}
	}
	return true
}

// textLine reads a line by absolute index, reusing p.textBuf. The lock
// must be held. A scrollback line is compressed in vt: AbsLine always
// decodes it into the buffer (grown as needed), which is kept for the
// next one. A screen line is returned without a copy: it must never
// become the buffer, which a later decode would overwrite.
func (p *TermPane) textLine(row int64) ([]vt.Cell, bool) {
	line, wrapped := p.t.AbsLine(row, p.textBuf)
	if _, screen := p.t.AbsBase(); row < screen {
		p.textBuf = line
	}
	return line, wrapped
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_-./~:@%+=#", r)
}

func (p *TermPane) selectWord(pos selPos) {
	p.t.Lock()
	line, _ := p.textLine(pos.row)
	runeAt := func(x int) rune {
		if x < 0 || x >= len(line) {
			return ' '
		}
		return line[x].Rune()
	}
	a, b := pos.col, pos.col
	if isWordRune(runeAt(a)) {
		for a > 0 && isWordRune(runeAt(a-1)) {
			a--
		}
		for b < len(line)-1 && isWordRune(runeAt(b+1)) {
			b++
		}
	}
	p.t.Unlock()
	p.sel.a, p.sel.b = selPos{pos.row, a}, selPos{pos.row, b}
}

// SelectedText returns the selected text.
func (p *TermPane) SelectedText() string {
	if p.s == nil || p.sel.empty() {
		return ""
	}
	a, b := p.sel.ordered()
	t := p.t
	t.Lock()
	defer t.Unlock()
	var sb strings.Builder
	for row := a.row; row <= b.row; row++ {
		line, wrapped := p.textLine(row)
		x0, x1 := 0, len(line)
		if !p.sel.lines {
			if row == a.row {
				x0 = min(a.col, len(line))
			}
			if row == b.row {
				x1 = min(b.col+1, len(line))
			}
		}
		var lb strings.Builder
		for _, c := range line[x0:x1] {
			if c.Attr()&vt.AttrWideCont != 0 {
				continue
			}
			r := c.Rune()
			if r == 0 {
				r = ' '
			}
			lb.WriteRune(r)
		}
		sb.WriteString(strings.TrimRight(lb.String(), " "))
		if row != b.row && (!wrapped || p.sel.lines) {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// EnterCopyMode starts vim-style navigation through the scrollback.
func (p *TermPane) EnterCopyMode() {
	if p.s == nil {
		return
	}
	p.t.Lock()
	x, y, _, _ := p.t.Cursor()
	_, screen := p.t.AbsBase()
	p.t.Unlock()
	p.cm = &copyMode{pos: selPos{screen + int64(y) - int64(p.offset), x}}
	if p.offset > 0 {
		p.cm.pos.row = screen - int64(p.offset) + int64(p.gridRows()-1)
	}
	p.dirty = true
}

func (p *TermPane) exitCopyMode() {
	p.cm = nil
	p.sel.active = false
	p.offset = 0
	p.dirty = true
}

func (p *TermPane) copyFollow() {
	t := p.t
	t.Lock()
	first, screen := t.AbsBase()
	hist := t.HistoryLen()
	cols, rows := t.Size()
	t.Unlock()
	c := p.cm
	c.pos.row = min(max(c.pos.row, first), screen+int64(rows)-1)
	c.pos.col = min(max(c.pos.col, 0), cols-1)
	top := screen - int64(p.offset)
	if c.pos.row < top {
		p.offset = int(screen - c.pos.row)
	} else if c.pos.row >= top+int64(rows) {
		p.offset = int(screen - (c.pos.row - int64(rows) + 1))
	}
	p.offset = min(max(p.offset, 0), hist)
	if c.visual > 0 {
		p.sel = selection{active: true, a: c.anchor, b: c.pos, lines: c.visual == 2, cell: true}
	} else {
		p.sel.active = false
	}
	p.dirty = true
}

// searchHits finds the matches of the last pattern on the visible lines
// starting at row top, for the overlay.
func (p *TermPane) searchHits(top int64) {
	p.searchHitsGrid(top, p.gridRows())
}

func (p *TermPane) searchHitsGrid(top int64, rows int) {
	c := p.cm
	c.hits = c.hits[:0]
	if len(c.pat) == 0 {
		return
	}
	p.t.Lock()
	defer p.t.Unlock()
	for y := range int64(rows) {
		line, _ := p.textLine(top + y)
		for _, m := range c.match(line) {
			c.hits = append(c.hits, searchHit{top + y, m[0], m[1]})
		}
	}
}

// copySearch moves the cursor to the next match of the last pattern, up
// for back, wrapping around the scrollback like vim's wrapscan.
func (p *TermPane) copySearch(back bool) {
	c := p.cm
	if len(c.pat) == 0 {
		return
	}
	t := p.t
	t.Lock()
	defer t.Unlock()
	first, screen := t.AbsBase()
	_, rows := t.Size()
	last := screen + int64(rows) - 1
	row := min(max(c.pos.row, first), last)
	for i := int64(0); i <= last-first+1; i++ {
		line, _ := p.textLine(row)
		ms := c.match(line)
		if i == 0 {
			// On the cursor line, only what lies past the cursor.
			ms = slices.DeleteFunc(ms, func(m [2]int) bool {
				return back && m[0] >= c.pos.col || !back && m[0] <= c.pos.col
			})
		}
		if len(ms) > 0 {
			m := ms[0]
			if back {
				m = ms[len(ms)-1]
			}
			c.pos = selPos{row, m[0]}
			return
		}
		if back {
			if row--; row < first {
				row = last
			}
		} else if row++; row > last {
			row = first
		}
	}
	c.miss = true
}

// match returns the cell spans [x0, x1) where the pattern occurs in line,
// ignoring case unless it holds a capital (vim's smartcase). The result
// is only valid until the next call.
func (c *copyMode) match(line []vt.Cell) [][2]int {
	pat := c.pat
	fold := c.word || !slices.ContainsFunc(pat, unicode.IsUpper)
	rs, xs := c.rs[:0], c.xs[:0] // xs: column of each rune, then the end of the line
	for x, cell := range line {
		if cell.Attr()&vt.AttrWideCont != 0 {
			continue
		}
		r := cell.Rune()
		if r == 0 {
			r = ' '
		}
		if fold {
			r = unicode.ToLower(r)
		}
		rs, xs = append(rs, r), append(xs, x)
	}
	xs = append(xs, len(line))
	c.rs, c.xs = rs, xs
	out := c.ms[:0]
	for i := 0; i+len(pat) <= len(rs); i++ {
		ok := true
		for j, r := range pat {
			if fold {
				r = unicode.ToLower(r)
			}
			if rs[i+j] != r {
				ok = false
				break
			}
		}
		if ok && c.word && (i > 0 && isKeyword(rs[i-1]) || i+len(pat) < len(rs) && isKeyword(rs[i+len(pat)])) {
			ok = false
		}
		if ok {
			out = append(out, [2]int{xs[i], xs[i+len(pat)]})
		}
	}
	c.ms = out
	return out
}

func (p *TermPane) copyLine(row int64) []vt.Cell {
	p.t.Lock()
	defer p.t.Unlock()
	line, _ := p.textLine(row)
	return append([]vt.Cell(nil), line...)
}

func (p *TermPane) copyChar(ctx *Ctx, r rune) {
	c := p.cm
	if c == nil {
		return
	}
	if c.prompt != 0 {
		if unicode.IsPrint(r) {
			c.input = append(c.input, r)
			p.dirty = true
		}
		return
	}
	c.miss = false
	if o := c.object; o != 0 {
		c.object = 0
		for i := range max(c.count, 1) {
			// Stop as soon as the object no longer changes anything: a
			// huge count must not walk the scrollback over and over.
			before := [2]selPos{c.anchor, c.pos}
			if p.textObject(r, o == 'a', i > 0); i > 0 && before == [2]selPos{c.anchor, c.pos} {
				break
			}
		}
		c.count = 0
		p.copyFollow()
		return
	}
	if f := c.find; f != 0 {
		c.find, c.lastFind = 0, [2]rune{f, r}
		p.findChar(f, r, max(c.count, 1), false)
		c.count = 0
		p.copyFollow()
		return
	}
	if r >= '1' && r <= '9' || r == '0' && c.count > 0 {
		c.count = min(c.count*10+int(r-'0'), 99999)
		return
	}
	n := max(c.count, 1)
	c.count = 0
	half := int64(max(1, p.gridRows()/2))
	switch r {
	case 'i', 'a':
		if c.visual > 0 {
			// The count waits for the object.
			c.object, c.count, c.pendingG = r, n, false
			return
		}
	case 'f', 'F', 't', 'T':
		// The count waits for the character.
		c.find, c.count, c.pendingG = r, n, false
		return
	case ';', ',':
		p.repeatFind(n, r == ',')
	case '^':
		p.firstNonBlank()
	case '%':
		p.matchPair()
	case '*', '#':
		p.starSearch(r == '#')
		for range n - 1 {
			at := c.pos
			if p.copySearch(r == '#'); c.miss || c.pos == at {
				break
			}
		}
	case '{', '}':
		dir := int64(1)
		if r == '{' {
			dir = -1
		}
		for range n {
			c.pos = selPos{p.paragraphMove(c.pos.row, dir), 0}
		}
	case '/', '?':
		c.prompt, c.input, c.pendingG = r, c.input[:0], false
		p.dirty = true
		return
	case 'n', 'N':
		for range n {
			at := c.pos
			if p.copySearch(c.back != (r == 'N')); c.miss || c.pos == at {
				break
			}
		}
	case 'h':
		c.pos.col -= n
	case 'l':
		c.pos.col += n
	case 'j':
		c.pos.row += int64(n)
	case 'k':
		c.pos.row -= int64(n)
	case '0':
		c.pos.col = 0
	case '$':
		line := p.copyLine(c.pos.row)
		n := len(line)
		for n > 0 && line[n-1].IsBlank() {
			n--
		}
		c.pos.col = max(n-1, 0)
	case 'w', 'b', 'e', 'W', 'B', 'E':
		if c.pendingG && (r == 'e' || r == 'E') {
			r -= 'e' - 'g' // ge gE
		}
		for range n {
			p.copyWord(r)
		}
	case 'g':
		if c.pendingG {
			c.pos.row = -1 << 62
			c.pos.col = 0
			c.pendingG = false
		} else {
			c.pendingG = true
			return
		}
	case 'G':
		c.pos.row = 1 << 62
	case 'H':
		c.pos.row = p.absRow(0)
	case 'M':
		c.pos.row = p.absRow(p.gridRows() / 2)
	case 'L':
		c.pos.row = p.absRow(p.gridRows() - 1)
	case 'v', 'V':
		mode := 1
		if r == 'V' {
			mode = 2
		}
		if c.visual == mode {
			c.visual = 0
		} else {
			if c.visual == 0 {
				c.anchor = c.pos
			}
			c.visual = mode
		}
	case 'y':
		if c.visual == 0 {
			p.sel = selection{active: true, a: c.pos, b: c.pos, lines: true}
		}
		if txt := p.SelectedText(); txt != "" {
			ctx.App.copyNotify(txt, fmt.Sprintf("%d characters copied", len([]rune(txt))))
		}
		p.exitCopyMode()
		return
	case 'q':
		p.exitCopyMode()
		return
	case 'u' - 'a' + 1: // Ctrl-U received as a character on some platforms
		c.pos.row -= half
	}
	c.pendingG = false
	p.copyFollow()
}

// copyRows returns the first and last rows reachable in copy mode. The
// terminal lock must be held.
func (p *TermPane) copyRows() (first, last int64) {
	first, screen := p.t.AbsBase()
	_, rows := p.t.Size()
	return first, screen + int64(rows) - 1
}

// blankRow tells whether a line holds nothing but spaces. The terminal
// lock must be held.
func (p *TermPane) blankRow(row int64) bool {
	line, _ := p.textLine(row)
	for _, c := range line {
		if !c.IsBlank() {
			return false
		}
	}
	return true
}

// paragraphMove is vim's { (dir -1) and } (dir 1): the next blank line
// past the paragraph, or the first or last line.
func (p *TermPane) paragraphMove(row int64, dir int64) int64 {
	p.t.Lock()
	defer p.t.Unlock()
	first, last := p.copyRows()
	row = min(max(row, first), last)
	for p.blankRow(row) && row+dir >= first && row+dir <= last {
		row += dir
	}
	for row+dir >= first && row+dir <= last {
		if row += dir; p.blankRow(row) {
			break
		}
	}
	return row
}

func (p *TermPane) copyKey(ctx *Ctx, ev KeyEvent) bool {
	c := p.cm
	if c.prompt != 0 {
		switch ev.Name {
		case "escape":
			c.prompt = 0
		case "enter":
			// An empty pattern repeats the last one, as in vim.
			if len(c.input) > 0 {
				c.pat = append(c.pat[:0], c.input...)
				c.word, c.hitsOK = false, false
			}
			c.back, c.prompt = c.prompt == '?', 0
			p.copySearch(c.back)
		case "backspace":
			if len(c.input) == 0 {
				c.prompt = 0
			} else {
				c.input = c.input[:len(c.input)-1]
			}
		default:
			return ev.Mods&ModCtrl != 0
		}
		p.copyFollow()
		return true
	}
	// Printable keys also come as characters, through copyChar: leave a
	// pending object or count alone for them.
	switch ev.Name {
	case "escape", "pageup", "pagedown", "up", "down", "left", "right", "enter":
	default:
		if ev.Mods&ModCtrl == 0 {
			return false
		}
	}
	pending := c.object != 0 || c.find != 0
	c.miss, c.object, c.find, c.count = false, 0, 0, 0
	half := int64(max(1, p.gridRows()/2))
	page := int64(max(1, p.gridRows()-1))
	switch {
	case ev.Name == "escape":
		if pending {
			// Escape only drops the pending object or f t.
		} else if c.visual > 0 {
			c.visual = 0
		} else {
			p.exitCopyMode()
			return true
		}
	case ev.Mods&ModCtrl != 0 && ev.Name == "u":
		c.pos.row -= half
	case ev.Mods&ModCtrl != 0 && ev.Name == "d":
		c.pos.row += half
	case ev.Mods&ModCtrl != 0 && ev.Name == "b", ev.Name == "pageup":
		c.pos.row -= page
	case ev.Mods&ModCtrl != 0 && ev.Name == "f", ev.Name == "pagedown":
		c.pos.row += page
	case ev.Name == "up":
		c.pos.row--
	case ev.Name == "down":
		c.pos.row++
	case ev.Name == "left":
		c.pos.col--
	case ev.Name == "right":
		c.pos.col++
	case ev.Name == "enter":
		p.copyChar(ctx, 'y')
		return true
	default:
		return ev.Mods&ModCtrl != 0
	}
	p.copyFollow()
	return true
}

// selSpan returns the selected columns [x0, x1) on the absolute line row,
// clamped to the grid: a selection made at 150 columns does not spill out
// of a terminal shrunk back to 80.
func (p *TermPane) selSpan(row int64) (x0, x1 int) {
	return p.selSpanGrid(row, p.gridCols())
}

func (p *TermPane) selSpanGrid(row int64, cols int) (x0, x1 int) {
	a, b := p.sel.ordered()
	x0, x1 = 0, cols
	if !p.sel.lines {
		if row == a.row {
			x0 = a.col
		}
		if row == b.row {
			x1 = b.col + 1
		}
	}
	x0 = min(max(x0, 0), cols)
	return x0, min(max(x1, x0), cols)
}

// cursorPixel returns the screen position of the cursor (top center).
func (p *TermPane) cursorPixel() (int, int, bool) {
	if p.s == nil || p.r.font == nil {
		return 0, 0, false
	}
	p.t.Lock()
	x, y, vis, _ := p.t.Cursor()
	p.t.Unlock()
	f := p.r.font
	return p.origin.X + x*f.CellW + f.CellW/2, p.origin.Y + y*f.CellH + f.CellH/2, vis
}

func (p *TermPane) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	if p.s == nil {
		if p.startEr != nil {
			ctx.P.Text(ctx.UI, float32(content.Min.X+8), float32(content.Min.Y+8), p.startEr.Error(), ctx.Theme.Warn, 1)
		}
		return
	}
	if ctx.Now.Before(p.hideUntil) {
		return // freshly adopted spare: wait for the clean prompt
	}
	if p.content.Empty() {
		p.content = content
	}
	exact := content == p.content
	if exact {
		p.origin = content.Min
	}
	if p.dirty || p.r.img == nil || p.r.theme != ctx.Theme || p.r.font != ctx.Term || p.r.braille != ctx.App.braille {
		p.r.render(p.t, ctx.Term, ctx.Theme, p.offset, ctx.App.braille)
		p.dirty = false
	}
	img := p.r.img
	if img == nil {
		return
	}
	// Nothing leaves the content area: neither a texture taller than a pane
	// of less than one line (it would cover the header), nor a selection
	// overlay from before a shrink.
	dst = p.clip.of(dst, content)
	op := &ebiten.DrawImageOptions{}
	if exact {
		op.GeoM.Translate(float64(p.origin.X), float64(p.origin.Y))
	} else {
		sx := float64(content.Dx()) / float64(max(1, p.content.Dx()))
		sy := float64(content.Dy()) / float64(max(1, p.content.Dy()))
		op.GeoM.Translate(float64(p.origin.X-p.content.Min.X), float64(p.origin.Y-p.content.Min.Y))
		op.GeoM.Scale(sx, sy)
		op.GeoM.Translate(float64(content.Min.X), float64(content.Min.Y))
		op.Filter = ebiten.FilterLinear
	}
	if g := ctx.Now.Sub(p.glitchAt); g < glitchDur {
		drawGlitch(dst, img, op, ctx, float64(g)/float64(glitchDur))
	} else {
		dst.DrawImage(img, op)
	}
	if !exact {
		return
	}
	p.drawOverlay(ctx, focused)
	ctx.P.Flush(dst)
}

func (p *TermPane) drawOverlay(ctx *Ctx, focused bool) {
	f := ctx.Term
	th := ctx.Theme
	P := ctx.P
	cw, ch := float32(f.CellW), float32(f.CellH)
	ox, oy := float32(p.origin.X), float32(p.origin.Y)

	// The terminal state is the one from the texture's last render, read
	// under its lock: the cursor and the overlay stay consistent with it.
	r := &p.r
	cx, cy, vis, shape, under := r.cx, r.cy, r.cursorVis, r.shape, r.under
	top := r.screen - int64(p.offset)
	cols, rows := r.gridCols, r.gridRows
	if cols <= 0 || rows <= 0 {
		cols, rows = p.gridSize()
	}

	// Selection: a colored overlay on top of the cached texture.
	if !p.sel.empty() {
		a, b := p.sel.ordered()
		for y := 0; y < rows; y++ {
			row := top + int64(y)
			if row < a.row || row > b.row {
				continue
			}
			if x0, x1 := p.selSpanGrid(row, cols); x1 > x0 {
				P.Rect(ox+float32(x0)*cw, oy+float32(y)*ch, float32(x1-x0)*cw, ch, th.Selection, 0.75)
			}
		}
	}

	// Visual bell flash.
	if d := ctx.Now.Sub(p.flashAt); d < 200*time.Millisecond {
		P.Rect(ox, oy, float32(cols)*cw, float32(rows)*ch, th.Accent, 0.18*float32(1-d.Seconds()/0.2))
	}

	if c := p.cm; c != nil {
		if k := (hitsKey{top, p.lastSeq, cols}); !c.hitsOK || c.hitsAt != k {
			p.searchHitsGrid(top, rows)
			c.hitsAt, c.hitsOK = k, true
		}
		for _, h := range c.hits {
			x0, x1 := min(h.x0, cols), min(h.x1, cols)
			if y := h.row - top; y >= 0 && y < int64(rows) && x1 > x0 {
				P.Rect(ox+float32(x0)*cw, oy+float32(y)*ch, float32(x1-x0)*cw, ch, th.Warn, 0.35)
			}
		}
		row := p.cm.pos.row - top
		if row >= 0 && row < int64(rows) && p.cm.pos.col < cols {
			x, y := ox+float32(p.cm.pos.col)*cw, oy+float32(row)*ch
			P.Rect(x, y, cw, ch, th.Accent2, 0.85)
		}
	} else if p.offset == 0 && vis && p.blinkOn && cy < rows && cx < cols {
		x, y := ox+float32(cx)*cw, oy+float32(cy)*ch
		wcell := cw
		if under.Attr()&vt.AttrWide != 0 {
			wcell = 2 * cw
		}
		px := ctx.Px
		switch {
		case !focused:
			P.Border(image.Rect(int(x), int(y), int(x+wcell), int(y+ch)), px, th.Cursor, 0.8)
		case shape == 3 || shape == 4:
			P.Rect(x, y+ch-2*px, wcell, 2*px, th.Cursor, 1)
		case shape == 5 || shape == 6:
			P.Rect(x, y, 2*px, ch, th.Cursor, 1)
			P.Rect(x-px, y, 4*px, ch, th.Cursor, 0.18)
		default:
			// Solid block with a light halo, then the character underneath in reverse.
			P.Rect(x-px, y-px, wcell+2*px, ch+2*px, th.Cursor, 0.22)
			P.Rect(x, y, wcell, ch, th.Cursor, 1)
			if r := under.Rune(); r > ' ' {
				if p.r.braille {
					r = brailleRune(r)
				}
				// Flat fills are drawn before text: the character stays readable.
				P.Glyph(f.Glyph(r, 0, under.Attr()&vt.AttrWide != 0), x, y, th.BG, 1)
			}
		}
	}

	// Scrollbar while scrolled back into the history.
	if p.offset > 0 {
		hist := r.hist
		total := float32(hist + rows)
		h := float32(rows) * ch
		barH := max(h*float32(rows)/total, 6*ctx.Px)
		pos := (float32(hist-p.offset) / total) * h
		P.Rect(ox+float32(cols)*cw-3*ctx.Px, oy+pos, 3*ctx.Px, barH, th.Accent, 0.8)
	}
}

// Close ends the session. Killing the process is the session's job
// (SIGHUP, then SIGKILL); the pane itself stops waking the renderer
// right away.
func (p *TermPane) Close() {
	p.quiet.Store(true)
	if p.s != nil {
		p.s.Close()
	}
	p.r.dispose()
}

func (p *TermPane) Done() bool {
	if p.s == nil {
		return p.startEr != nil
	}
	select {
	case <-p.s.Done():
		return true
	default:
		return false
	}
}

// drawGlitch draws an image cut into shifted strips with a red and cyan
// double image that fades out.
func drawGlitch(dst, img *ebiten.Image, base *ebiten.DrawImageOptions, ctx *Ctx, t float64) {
	b := img.Bounds()
	amp := (1 - t) * float64(ctx.Term.CellW) * 1.5
	seed := int(ctx.Now.UnixMilli() / 40)
	slices := 9
	for i := 0; i < slices; i++ {
		y0 := b.Dy() * i / slices
		y1 := b.Dy() * (i + 1) / slices
		sub := img.SubImage(image.Rect(0, y0, b.Dx(), y1)).(*ebiten.Image)
		h := (seed*31 + i*17) % 7
		dx := float64(h-3) * amp / 3
		for k, tint := range [][3]float32{{1, 0.1, 0.3}, {0.1, 1, 1}, {1, 1, 1}} {
			op := &ebiten.DrawImageOptions{}
			op.GeoM.Translate(dx+float64(k-1)*amp*0.4, float64(y0))
			op.GeoM.Concat(base.GeoM)
			op.Filter = base.Filter
			a := float32(0.55)
			if k == 2 {
				a = 1
			}
			op.ColorScale.Scale(tint[0]*a, tint[1]*a, tint[2]*a, a)
			if k < 2 {
				op.Blend = ebiten.BlendLighter
			}
			dst.DrawImage(sub, op)
		}
	}
}

// traceReady is a measuring aid (NEONMUX_TRACE=1): it prints the delay
// between opening the pane and the output of an "echo READY" command typed
// right away, that is, the moment the shell is really usable.
var traceReady = os.Getenv("NEONMUX_TRACE") != ""

func (p *TermPane) traceReady(now time.Time) {
	p.t.Lock()
	defer p.t.Unlock()
	_, rows := p.t.Size()
	for y := 0; y < rows; y++ {
		var b strings.Builder
		for _, c := range p.t.ScreenLine(y) {
			if r := c.Rune(); r != 0 {
				b.WriteRune(r)
			}
		}
		if strings.TrimSpace(b.String()) == "READY" {
			p.traced = true
			fmt.Fprintf(os.Stderr, "trace: pane %d ready in %d ms\n", p.id, now.Sub(p.addedAt).Milliseconds())
			return
		}
	}
}
