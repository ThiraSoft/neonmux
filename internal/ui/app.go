// Package ui assembles the application: panes, layout, input, compositing.
package ui

import (
	"cmp"
	"crypto/sha256"
	"image"
	"image/color"
	"math"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"github.com/ThiraSoft/neonmux/internal/fx"
	"github.com/ThiraSoft/neonmux/internal/games"
	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/server"
	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

// Options holds the launch options (command line).
type Options struct {
	Exec           []string      // command of the first terminal
	Demo           bool          // opens one example of each pane type
	Screenshot     string        // take a screenshot, then exit (tests)
	ScreenshotWait time.Duration // delay before the screenshot
	Script         []string      // keys fed into the keyboard pipeline (tests, demos)
	Persist        bool          // terminals kept by the daemon, layout restored
	Fresh          bool          // ignore the saved layout
	StartDir       string        // directory of the first terminal
	Socket         string
}

type toastMsg struct {
	text  string
	color color.RGBA
	at    time.Time
}

type heldKey struct {
	ev   KeyEvent
	next time.Time
}

// App is the Ebiten game.
type App struct {
	cfg  Config
	opts Options

	theme    *theme.Theme
	termFont *gfx.Font
	uiFont   *gfx.Font
	hintFont *gfx.Font // title screen hints (splashFont), loaded on first use
	dsf      float64
	px       float32
	decorPx  float32 // size of the background's big pixels
	painter  gfx.Painter
	pacer    *fx.Pacer
	shaders  *fx.Shaders
	bg       *fx.Background
	parts    *fx.Particles
	ctx      Ctx

	panes   []Pane
	focus   int
	layout  string
	ratio   float64
	ratio2  float64
	zoomed  bool
	closing []Pane
	nextID  int

	lastTile  Pane          // last active tile, to return to from a floating pane
	lastFloat Pane          // last active floating pane
	zTop      int           // z of the topmost floating pane
	zoomFloat Pane          // floating pane maximized to the whole area (zoomed only applies to tiles)
	order     []Pane        // draw order, reused from one frame to the next
	drag      *floatDrag    // floating pane being moved or resized with the mouse
	prevFocus Pane          // previously active pane (mod+shift+n goes back to it)
	arrived   Pane          // pane that last received the focus: a game it leaves goes to the garage (games.go)
	zen       bool          // panes hidden (zen.go)
	zenAt     time.Time     // start of the last fade
	zenLit    bool          // alert frame shown on the last frame
	fade      *ebiten.Image // fade image, kept while the animation runs

	// Garage (garage.go): the visitor is the pane brought out to the center.
	// Before it comes out, we remember the active pane, its floating position
	// and its PTY size, which it gets back when it returns.
	visitor                  Pane
	visitorBack              Pane
	visitorGeom              floatGeom
	visitorCols, visitorRows int
	parkHinted, visitHinted  bool // garage hint already shown

	w, h       int
	needRedraw bool   // full recomposition of the screen
	damage     []Pane // panes to redraw on their own (cursor, output, animation)
	statusSig  string // status bar content at the last render
	sigBuf     []byte // summary of the current status bar (statusSignature)
	clock      string // status bar clock (clockLabel)
	clockAt    time.Time
	inited     bool

	mode       string // "", "leader", "resize"
	palette    *Palette
	explorer   *Explorer // file explorer laid over the active pane (explorer.go)
	toasts     []toastMsg
	hud        bool
	bindings   map[Combo]string
	leader     Combo
	leaderWait bool // the leader waits for a character (":", "?"…)
	mod        Mods

	keyBuf   []ebiten.Key
	charBuf  []rune
	repDelay time.Duration
	repEvery time.Duration
	held     *heldKey

	echoArmed bool   // a keystroke went to a terminal during this frame
	echoKicks uint64 // pacer.Kicks reading before that keystroke

	lastFrame time.Time
	lastInput time.Time
	start     time.Time
	posted    chan func()
	stats     perfStats

	pressPane  Pane
	lastClick  time.Time
	clickCount int
	clickPos   image.Point
	clickPane  Pane // pane of the last click: a double click never spans two panes
	lastMouse  image.Point
	wasFocused bool
	lit        Pane      // pane whose border lit up last
	litAt      time.Time // when it lit up

	script        []string
	scriptAt      time.Time
	client        *server.Client
	daemonLost    bool // daemon loss already reported (backend.go)
	revive        bool // terminals are waiting for the daemon to come back
	shutdown      bool
	lastTerm      *TermPane
	wikiSrc       articleSource // waiting games: random articles (typingpane.go)
	gameStore     *games.Store  // waiting games: records
	spare         *spareShell   // prewarmed shell (prewarm.go)
	spareAt       time.Time
	cfgHash       [32]byte // config content at the last load or save
	renaming      Pane     // pane whose name is being edited
	renameBuf     []rune
	layoutDirty   bool
	layoutSavedAt time.Time
	fontScan      []fontEntry
	symbols       string
	lastMinute    time.Time
	batt          battery // last battery reading (startBattery)
	battText      string  // its status bar text, empty without a battery
	wasAnimating  bool
	quit          bool
	shotDone      bool
}

// New creates the application.
func New(cfg Config, opts Options) (*App, error) {
	sh, err := fx.LoadShaders()
	if err != nil {
		return nil, err
	}
	a := &App{
		cfg:     cfg,
		opts:    opts,
		theme:   theme.Get(cfg.Theme),
		layout:  "tiles",
		ratio:   0.5,
		ratio2:  0.5,
		pacer:   fx.NewPacer(maxFPSFor(cfg.Power)),
		shaders: sh,
		parts:   fx.NewParticles(),
		posted:  make(chan func(), 64),
		start:   time.Now(),
	}
	a.bg = fx.NewBackground(sh)
	a.applyBackground()
	a.mod = ParseMods(cfg.Mod)
	a.buildBindings()
	a.repDelay, a.repEvery = 300*time.Millisecond, 33*time.Millisecond
	go func() {
		d, e := keyRepeat()
		a.post(func() { a.repDelay, a.repEvery = d, e })
	}()
	gfx.RuneCells = vt.RuneWidth
	if opts.Persist {
		a.connectServer()
	}
	if data, err := os.ReadFile(ConfigPath()); err == nil {
		a.cfgHash = sha256.Sum256(data)
	}
	a.watchConfig()
	muted = opts.Screenshot != "" || os.Getenv("NEONMUX_MUTE") != ""
	return a, nil
}

func maxFPSFor(power string) int {
	switch power {
	case "eco":
		return 30
	case "max":
		return 120
	}
	return 60
}

func (a *App) applyBackground() {
	kind := a.cfg.Background
	if kind == "" || kind == "auto" {
		kind = a.theme.Background
	}
	a.bg.Kind = kind
	a.needRedraw = true
}

// post runs fn on the main thread at the next frame.
func (a *App) post(fn func()) {
	a.posted <- fn
	a.pacer.Kick()
}

// Post is the exported version of post (IPC).
func (a *App) Post(fn func()) { a.post(fn) }

func (a *App) focused() Pane {
	if a.focus >= 0 && a.focus < len(a.panes) {
		return a.panes[a.focus]
	}
	return nil
}

func (a *App) focusedTerm() *TermPane {
	t, _ := a.focused().(*TermPane)
	return t
}

func (a *App) loadFonts() {
	a.painter.Forget()
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok {
			t.r.fg.Forget()
		}
	}
	if a.hintFont != nil {
		a.hintFont.Dispose()
		a.hintFont = nil // loaded again by splashFont: the display scale may have changed
	}
	if a.termFont != nil {
		a.termFont.Dispose()
	}
	if a.uiFont != nil && a.uiFont != a.termFont {
		a.uiFont.Dispose()
	}
	f, err := gfx.LoadFont(a.cfg.Font, a.cfg.FontSize, a.dsf)
	if err != nil {
		a.toast("font not found, falling back to the pixel font: "+err.Error(), a.theme.Warn)
		a.cfg.Font = gfx.FontPixel
		f, _ = gfx.LoadFont(gfx.FontPixel, a.cfg.FontSize, a.dsf)
	}
	a.termFont = f
	// Same font at the same size: one atlas for both.
	ui := cmp.Or(a.cfg.UIFont, gfx.FontTerminus)
	if f.Name == ui && a.cfg.FontSize == a.cfg.UIFontSize {
		a.uiFont = f
	} else if a.uiFont, err = gfx.LoadFont(ui, a.cfg.UIFontSize, a.dsf); err != nil {
		a.toast("interface font not found, falling back to Terminus: "+err.Error(), a.theme.Warn)
		a.uiFont, _ = gfx.LoadFont(gfx.FontTerminus, a.cfg.UIFontSize, a.dsf)
	}
	// The backgrounds keep the pixel size they had with the 6×13 pixel font.
	a.decorPx = float32(gfx.PixelScale(a.cfg.UIFontSize, a.dsf)) * 2
	if a.symbols == "" {
		switch a.cfg.SymbolsFont {
		case "none":
			a.symbols = "-"
		case "", "auto":
			a.symbols = findSymbolsFont()
		default:
			a.symbols = a.cfg.SymbolsFont
		}
	}
	if a.symbols != "-" {
		a.termFont.SetSymbols(a.symbols)
		if a.uiFont != a.termFont {
			a.uiFont.SetSymbols(a.symbols)
		}
	}
	a.px = float32(max(1, math.Round(a.dsf)))
	a.relayout()
}

func (a *App) init() {
	a.inited = true
	a.dsf = ebiten.Monitor().DeviceScaleFactor()
	a.loadFonts()
	a.startBattery()
	fixAppMenu()
	a.openFirstPanes()
	a.lastInput = time.Now()
	a.spareAt = time.Now().Add(spareDelay)
	a.script = a.opts.Script
	a.scriptAt = time.Now().Add(500 * time.Millisecond)
}

// openFirstPanes sets up the launch: the demo, the saved layout, or the
// terminal of the -e command. With none of them (first launch), the window
// stays on the title screen until mod+enter opens the first terminal (the
// prewarmed shell, ready two seconds after launch, makes it instant).
func (a *App) openFirstPanes() {
	switch {
	case a.opts.Demo:
		a.setupDemo()
	case a.client != nil && !a.opts.Fresh && a.restoreLayout():
	case len(a.opts.Exec) > 0:
		a.addPane(NewTermPane(a.newID(), a.opts.Exec, a.opts.StartDir, a.paneEnv()))
	}
}

// runScript replays one script key per frame, as if it came from the
// keyboard: "wait:1s", "text:ls -la", "enter", "cmd+e", "ctrl+space", "j".
func (a *App) runScript(ctx *Ctx) {
	if len(a.script) == 0 || ctx.Now.Before(a.scriptAt) {
		return
	}
	tok := a.script[0]
	a.script = a.script[1:]
	a.scriptAt = ctx.Now.Add(150 * time.Millisecond)
	a.markInput(ctx.Now)
	switch {
	case strings.HasPrefix(tok, "wait:"):
		if d, err := time.ParseDuration(strings.TrimPrefix(tok, "wait:")); err == nil {
			a.scriptAt = ctx.Now.Add(d)
		}
	case strings.HasPrefix(tok, "text:"):
		rs := []rune(strings.TrimPrefix(tok, "text:"))
		switch {
		case a.renaming != nil:
			a.renameBuf = append(a.renameBuf, rs...)
			a.needRedraw = true
		case a.palette != nil:
			a.palette.chars(rs)
		case a.explorer != nil:
			a.explorer.Chars(ctx, rs)
		case a.focused() != nil:
			a.focused().Chars(ctx, rs)
		}
	default:
		c, err := ParseCombo(tok, a.mod)
		if err != nil {
			return
		}
		ev := KeyEvent{Name: c.Name, Mods: c.Mods}
		wasResize := a.mode == "resize"
		consumed := a.handleKey(ctx, ev)
		// A letter without a modifier also produces a character, as with GLFW.
		if !consumed && isPrintableName(c.Name) && c.Mods&(ModCtrl|ModMeta) == 0 && a.mode != "leader" {
			r := []rune(c.Name)
			if c.Mods&ModShift != 0 {
				r = []rune(strings.ToUpper(c.Name))
			}
			if a.palette != nil {
				a.palette.chars(r)
			} else if a.explorer != nil {
				a.explorer.Chars(ctx, r)
			} else if p := a.focused(); p != nil {
				p.Chars(ctx, r)
			}
		} else if a.leaderWait && isPrintableName(c.Name) {
			a.leaderWait = false
			a.mode = ""
			if act, ok := a.bindings[Combo{Name: "leader+" + c.Name}]; ok {
				a.do(act)
			}
		} else if wasResize && a.mode == "resize" && isPrintableName(c.Name) && c.Mods&(ModCtrl|ModMeta) == 0 {
			// From the keyboard, these letters arrive as typed characters.
			r := []rune(c.Name)[0]
			if c.Mods&ModShift != 0 {
				r = []rune(strings.ToUpper(c.Name))[0]
			}
			a.resizeChar(r)
		}
	}
}

func (a *App) newID() int {
	a.nextID++
	return a.nextID
}

func (a *App) paneEnv() []string {
	env := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := exe[:strings.LastIndexByte(exe, '/')]
		// NEONMUX_BIN points to the running binary, even if another
		// version comes first in PATH.
		env = append(env, "PATH="+os.Getenv("PATH")+":"+dir, "NEONMUX_BIN="+exe)
	}
	return env
}

// addPane inserts a pane and gives it focus. Past the maximum number of
// tiles, a new tile takes the place of the active tile, which goes to the
// garage and keeps running (never a floating pane, never a killed process).
func (a *App) addPane(p Pane) {
	if !a.roomForPane() {
		p.Close()
		return
	}
	now := time.Now()
	p.Base().glitchAt = now
	// A game left on screen would float over the new pane.
	a.leaveGame(p)
	a.arrived = p
	a.noteFocus()
	a.prevFocus = a.focused()
	if old := a.activeTile(); isTile(p) && a.countPanes(false) >= a.cfg.MaxPanes && old != nil {
		i := slices.Index(a.panes, old)
		p.Base().target = old.Base().target
		a.stow(old)
		a.panes = slices.Insert(a.panes, i, p)
		a.focus = i
		a.toast("tiles full: \""+gfx.Truncate(displayName(old), 16)+"\" goes to the garage", a.theme.Accent2)
	} else {
		n := a.visibleCount()
		a.panes = slices.Insert(a.panes, n, p)
		a.focus = n
	}
	if isFloat(p) {
		a.raise(p)
	}
	if t, ok := p.(*TermPane); ok {
		a.lastTerm = t
		t.addedAt = now
	}
	// A new floating pane leaves the zoomed tile alone. The new pane
	// takes focus: a maximized floating pane goes back to its place.
	if isTile(p) {
		a.zoomed = false
	}
	a.zoomFloat = nil
	a.relayout()
}

func (a *App) retire(p Pane) {
	b := p.Base()
	if b.parked {
		p.Close() // no fade-out on screen: it was not there
		return
	}
	b.closing = true
	b.closeAt = time.Now()
	a.closing = append(a.closing, p)
	p.Close()
}

func (a *App) closePane(i int) {
	if i < 0 || i >= len(a.panes) {
		return
	}
	p := a.panes[i]
	a.panes = slices.Delete(a.panes, i, i+1)
	a.retire(p)
	if a.drag != nil && a.drag.p == p {
		a.drag = nil
	}
	// A pane closed during a click does not get the release.
	if a.pressPane == p {
		a.pressPane = nil
	}
	if a.clickPane == p {
		a.clickPane = nil
	}
	if a.prevFocus == p {
		a.prevFocus = nil
	}
	if a.visitorBack == p {
		a.visitorBack = nil
	}
	back := Pane(nil)
	if a.visitor == p {
		back, a.visitor = a.visitorBack, nil
		a.visitorBack = nil
	}
	switch {
	case i == a.focus:
		// The visitor gives focus back to the pane it was brought out from.
		// Otherwise focus stays on the same layer: the neighboring tile after
		// a tile, the previous tile after a floating pane.
		if j := slices.Index(a.panes, back); j >= 0 && !isParked(back) {
			a.focus = j
			a.arrive()
		} else {
			a.refocus(i, isFloat(p))
		}
	case i < a.focus:
		a.focus--
	}
	a.relayout()
}

func (a *App) metrics() (gap, header, status, pad int) {
	px := int(a.px)
	f := a.uiFont
	return 6 * px, f.CellH + 4*px, f.CellH + 6*px, 3 * px
}

// relayout recomputes the areas of all panes: tiles share the work area,
// floating panes sit on top of it at their own place.
func (a *App) relayout() {
	a.needRedraw = true
	a.layoutDirty = true
	if a.uiFont == nil || a.w == 0 {
		return
	}
	gap, _, status, _ := a.metrics()
	area := image.Rect(gap, gap, a.w-gap, a.h-status-gap)
	// Zoom on a floating pane: it alone fills the whole area.
	if a.zoomFloat != nil && (isTile(a.zoomFloat) || !slices.Contains(a.panes, a.zoomFloat)) {
		a.zoomFloat = nil
	}
	maxed := a.zoomFloat
	lay := a.layout
	if a.zoomed {
		lay = "monocle"
	}
	var tiles []Pane
	for _, p := range a.panes {
		if isTile(p) {
			tiles = append(tiles, p)
		}
	}
	rects := tile(lay, len(tiles), area, a.ratio, a.ratio2)
	// A separate context: a.ctx belongs to the current frame, which Update
	// holds by pointer. Rewriting it here would drop its DT to a few µs.
	lctx := a.newCtx(time.Now(), 0)
	ctx := &lctx
	active := a.activeTile()
	for i, p := range tiles {
		r := rects[i]
		if lay != "monocle" {
			r = r.Inset(gap / 2)
			if len(tiles) == 1 {
				r = area
			}
		}
		p.Base().hidden = maxed != nil || (lay == "monocle" && p != active)
		a.place(ctx, p, r, true)
	}
	for _, p := range a.panes {
		b := p.Base()
		if b.parked {
			b.hidden = true
			continue
		}
		if isTile(p) {
			continue
		}
		r := a.floatRect(b.geom)
		b.hidden = maxed != nil && p != maxed
		if p == maxed {
			r = area
		}
		a.place(ctx, p, r, a.drag == nil || a.drag.p != p)
	}
}

// place puts a pane in r, animated or not, and gives it its content area.
func (a *App) place(ctx *Ctx, p Pane, r image.Rectangle, animate bool) {
	_, header, _, pad := a.metrics()
	b := p.Base()
	if animate {
		b.moveTo(r, ctx.Now)
	} else {
		b.target, b.from = r, image.Rectangle{}
	}
	if !b.hidden {
		p.Layout(ctx, contentRect(r, header, pad))
	}
}

// frameCtx prepares the context of the current frame, kept in a.ctx.
func (a *App) frameCtx(now time.Time) *Ctx {
	dt := now.Sub(a.lastFrame).Seconds()
	if dt > 0.25 || dt < 0 {
		dt = 0.25
	}
	a.ctx = a.newCtx(now, dt)
	return &a.ctx
}

// newCtx builds a context independent of the frame's own.
func (a *App) newCtx(now time.Time, dt float64) Ctx {
	return Ctx{App: a, Theme: a.theme, Term: a.termFont, UI: a.uiFont, P: &a.painter,
		Now: now, DT: dt, DSF: a.dsf, Px: a.px, Mods: currentMods()}
}

// Update handles input and state. Draw does nothing if nothing changed.
func (a *App) Update() error {
	t0 := time.Now()
	a.pacer.FrameDone()
	if !a.inited {
		if a.w == 0 {
			return nil
		}
		a.init()
	}
	now := time.Now()
	ctx := a.frameCtx(now)
	a.lastFrame = now

	for {
		select {
		case fn := <-a.posted:
			fn()
			a.needRedraw = true
			continue
		default:
		}
		break
	}

	if f := ebiten.IsFocused(); f != a.wasFocused {
		a.windowFocus(f)
	}

	a.handleDrops(ctx)
	a.runScript(ctx)
	a.handleKeyboard(ctx)
	a.handleMouse(ctx)
	// The echo of a keystroke arrives within a few milliseconds: wait for it so
	// it shows up in this frame rather than at the next refresh.
	if a.echoArmed {
		a.echoArmed = false
		a.pacer.AwaitKick(a.echoKicks, echoWait)
	}

	for _, p := range a.panes {
		if p.Update(ctx) {
			a.damagePane(p)
		}
	}
	// Terminals whose process has exited close on their own.
	// A terminal lost with the daemon is not finished: it keeps its place and
	// waits for the daemon to come back (watchDaemon).
	for i := len(a.panes) - 1; i >= 0; i-- {
		if p := a.panes[i]; p.Done() {
			if t, ok := p.(*TermPane); ok && t.daemonLost() {
				a.revive = true
				continue
			}
			if isParked(p) {
				a.toast("garage: \""+displayName(p)+"\" has exited", a.theme.Dim)
			}
			a.closePane(i)
		}
	}
	if a.renaming != nil && !slices.Contains(a.panes, a.renaming) {
		a.renaming = nil
	}
	if a.explorer != nil {
		a.updateExplorer(ctx)
	}
	a.closing = slices.DeleteFunc(a.closing, func(p Pane) bool {
		return now.Sub(p.Base().closeAt) > closeDur
	})

	a.toasts = slices.DeleteFunc(a.toasts, func(t toastMsg) bool { return now.Sub(t.at) > toastLife })

	if ebiten.IsWindowBeingClosed() {
		a.quit = true
	}
	a.watchDaemon(now)
	a.recycleSpare(now)
	a.prepareSpare(now)
	// With the daemon lost, the layout waits for its return: saved now, it
	// could capture a transient state.
	if a.layoutDirty && now.Sub(a.layoutSavedAt) > time.Second && !a.daemonLost {
		a.saveLayout()
	}
	a.schedule(ctx)
	a.stats.update(now, time.Since(t0))
	if a.quit {
		a.Shutdown()
		return ebiten.Termination
	}
	return nil
}

// Shutdown saves the layout, then detaches the daemon's terminals (they
// keep running) or ends them when running without a daemon.
func (a *App) Shutdown() {
	if a.shutdown {
		return
	}
	a.shutdown = true
	if a.client != nil {
		a.saveLayout()
	}
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.s != nil {
			// A daemon session goes on without the window. A local session
			// (no daemon, or opened while it was lost) ends with it.
			if _, remote := t.s.(remoteBackend); remote {
				t.s.Detach()
			} else {
				t.s.Close()
			}
			continue
		}
		p.Close()
	}
	a.panes = nil
	// The spare was never used: it is closed, not detached.
	if a.spare != nil {
		a.spare.p.Close()
		a.spare = nil
	}
	if a.client != nil {
		a.client.Close()
	}
}

const toastLife = 3500 * time.Millisecond

// schedule computes the animation frame rate and the next useful wakeup.
func (a *App) schedule(ctx *Ctx) {
	now := ctx.Now
	fps := 0
	// The background does not freeze for lack of typing: in auto mode, only
	// losing focus pauses it.
	active := ebiten.IsFocused() || a.cfg.Power == "max"

	if a.bg.Animated = a.cfg.Animate && a.cfg.Power != "eco" && active; a.bg.Animated {
		fps = max(fps, a.bg.FPS())
		a.bg.Update(ctx.DT)
		if a.bg.FPS() > 0 {
			a.needRedraw = true
		}
	}
	// The focus border lights up again on every pane change, then
	// dims down to its resting intensity.
	if f := a.focused(); f != a.lit {
		a.lit, a.litAt = f, now
	}
	if a.focusFading(now) {
		fps = max(fps, 30)
		a.damagePane(a.lit)
	}
	animating := len(a.closing) > 0
	for _, p := range a.panes {
		if n := p.AnimFPS(ctx); n > 0 && !p.Base().hidden {
			fps = max(fps, n)
			a.damagePane(p)
		}
		b := p.Base()
		if b.animating(now) {
			fps = max(fps, 60)
			a.needRedraw = true
			animating = true
		}
		// A hidden pane (monocle, maximized floating pane, garage) does not pulse.
		if b.notify != "" && a.cfg.Power != "eco" && !b.hidden {
			fps = max(fps, 15)
			a.damagePane(p)
		}
	}
	if len(a.closing) > 0 || a.parts.Active() {
		fps = max(fps, 60)
		a.needRedraw = true
	}
	// A blocked agent in the garage makes the garage summary pulse.
	if _, _, blocked := a.garageSummary(); blocked && a.cfg.Power != "eco" {
		fps = max(fps, 15)
	}
	if a.zenAnimating(now) {
		fps = max(fps, 60)
		a.needRedraw = true
		animating = true
	}
	// Zen mode alert frame: one frame on each toggle.
	if a.zenAlertLit(now) != a.zenLit {
		a.needRedraw = true
	}
	// One last full frame when an animation ends: panes that were
	// waiting for their final position finally get drawn.
	if a.wasAnimating && !animating {
		a.needRedraw = true
	}
	a.wasAnimating = animating
	a.parts.Update(float32(ctx.DT))
	for _, t := range a.toasts {
		if now.Sub(t.at) < 250*time.Millisecond {
			fps = max(fps, 60)
			a.needRedraw = true
		}
	}
	if a.held != nil {
		fps = max(fps, int(time.Second/a.repEvery)+1)
	}
	if a.hud {
		fps = max(fps, 2)
		a.needRedraw = true
	}
	if a.opts.Screenshot != "" || len(a.script) > 0 {
		fps = max(fps, 10)
	}
	a.pacer.SetFPS(fps)

	// Next one-off wakeup: cursor blink, end of a toast, clock minute.
	wake := now.Truncate(time.Minute).Add(time.Minute)
	for _, p := range a.panes {
		// A past or zero deadline is ignored: it must not hide
		// the other wakeups.
		if t := p.NextWake(ctx); t.After(now) && t.Before(wake) {
			wake = t
		}
	}
	for _, t := range a.toasts {
		if e := t.at.Add(toastLife); e.Before(wake) {
			wake = e
		}
	}
	if a.held != nil && a.held.next.Before(wake) {
		wake = a.held.next
	}
	if t := zenAlertNext(now); a.zenAlert() && t.Before(wake) {
		wake = t
	}
	// The spare gets prepared on time, even if nothing moves.
	if a.cfg.Prewarm && a.spare == nil && !a.spareAt.IsZero() && a.spareAt.After(now) && a.spareAt.Before(wake) {
		wake = a.spareAt
	}
	a.pacer.WakeAt(wake)
	if m := now.Truncate(time.Minute); m != a.lastMinute {
		a.lastMinute = m
		a.needRedraw = true
	}
}

// damagePane asks for a single pane to be redrawn. A hidden pane is not
// drawn: its dirty flag waits for it to reappear (relayout then redraws
// everything), without costing Draw its early exit on every frame.
func (a *App) damagePane(p Pane) {
	if b := p.Base(); !b.hidden && !b.parked && !slices.Contains(a.damage, p) {
		a.damage = append(a.damage, p)
	}
}

func (a *App) markInput(now time.Time) {
	a.lastInput = now
	a.pacer.Urgent()
}

// echoWait caps the wait for the echo in the keystroke's frame. zsh answers
// in 2 to 5 ms; past that, the frame goes out without it and Urgent takes over.
const echoWait = 8 * time.Millisecond

// armEcho notes that a keystroke is going to a terminal: Update will wait for
// its echo before reading the panes.
func (a *App) armEcho() {
	if !a.echoArmed {
		a.echoArmed = true
		a.echoKicks = a.pacer.Kicks()
	}
}

func (a *App) handleKeyboard(ctx *Ctx) {
	now := ctx.Now
	a.keyBuf = inpututil.AppendJustPressedKeys(a.keyBuf[:0])
	a.charBuf = ebiten.AppendInputChars(a.charBuf[:0])
	mods := ctx.Mods

	var events []KeyEvent
	for _, k := range a.keyBuf {
		if isModifierKey(k) {
			continue
		}
		ev := KeyEvent{Key: k, Name: keyName(k), Mods: mods}
		events = append(events, ev)
		if repeatable(ev) {
			a.held = &heldKey{ev: ev, next: now.Add(a.repDelay)}
		}
	}
	if a.held != nil {
		if !ebiten.IsKeyPressed(a.held.ev.Key) {
			a.held = nil
		} else if !now.Before(a.held.next) {
			ev := a.held.ev
			ev.Repeat = true
			ev.Mods = mods
			events = append(events, ev)
			a.held.next = a.held.next.Add(a.repEvery)
			if a.held.next.Before(now) {
				a.held.next = now.Add(a.repEvery)
			}
		}
	}
	if len(events) == 0 && len(a.charBuf) == 0 {
		return
	}
	markTypedKeypad(events, a.charBuf)
	a.markInput(now)
	// A field opened by a key in this frame (palette, rename) does not
	// receive the character of that same key.
	renamingBefore, paletteBefore := a.renaming != nil, a.palette != nil

	consumedChars := false
	for _, ev := range events {
		if a.handleKey(ctx, ev) {
			consumedChars = true
		}
	}
	if len(a.charBuf) == 0 || a.zen {
		return
	}
	switch {
	case a.renaming != nil:
		if renamingBefore {
			for _, r := range a.charBuf {
				if r >= ' ' {
					a.renameBuf = append(a.renameBuf, r)
				}
			}
			a.needRedraw = true
		}
	case a.palette != nil:
		if paletteBefore {
			a.palette.chars(a.charBuf)
		}
	case a.leaderWait:
		a.leaderWait = false
		a.mode = ""
		if act, ok := a.bindings[Combo{Name: "leader+" + string(a.charBuf[0])}]; ok {
			a.do(act)
		}
	case a.mode == "resize":
		for _, r := range a.charBuf {
			a.resizeChar(r)
		}
	case consumedChars || mods&ModMeta != 0:
	case runtime.GOOS == "darwin" && a.cfg.OptionAsMeta && mods&ModAlt != 0:
	case a.explorer != nil:
		a.explorer.Chars(ctx, a.charBuf)
	default:
		if p := a.focused(); p != nil {
			p.Chars(ctx, a.charBuf)
		}
	}
}

// handleKey handles a key. It returns true if the app consumed it
// (shortcut, leader, palette): characters from the same frame are then ignored.
func (a *App) handleKey(ctx *Ctx, ev KeyEvent) bool {
	if a.zen {
		a.zenKey(ev)
		return true
	}
	if a.renaming != nil {
		a.renameKey(ev)
		return true
	}
	if a.palette != nil {
		a.palette.key(ev)
		return true
	}
	if a.mode == "leader" {
		a.mode = ""
		if ev.Repeat {
			return true
		}
		combo := Combo{Mods: ev.Mods &^ ModShift, Name: "leader+" + ev.Name}
		if ev.Mods&ModShift != 0 {
			combo.Name = "leader+shift+" + ev.Name
		}
		if (Combo{Mods: ev.Mods, Name: ev.Name}) == a.leader {
			// Leader twice: send the key itself to the program.
			if a.explorer != nil {
				a.explorer.Key(ctx, ev)
			} else if p := a.focused(); p != nil {
				p.Key(ctx, ev)
			}
			return true
		}
		if act, ok := a.bindings[Combo{Name: combo.Name}]; ok {
			a.do(act)
			return true
		}
		if isPrintableName(ev.Name) {
			// Maybe a composed character (":" with Shift): wait for it.
			a.mode = "leader"
			a.leaderWait = true
		}
		return true
	}
	if a.mode == "resize" {
		arrows := map[string]rune{"left": 'h', "right": 'l', "up": 'k', "down": 'j'}
		switch r, ok := arrows[ev.Name]; {
		case ev.Name == "escape", ev.Name == "enter":
			a.mode = ""
		case ok && ev.Mods&ModShift != 0:
			// Shift + arrow moves a floating pane, like H J K L.
			a.resizeChar(r - 'a' + 'A')
		case ok:
			a.resizeChar(r)
		}
		return true
	}
	if !ev.Repeat && (Combo{Mods: ev.Mods, Name: ev.Name}) == a.leader {
		a.mode = "leader"
		a.leaderWait = false
		return true
	}
	if act, ok := a.bindings[Combo{Mods: ev.Mods, Name: ev.Name}]; ok {
		// A key held down does not close or park one pane after
		// another (on Linux, mod+w is a ctrl+… that repeats).
		if !ev.Repeat || !oneShot[act] {
			a.do(act)
		}
		return true
	}
	if ev.Mods&ModMeta != 0 {
		return true // cmd is never sent to the program
	}
	if a.explorer != nil {
		// Its characters come next, in the same frame (Chars).
		a.explorer.Key(ctx, ev)
		return false
	}
	if p := a.focused(); p != nil {
		// A keypad key sent as SS3 (application mode) must not be
		// followed by a character that arrived in the same frame.
		if p.Key(ctx, ev) && isKeypad(ev.Key) && isPrintableName(ev.Name) {
			return true
		}
	}
	return false
}

// oneShot lists the actions that a repeated key does not replay.
var oneShot = map[string]bool{"pane.close": true, "pane.park": true}

func (a *App) resizeChar(r rune) {
	if p := a.focused(); p != nil && isFloat(p) {
		a.floatResizeChar(p, r)
		return
	}
	step := 0.04
	switch r {
	case 'h':
		a.ratio = max(0.15, a.ratio-step)
	case 'l':
		a.ratio = min(0.85, a.ratio+step)
	case 'k':
		a.ratio2 = max(0.15, a.ratio2-step)
	case 'j':
		a.ratio2 = min(0.85, a.ratio2+step)
	case 'q':
		a.mode = ""
		return
	case '=':
		a.ratio, a.ratio2 = 0.5, 0.5
	}
	a.relayout()
}

// paneAt returns the pane under pt: the topmost floating pane first, then
// the tiles.
func (a *App) paneAt(pt image.Point) int { return a.hitAt(pt).pane }

// countClick counts clicks in quick succession (double, triple click). A
// click that comes too late (400 ms), too far from the previous one (10
// pixels, in either direction) or on another pane starts over at 1.
func (a *App) countClick(now time.Time, pt image.Point, p Pane) int {
	d := pt.Sub(a.clickPos)
	if now.Sub(a.lastClick) < 400*time.Millisecond && d.X*d.X+d.Y*d.Y < 100 && p == a.clickPane {
		a.clickCount++
	} else {
		a.clickCount = 1
	}
	a.lastClick, a.clickPos, a.clickPane = now, pt, p
	return a.clickCount
}

func (a *App) handleMouse(ctx *Ctx) {
	if a.zen {
		return
	}
	x, y := ebiten.CursorPosition()
	pt := image.Pt(x, y)
	mods := ctx.Mods
	_, header, _, _ := a.metrics()

	// What is under the mouse, looked up only once (the draw order is
	// sorted again on every lookup), and again only if a click or a drag
	// may have changed the stacking.
	var hit mouseHit
	stale := true
	under := func() mouseHit {
		if stale {
			hit, stale = a.hitAt(pt), false
		}
		return hit
	}

	if _, wy := ebiten.Wheel(); wy != 0 {
		a.markInput(ctx.Now)
		if a.palette != nil {
			a.palette.scroll(int(math.Copysign(1, wy)))
		} else if a.explorer != nil {
			a.explorer.Mouse(ctx, MouseEvent{Kind: MouseWheel, X: x, Y: y, Wheel: wy, Mods: mods})
		} else if i := under().pane; i >= 0 {
			a.panes[i].Mouse(ctx, MouseEvent{Kind: MouseWheel, X: x, Y: y, Wheel: wy, Mods: mods})
		}
	}
	for _, btn := range []ebiten.MouseButton{ebiten.MouseButtonLeft, ebiten.MouseButtonMiddle, ebiten.MouseButtonRight} {
		if inpututil.IsMouseButtonJustPressed(btn) {
			a.markInput(ctx.Now)
			if a.palette != nil {
				a.palette = nil
				continue
			}
			if a.explorer != nil {
				a.explorerClick(ctx, MouseEvent{Kind: MousePress, X: x, Y: y, Button: btn, Mods: mods})
				continue
			}
			h := under()
			stale = true // the click may give focus and raise a floating pane
			// Edge of a floating pane: resize.
			if h.float >= 0 && h.edges != 0 && btn == ebiten.MouseButtonLeft {
				a.setFocus(h.float)
				a.startDrag(a.panes[h.float], h.edges, pt)
				continue
			}
			i := h.pane
			if i < 0 {
				continue
			}
			if btn == ebiten.MouseButtonLeft {
				a.countClick(ctx.Now, pt, a.panes[i])
			}
			if i != a.focus {
				a.setFocus(i)
			}
			p := a.panes[i]
			if pt.Y < p.Base().target.Min.Y+header {
				switch {
				case btn == ebiten.MouseButtonRight:
					a.startRename(p)
				case a.clickCount == 2:
					a.do("pane.zoom")
				case isFloat(p) && btn == ebiten.MouseButtonLeft:
					a.startDrag(p, edgeAll, pt)
				}
				continue
			}
			// The modifier plus a drag anywhere moves a floating pane.
			if isFloat(p) && btn == ebiten.MouseButtonLeft && a.mod != 0 && mods&a.mod == a.mod {
				a.startDrag(p, edgeAll, pt)
				continue
			}
			a.pressPane = p
			p.Mouse(ctx, MouseEvent{Kind: MousePress, X: x, Y: y, Button: btn, Mods: mods, Clicks: a.clickCount})
		}
		if btn == ebiten.MouseButtonLeft && inpututil.IsMouseButtonJustReleased(btn) && a.drag != nil {
			a.dragTo(ctx, pt)
			a.endDrag()
			stale = true
		}
		if inpututil.IsMouseButtonJustReleased(btn) && a.pressPane != nil {
			a.pressPane.Mouse(ctx, MouseEvent{Kind: MouseRelease, X: x, Y: y, Button: btn, Mods: mods})
			a.pressPane = nil
			a.needRedraw = true
		}
	}
	if pt != a.lastMouse && a.drag != nil {
		a.lastMouse = pt
		a.dragTo(ctx, pt)
		stale = true
		ebiten.SetCursorShape(edgesCursor(a.drag.edges))
	}
	if pt != a.lastMouse {
		a.lastMouse = pt
		h := under()
		target := a.pressPane
		if target == nil && h.pane >= 0 {
			target = a.panes[h.pane]
		}
		if target != nil {
			target.Mouse(ctx, MouseEvent{Kind: MouseMove, X: x, Y: y, Mods: mods})
		}
		shape := ebiten.CursorShapeDefault
		switch i := h.pane; {
		case h.float >= 0 && h.edges != 0:
			shape = edgesCursor(h.edges)
		case i >= 0 && isFloat(a.panes[i]) && a.panes[i] != a.zoomFloat && pt.Y < a.panes[i].Base().target.Min.Y+header:
			shape = ebiten.CursorShapeMove
		case i >= 0 && a.panes[i].Kind() == "term" && pt.Y > a.panes[i].Base().target.Min.Y+header:
			shape = ebiten.CursorShapeText
		}
		ebiten.SetCursorShape(shape)
	}
}

// windowFocus tracks the window gaining and losing focus. The active
// terminal is told (mode 1004).
func (a *App) windowFocus(f bool) {
	a.wasFocused = f
	a.needRedraw = true
	if t := a.focusedTerm(); t != nil && f && !a.zen {
		t.markSeen()
	}
	if t := a.focusedTerm(); t != nil && t.s != nil && t.t.Modes()&vt.ModeFocus != 0 {
		if f {
			t.write([]byte("\x1b[I"))
		} else {
			t.write([]byte("\x1b[O"))
		}
	}
}

func (a *App) setFocus(i int) {
	if i < 0 || i >= len(a.panes) || isParked(a.panes[i]) {
		return
	}
	if i == a.focus {
		// A floating pane that is already active but covered is still raised.
		if p := a.panes[i]; isFloat(p) && p.Base().z != a.zTop {
			a.raise(p)
			a.needRedraw = true
		}
		return
	}
	a.blur()
	a.noteFocus()
	a.prevFocus = a.focused()
	a.focus = i
	a.arrive()
}

// blur tells the active terminal that it is about to lose focus (mode 1004).
func (a *App) blur() {
	if t := a.focusedTerm(); t != nil && t.s != nil && t.t.Modes()&vt.ModeFocus != 0 {
		t.write([]byte("\x1b[O"))
	}
}

// arrive applies the arrival of focus on the active pane: it is raised if
// it floats, its notification is read, a finished agent is seen and a
// terminal that tracks focus (mode 1004) is told.
func (a *App) arrive() {
	p := a.focused()
	if p == nil {
		return
	}
	a.leaveGame(p)
	a.arrived = p
	if isFloat(p) {
		a.raise(p)
	}
	if t, ok := p.(*TermPane); ok && ebiten.IsFocused() && !a.zen {
		t.markSeen()
	}
	// A maximized floating pane goes back to its place as soon as it loses focus.
	if a.zoomFloat != nil && a.zoomFloat != p {
		a.zoomFloat = nil
		a.relayout()
	}
	b := p.Base()
	b.notify = ""
	b.activity = false
	if t, ok := p.(*TermPane); ok {
		a.lastTerm = t
		t.resetBlink(time.Now())
		if t.s != nil && t.t.Modes()&vt.ModeFocus != 0 {
			t.write([]byte("\x1b[I"))
		}
	}
	if a.zoomed || a.layout == "monocle" {
		a.relayout()
	}
	a.needRedraw = true
}

// startRename starts editing a pane's name, right in its header.
func (a *App) startRename(p Pane) {
	if p == nil {
		return
	}
	a.renaming = p
	a.renameBuf = []rune(p.Base().name)
	a.needRedraw = true
}

func (a *App) renameKey(ev KeyEvent) {
	a.needRedraw = true
	switch {
	case ev.Name == "enter":
		// An empty name goes back to the automatic name (foreground process).
		a.renaming.Base().name = strings.TrimSpace(string(a.renameBuf))
		a.renaming = nil
		a.layoutDirty = true
	case ev.Name == "escape":
		a.renaming = nil
	case ev.Name == "backspace":
		if n := len(a.renameBuf); n > 0 {
			if ev.Mods&(ModAlt|ModCtrl) != 0 {
				// Previous word.
				i := n - 1
				for i > 0 && a.renameBuf[i-1] == ' ' {
					i--
				}
				for i > 0 && a.renameBuf[i-1] != ' ' {
					i--
				}
				a.renameBuf = a.renameBuf[:i]
			} else {
				a.renameBuf = a.renameBuf[:n-1]
			}
		}
	case ev.Mods&ModCtrl != 0 && ev.Name == "u":
		a.renameBuf = a.renameBuf[:0]
	}
}

// recentTerm returns the last terminal that had focus, or the first one.
func (a *App) recentTerm() (*TermPane, int) {
	first, firstIdx := (*TermPane)(nil), -1
	for i, p := range a.panes[:a.visibleCount()] {
		t, ok := p.(*TermPane)
		if !ok {
			continue
		}
		if t == a.lastTerm {
			return t, i
		}
		if first == nil {
			first, firstIdx = t, i
		}
	}
	return first, firstIdx
}

// cdTerminal moves t (nil: the last used terminal) to dir. The command is
// only typed when the shell is at its prompt: never into nvim or a build.
// It returns false when it refuses.
func (a *App) cdTerminal(t *TermPane, dir string) bool {
	i := slices.Index(a.panes, Pane(t))
	if t == nil {
		t, i = a.recentTerm()
	}
	if t == nil || t.s == nil {
		a.newShellPane(dir, true)
		return true
	}
	if name := strings.TrimPrefix(t.s.ForegroundName(), "-"); name != "" && !shell.IsShell(name) {
		a.toast("terminal "+itoa(i+1)+" is running "+name+": t opens a new terminal here", a.theme.Warn)
		return false
	}
	// Ctrl-U clears the current line before typing the command.
	t.write([]byte("\x15cd -- " + shellQuote(dir) + "\r"))
	a.setFocus(i)
	a.toast("cd "+shortPath(dir), a.theme.Accent2)
	return true
}

// toast shows a short-lived notification.
func (a *App) toast(text string, c color.RGBA) {
	a.toasts = append(a.toasts, toastMsg{text: text, color: c, at: time.Now()})
	if len(a.toasts) > 4 {
		a.toasts = a.toasts[1:]
	}
	a.needRedraw = true
}

// notifyPane marks a pane as asking for attention (bell, OSC 9/777).
func (a *App) notifyPane(p Pane, title, body string) {
	b := p.Base()
	msg := strings.TrimSpace(strings.TrimSpace(title) + " " + strings.TrimSpace(body))
	if msg == "" {
		msg = "bell"
	}
	if a.focused() == p && ebiten.IsFocused() {
		if body != "bell" {
			a.toast(msg, a.theme.Accent2)
		}
		return
	}
	b.notify = msg
	// The number is the one in the bottom bar (mod+1..9), not the internal
	// id. A pane in the garage has none.
	a.toast(a.paneTag(p)+" "+msg, a.theme.Warn)
}

// runFX runs an effect command received from a program (OSC 7777) or
// from the socket: "confetti", "glitch", "notify=text".
func (a *App) runFX(p Pane, cmd string) {
	cmd = strings.TrimSpace(cmd)
	name, arg, _ := strings.Cut(cmd, "=")
	if name == "fx" {
		name, arg, _ = strings.Cut(arg, ":")
	}
	if name == "kata" {
		return // only the vim kata's own session speaks this (katapane.go)
	}
	if isParked(p) && (name == "confetti" || name == "glitch") {
		return // in the garage, nothing on screen for the effect
	}
	r := p.Base().target
	cx, cy := float32(r.Min.X+r.Dx()/2), float32(r.Min.Y+r.Dy()/2)
	th := a.theme
	switch name {
	case "confetti":
		a.parts.Burst(cx, cy, 160, []color.RGBA{th.Accent, th.Accent2, th.Warn, th.FG}, float32(r.Dy())*0.9, 2*a.px)
	case "glitch":
		p.Base().glitchAt = time.Now()
	case "notify":
		a.notifyPane(p, "", arg)
	case "toast":
		a.toast(arg, th.Accent2)
	default:
		a.toast("unknown effect: "+name, th.Warn)
	}
	a.needRedraw = true
}

func (a *App) sparks(x, y float32) {
	th := a.theme
	a.parts.Sparks(x, y, []color.RGBA{th.Accent, th.Accent2, th.Warn}, a.px*2)
}

// Layout is required by ebiten.Game, LayoutF takes over.
func (a *App) Layout(w, h int) (int, int) { return w, h }

// LayoutF renders at the physical resolution of the screen (Retina included).
func (a *App) LayoutF(w, h float64) (float64, float64) {
	s := ebiten.Monitor().DeviceScaleFactor()
	pw, ph := int(math.Ceil(w*s)), int(math.Ceil(h*s))
	if pw != a.w || ph != a.h || (a.inited && s != a.dsf) {
		a.w, a.h = pw, ph
		fx.ScreenSize = image.Pt(pw, ph)
		if a.inited && s != a.dsf {
			a.dsf = s
			a.loadFonts()
		} else {
			a.relayout()
		}
		a.needRedraw = true
	}
	return float64(pw), float64(ph)
}

func itoa(i int) string { return strconv.Itoa(i) }
