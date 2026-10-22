package ui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"runtime"
	"runtime/metrics"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// perfStats feeds the performance HUD.
type perfStats struct {
	frameTimes [90]float32
	idx        int
	frames     int
	fpsAt      time.Time
	fps        int
	heap, sys  uint64
	gcs        uint32
	rss        int64
	memAt      time.Time
	draws      int
	partial    int
	sysLabel   string // mb(sys), rebuilt with each sample, once per second
}

var memSamples = []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/total:bytes"}, {Name: "/gc/cycles/total:gc-cycles"}}

func (s *perfStats) update(now time.Time, d time.Duration) {
	s.frameTimes[s.idx] = float32(d.Seconds() * 1000)
	s.idx = (s.idx + 1) % len(s.frameTimes)
	s.frames++
	if now.Sub(s.fpsAt) >= time.Second {
		s.fps = s.frames
		s.frames = 0
		s.fpsAt = now
	}
	if now.Sub(s.memAt) >= time.Second {
		s.memAt = now
		metrics.Read(memSamples)
		s.heap = memSamples[0].Value.Uint64()
		s.sys = memSamples[1].Value.Uint64()
		s.sysLabel = mb(s.sys)
		s.gcs = uint32(memSamples[2].Value.Uint64())
		var ru syscall.Rusage
		if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) == nil {
			s.rss = ru.Maxrss
			if runtime.GOOS == "linux" {
				s.rss *= 1024
			}
		}
	}
}

func mb(b uint64) string { return fmt.Sprintf("%.1f MB", float64(b)/(1<<20)) }

// sysText returns mb(sys) for the status bar, rebuilt with each sample.
func (s *perfStats) sysText() string {
	if s.sysLabel == "" {
		s.sysLabel = mb(s.sys)
	}
	return s.sysLabel
}

// Draw composes the frame. With SetScreenClearedEveryFrame(false), drawing
// nothing keeps the previous frame: that is the usual case when idle.
func (a *App) Draw(screen *ebiten.Image) {
	if !a.inited {
		return
	}
	now := time.Now()
	vis := a.zenVisibility(now)
	// Comparing a converted []byte with a string does not allocate.
	statusChanged := false
	if vis == 0 {
		// Zen mode: no panes and no status bar, their changes can wait.
		a.damage = a.damage[:0]
	} else {
		statusChanged = string(a.statusSignature(now)) != a.statusSig
	}
	if !a.needRedraw && len(a.damage) == 0 && !statusChanged {
		return
	}
	// The floating layers (toasts, palette, HUD, particles) cover the
	// panes: while they are up, everything is recomposed.
	full := a.needRedraw || len(a.toasts) > 0 || a.palette != nil || a.explorer != nil ||
		a.hud || a.parts.Active() || len(a.closing) > 0 || a.visibleCount() == 0 || vis < 1
	a.needRedraw = false
	damage := a.damage
	a.damage = a.damage[:0]
	a.stats.draws++
	ctx := a.frameCtx(now)
	th := a.theme
	P := &a.painter

	target := screen

	if !full {
		// Partial render: only the damaged panes and the status bar are
		// recomposed, the rest of the previous frame is kept. A pane that
		// overlaps the area (a floating pane above, a tile below) is
		// redrawn along with it, in stacking order.
		a.stats.partial++
		order := a.drawOrder()
		_, _, status, _ := a.metrics()
		panesArea := image.Rect(0, 0, a.w, a.h-status) // the status bar is drawn on its own
		for _, p := range damage {
			if !slices.Contains(order, p) {
				continue
			}
			clip := a.paneExtent(p, now).Intersect(panesArea)
			sub := target.SubImage(clip).(*ebiten.Image)
			a.bg.Draw(sub, P, a.uiFont, th, a.decorPx)
			for _, q := range order {
				if q == p || a.paneExtent(q, now).Overlaps(clip) {
					i := slices.Index(a.panes, q)
					a.drawPane(ctx, sub, q, i, i == a.focus)
				}
			}
		}
		if statusChanged {
			_, _, status, _ := a.metrics()
			sub := target.SubImage(image.Rect(0, a.h-status, a.w, a.h)).(*ebiten.Image)
			a.drawStatus(ctx, sub)
			P.Flush(sub)
			a.statusSig = string(a.sigBuf)
		}
		a.maybeScreenshot(screen)
		return
	}
	if statusChanged {
		a.statusSig = string(a.sigBuf)
	}

	// Layer 0: the background.
	a.bg.Draw(target, P, a.uiFont, th, a.decorPx)

	// Layers 1 and 2: the tiles, the floating panes on top, then the panes
	// being closed. During the zen mode fade, they go through an
	// intermediate image drawn with the current opacity.
	if vis > 0 {
		layer := target
		if vis < 1 {
			layer = a.fadeLayer(target.Bounds())
		}
		for _, p := range a.drawOrder() {
			i := slices.Index(a.panes, p)
			a.drawPane(ctx, layer, p, i, i == a.focus)
		}
		for _, p := range a.closing {
			a.drawClosing(ctx, layer, p)
		}
		if a.visibleCount() == 0 {
			a.drawSplash(ctx, layer, true, 1)
		}
		a.drawStatus(ctx, layer)
		P.Flush(layer)
		if vis < 1 {
			op := &ebiten.DrawImageOptions{}
			op.ColorScale.ScaleAlpha(vis)
			target.DrawImage(layer, op)
		}
	}
	if vis < 1 {
		a.drawSplash(ctx, target, false, 1-vis)
	} else {
		a.releaseFade()
	}
	if vis == 0 {
		a.releaseFade()
	}
	a.drawZenAlert(target, now, 1-vis)

	// Layer 3: the explorer, freeform effects and floating UI.
	a.drawExplorer(ctx, target)
	a.parts.Draw(P)
	P.Flush(target)
	a.drawToasts(ctx, target)
	if a.palette != nil {
		a.palette.draw(ctx, target)
	}
	if a.hud {
		a.drawHUD(ctx, target)
	}
	P.Flush(target)

	a.maybeScreenshot(screen)
}

func (a *App) maybeScreenshot(screen *ebiten.Image) {
	if a.opts.Screenshot != "" && !a.shotDone && time.Since(a.start) > a.opts.ScreenshotWait {
		a.shotDone = true
		a.saveScreenshot(screen)
		a.quit = true
	}
}

// statusSignature sums up the content of the status bar: the bar is only
// redrawn when this summary changes. It is computed every frame, even when
// Draw has nothing to do: it is written into a buffer kept from one frame
// to the next, from texts that are already built (titles, memory), without
// allocating. A zero separates the fields, so that two different contents
// cannot be mistaken for each other once put end to end.
func (a *App) statusSignature(now time.Time) []byte {
	b := append(a.sigBuf[:0], a.mode...)
	b = strconv.AppendInt(append(b, 0), int64(a.focus), 10)
	if a.renaming != nil {
		b = append(b, "\x00rename"...)
	}
	for _, p := range a.panes[:a.visibleCount()] {
		b = append(append(b, 0), displayName(p)...)
		b = append(append(b, 0), p.Mode()...)
		b = append(append(b, 0), p.Base().notify...)
		if isFloat(p) {
			b = append(b, "\x00float"...)
		}
		if t, ok := p.(*TermPane); ok {
			g, _ := t.barIcon(a.theme)
			b = append(append(b, 0), g...)
		}
	}
	// The garage summary, and its pulse when a parked agent is blocked.
	if s, _, blocked := a.garageSummary(); s != "" {
		b = append(append(b, 0), s...)
		if blocked && a.cfg.Power != "eco" {
			b = strconv.AppendInt(append(b, 0), int64(pulse(now)*12), 10)
		}
	}
	b = append(append(b, 0), a.stats.sysText()...)
	if a.cfg.Battery && a.battText != "" {
		b = append(append(b, 0), a.battText...)
		if a.batt.onAC {
			b = append(b, '+')
		}
	}
	b = now.AppendFormat(append(b, 0), "15:04")
	a.sigBuf = b
	return b
}

func (a *App) saveScreenshot(screen *ebiten.Image) {
	b := screen.Bounds()
	img := image.NewRGBA(b)
	screen.ReadPixels(img.Pix)
	f, err := os.Create(a.opts.Screenshot)
	if err != nil {
		return
	}
	defer f.Close()
	_ = png.Encode(f, img)
}

func (a *App) drawPane(ctx *Ctx, dst *ebiten.Image, p Pane, idx int, focused bool) {
	th := a.theme
	P := ctx.P
	if _, help := p.(*HelpPane); !help {
		P.Runes = a.chromeRunes()
	}
	defer func() { P.Runes = nil }()
	b := p.Base()
	r := b.rect(ctx.Now)
	_, header, _, pad := a.metrics()
	px := a.px
	f := a.uiFont

	// Translucent fill: the background shows through a little. A floating
	// pane stays almost opaque so the text of the tile below does not get in the way.
	alpha := a.cfg.paneAlpha(th.PaneAlpha)
	if b.floating {
		a.drawShadow(P, r)
		alpha = max(alpha, floatAlpha)
	}
	P.Rect(float32(r.Min.X), float32(r.Min.Y), float32(r.Dx()), float32(r.Dy()), th.BG, alpha)

	// Header.
	hb := th.BarBG
	if focused {
		hb = theme.Mix(th.BarBG, th.Accent, 0.16)
	}
	P.Rect(float32(r.Min.X), float32(r.Min.Y), float32(r.Dx()), float32(header), hb, 0.96)
	accent := th.Dim
	if focused {
		accent = th.Accent
	}
	if pulsing(p) {
		accent = th.Warn
	}
	P.Rect(float32(r.Min.X), float32(r.Min.Y), 3*px, float32(header), accent, 1)
	ty := float32(r.Min.Y) + float32(header-f.CellH)/2
	x := float32(r.Min.X) + 3*px + float32(f.CellW)/2
	x += P.Text(f, x, ty, itoa(idx+1), accent, 1)
	x += float32(f.CellW) / 2
	iconS := float32(max(1, f.CellH/9))
	drawIcon(P, p.Kind(), x, float32(r.Min.Y)+(float32(header)-6*iconS)/2, iconS, th.Accent2)
	x += 6*iconS + float32(f.CellW)

	// On the right: an agent's badge (otherwise the pane info) and its change
	// counter. The texts are only rebuilt when what they show changes
	// (b.texts): a full frame does not rebuild them.
	ik := infoKey{mode: p.Mode(), renaming: a.renaming == p}
	infoC, icon := th.Dim, ""
	var stat []textPart
	if t, ok := p.(*TermPane); ok && t.hasProg {
		_, ik.text, infoC = t.badge(th, ctx.Now)
		icon, ik.offset = t.badgeIcon(), t.offset
		stat = t.statParts(th)
	} else {
		ik.text = p.Info()
	}
	renaming := ik.renaming
	if renaming {
		infoC, stat, icon = th.Dim, nil, ""
	}
	cw := float32(f.CellW)
	ik.n = max(8, int(float32(r.Dx())/cw*0.55))
	info := b.texts.info.get(ik, ik.build)
	infoW := badgeWidth(f, icon, info)
	badgeW := infoW
	statW := float32(0)
	for _, s := range stat {
		statW += float32(gfx.TextWidth(f, s.text)) + cw
	}
	if statW > 0 {
		infoW += statW + cw
	}
	// At least one cell for the title: a pane narrower than its header
	// (4 columns on Retina, a minimal floating pane with a large UI font)
	// has no room left, but nothing may go negative.
	room := max(1, int((float32(r.Max.X)-x-infoW-2*cw)/cw))
	titleC := th.BarFG
	if !focused {
		titleC = theme.Mix(th.BarFG, th.BG, 0.35)
	}
	if renaming {
		// Input field right in the header.
		P.Rect(x-cw/2, float32(r.Min.Y)+2*px, float32(room+1)*cw, max(0, float32(header)-4*px), th.BG, 0.95)
		P.Rect(x-cw/2, float32(r.Min.Y+header)-3*px, float32(room+1)*cw, px, th.Accent, 1)
		field := string(a.renameBuf)
		// The end of the field stays visible, cursor included.
		if keep := room - 1; gfx.TextCells(field) > keep {
			rs := []rune(field)
			field = string(rs[max(0, len(rs)-keep):])
		}
		w := P.Text(f, x, ty, field, th.FG, 1)
		if len(a.renameBuf) == 0 {
			P.Text(f, x+cw, ty, gfx.Truncate(p.Title(), room-2), th.Dim, 0.7)
		}
		P.Rect(x+w, ty, cw*0.6, float32(f.CellH), th.Accent, 0.9)
	} else {
		nk := cutKey{displayName(p), room}
		name := b.texts.name.get(nk, func() string { return gfx.Truncate(nk.text, nk.n) })
		w := P.Text(f, x, ty, name, titleC, 1)
		if st, ok := p.(subtitled); ok {
			sk := cutKey{st.Subtitle(), room - gfx.TextCells(name) - 2}
			if sub := b.texts.sub.get(sk, func() string { return compactPath(sk.text, sk.n) }); sub != "" {
				P.Text(f, x+w+2*cw, ty, sub, theme.Mix(th.Dim, th.BarBG, 0.15), 1)
			}
		}
	}
	if b.notify != "" {
		P.Text(f, float32(r.Max.X)-infoW-float32(3*f.CellW), ty, "●", th.Warn, 0.6+0.4*pulse(ctx.Now))
	} else if b.activity {
		P.Text(f, float32(r.Max.X)-infoW-float32(3*f.CellW), ty, "•", th.Accent2, 0.8)
	}
	sx := float32(r.Max.X) - infoW - cw
	for _, s := range stat {
		sx += P.Text(f, sx, ty, s.text, s.c, 0.9) + cw
	}
	drawBadge(P, f, float32(r.Max.X)-badgeW-cw, ty, icon, info, infoC)
	P.Flush(dst)

	if content := contentRect(r, header, pad); content.Dx() > 4 && content.Dy() > 4 {
		p.Draw(ctx, dst, content, focused)
	}
	P.Flush(dst)

	// Neon border: three outlines, each more transparent than the last.
	switch {
	case pulsing(p):
		a2 := 0.35 + 0.65*pulse(ctx.Now)
		P.Border(r, 2*px, th.Warn, a2)
		P.Border(r.Inset(-int(px)), px, th.Warn, 0.35*a2)
	case focused && a.focusBorderShown():
		k := focusGlow(a.cfg.FocusRest, a.cfg.FocusFade, ctx.Now.Sub(a.litAt))
		layers := focusBorderLayers(a.cfg.FocusBorder)
		// Once dimmed, the focus border sits on the normal border: it fades
		// toward it instead of into the background, and stays brighter.
		if k < 1 {
			P.Border(r, layers[0].width*px, th.Border, 1)
		}
		for _, l := range layers {
			P.Border(r.Inset(l.inset*int(px)), l.width*px, th.Accent, l.alpha*k)
		}
	case b.floating:
		// An unfocused floating pane must stay distinct from the tile below.
		P.Border(r, px, theme.Mix(th.Border, th.Dim, 0.6), 1)
	default:
		P.Border(r, px, th.Border, 1)
	}
	if k := flashLevel(ctx.Now.Sub(b.flashAt), b.flashN, b.flashEach); k > 0 {
		P.Border(r, 2*px, b.flashC, k)
		P.Border(r.Inset(-int(px)), px, b.flashC, 0.35*k)
	}
	if g := ctx.Now.Sub(b.glitchAt); g < glitchDur && p.Kind() != "term" {
		glitchBars(ctx, r, float32(g.Seconds()/glitchDur.Seconds()))
	}
	P.Flush(dst)
}

// infoKey describes the text on the right of the header: the pane info or
// an agent's badge, its position in the scrollback, the mode and the room.
type infoKey struct {
	text     string
	offset   int // lines scrolled back in the history (agents only)
	mode     string
	renaming bool
	n        int // available cells
}

func (k infoKey) build() string {
	s := k.text
	if k.offset > 0 {
		s = "↑" + itoa(k.offset) + "  " + s // reading the history
	}
	if k.mode != "" {
		s = "[" + k.mode + "]  " + s
	}
	if k.renaming {
		s = "enter confirm  esc cancel"
	}
	return gfx.Truncate(s, k.n)
}

// contentRect returns the content area of r, below the header. A pane that
// is too small gets an empty area: image.Rect would flip it, and the
// content would end up above the header.
func contentRect(r image.Rectangle, header, pad int) image.Rectangle {
	c := image.Rectangle{Min: image.Pt(r.Min.X+pad, r.Min.Y+header+pad), Max: image.Pt(r.Max.X-pad, r.Max.Y-pad)}
	c.Max.X, c.Max.Y = max(c.Max.X, c.Min.X), max(c.Max.Y, c.Min.Y)
	return c
}

func pulse(now time.Time) float32 {
	return float32(0.5 + 0.5*math.Sin(float64(now.UnixMilli())/1000*2*math.Pi*0.9))
}

// glitchBars draws random colored bars: the opening effect of panes that
// have no texture of their own.
func glitchBars(ctx *Ctx, r image.Rectangle, t float32) {
	th := ctx.Theme
	seed := int(ctx.Now.UnixMilli() / 45)
	for i := 0; i < 7; i++ {
		h := (seed*13 + i*29) % 97
		y := float32(r.Min.Y) + float32(r.Dy())*float32(h)/97
		w := float32(r.Dx()) * float32((seed*7+i*11)%60+20) / 100
		x := float32(r.Min.X) + float32((seed*3+i*5)%40)/100*float32(r.Dx())
		c := th.Accent
		if i%2 == 0 {
			c = th.Accent2
		}
		ctx.P.Rect(x, y, w, float32(ctx.UI.CellH)/3, c, 0.5*(1-t))
	}
}

func (a *App) drawClosing(ctx *Ctx, dst *ebiten.Image, p Pane) {
	b := p.Base()
	t := float32(ctx.Now.Sub(b.closeAt).Seconds() / closeDur.Seconds())
	if t >= 1 {
		return
	}
	r := b.target
	c := r.Min.Add(r.Max).Div(2)
	// CRT-style switch-off: the area collapses into a line.
	h := int(float32(r.Dy()) * (1 - t) * (1 - t))
	w := r.Dx()
	if t > 0.6 {
		w = int(float32(r.Dx()) * (1 - (t-0.6)/0.4))
	}
	rr := image.Rect(c.X-w/2, c.Y-h/2-1, c.X+w/2, c.Y+h/2+1)
	ctx.P.Rect(float32(rr.Min.X), float32(rr.Min.Y), float32(rr.Dx()), float32(rr.Dy()), a.theme.Accent, 0.5*(1-t))
	ctx.P.Border(rr, a.px, a.theme.FG, 1-t)
	ctx.P.Flush(dst)
}

const logo = `█▄ █ █▀▀ █▀█ █▄ █ █▀▄▀█ █ █ ▀▄▀
█ ▀█ ██▄ █▄█ █ ▀█ █ ▀ █ █▄█ █ █`

// splashFont returns the font of the title screen's hints: the built-in
// pixel font at 14 points, in the spirit of the logo, loaded on first use.
// It is scaled by whole factors, so it stays sharp on any screen.
func (a *App) splashFont() *gfx.Font {
	if a.hintFont == nil {
		a.hintFont, _ = gfx.LoadFont(gfx.FontPixel, 14, a.dsf) // the pixel font never fails
	}
	return a.hintFont
}

func (a *App) splashY(lines []string) float32 {
	scale := float32(2)
	return float32(a.h)/2 - float32(a.uiFont.CellH)*scale*float32(len(lines)+2)/2
}

// drawSplash draws the logo, with the starter shortcuts if hints is set
// (not in zen mode, where only the logo stays).
func (a *App) drawSplash(ctx *Ctx, dst *ebiten.Image, hints bool, alpha float32) {
	f := a.uiFont
	P := ctx.P
	P.Runes = a.chromeRunes()
	defer func() { P.Runes = nil }()
	logoText := logo
	if a.braille {
		logoText = brailleLogo
	}
	lines := strings.Split(logoText, "\n")
	scale := float32(2)
	lw := float32(gfx.TextCells(lines[0])*f.CellW) * scale
	x := (float32(a.w) - lw) / 2
	y := a.splashY(lines)
	for i, l := range lines {
		cx := x
		for _, r := range l {
			if r != ' ' {
				P.GlyphScaled(f.Glyph(r, 0, false), cx, y+float32(i*f.CellH)*scale, scale, a.theme.Accent, alpha)
			}
			cx += float32(f.CellW) * scale
		}
	}
	if !hints {
		P.Flush(dst)
		return
	}
	hint := Combo{Mods: a.mod, Name: "enter"}.String() + " new terminal    " +
		Combo{Mods: a.mod, Name: "p"}.String() + " palette    " + Combo{Mods: a.mod, Name: "i"}.String() + " help    " +
		Combo{Mods: a.mod, Name: "q"}.String() + " quit"
	hf := a.splashFont()
	hy := y + float32(f.CellH)*scale*float32(len(lines)+1)
	hw := float32(gfx.TextWidth(hf, hint))
	P.Text(hf, (float32(a.w)-hw)/2, hy, hint, a.theme.Dim, 1)
	// Everything is in the garage: show the way back to the panes.
	if n := len(a.parkedPanes()); n > 0 {
		g := a.hintFor("agents.pick") + " find " + plural(n, "pane") + " in the garage"
		gw := float32(gfx.TextWidth(hf, g))
		P.Text(hf, (float32(a.w)-gw)/2, hy+float32(hf.CellH)*2, g, a.theme.Accent2, 1)
	}
	P.Flush(dst)
}

// segment draws a status bar block ending with a Powerline triangle
// (drawn by hand by the atlas, so sharp at any size).
func (a *App) segment(P *gfx.Painter, x, y, h float32, text string, fg, bg color.RGBA) float32 {
	f := a.uiFont
	w := float32(gfx.TextWidth(f, text)) + float32(f.CellW)*2
	P.Rect(x, y, w, h, bg, 1)
	P.Text(f, x+float32(f.CellW), y+(h-float32(f.CellH))/2, text, fg, 1)
	g := f.Glyph(0xe0b0, 0, false)
	P.GlyphScaled(g, x+w, y, h/float32(f.CellH), bg, 1)
	return w + h*float32(f.CellW)/float32(f.CellH)
}

func (a *App) drawStatus(ctx *Ctx, dst *ebiten.Image) {
	th := a.theme
	P := ctx.P
	P.Runes = a.chromeRunes()
	defer func() { P.Runes = nil }()
	f := a.uiFont
	_, _, status, _ := a.metrics()
	y := float32(a.h - status)
	h := float32(status)
	P.Rect(0, y, float32(a.w), h, th.BarBG, 1)
	P.Rect(0, y, float32(a.w), a.px, th.Border, 1)

	mode := "NORMAL"
	modeBG := th.Accent
	switch {
	case a.palette != nil:
		mode, modeBG = "PALETTE", th.Accent2
	case a.explorer != nil:
		mode, modeBG = "FILES", th.Accent2
		if m := a.explorer.Mode(); m != "" {
			mode = m // FILTER
		}
	case a.mode == "leader":
		mode, modeBG = "LEADER", th.Warn
	case a.mode == "resize" && a.focused() != nil && isFloat(a.focused()):
		mode, modeBG = "FLOAT", th.Warn
	case a.mode == "resize":
		mode, modeBG = "RESIZE", th.Warn
	case a.renaming != nil:
		mode, modeBG = "RENAME", th.Accent2
	default:
		if p := a.focused(); p != nil && p.Mode() != "" {
			mode, modeBG = p.Mode(), th.Accent2
		}
	}
	x := a.segment(P, 0, y, h, mode, th.BG, modeBG)
	x += float32(f.CellW)
	ty := y + (h-float32(f.CellH))/2

	// On the right: layout, theme, memory, power mode, battery, clock.
	// Computed first to know how much room is left for the pane list.
	lay := layoutLabels[a.layout]
	if a.zoomed || a.zoomFloat != nil {
		lay = "ZOOM"
	}
	all := [6]barItem{
		{text: lay, color: th.BarFG},
		{text: a.theme.Label, color: th.BarFG},
		{text: a.stats.sysText(), color: th.BarFG},
		{text: powerLabels[a.cfg.Power], color: th.BarFG, icons: []barIcon{{"bolt", th.Warn}}},
	}
	n := 4
	if a.cfg.Battery && a.battText != "" {
		all[n] = a.batteryItem(th)
		n++
	}
	all[n] = barItem{text: a.clockLabel(ctx.Now), color: th.Accent2}
	right := all[:n+1]
	iconS := float32(max(1, f.CellH/9))
	rightW := float32(f.CellW)
	for _, it := range right {
		rightW += it.width(f, iconS) + float32(f.CellW)*2
	}
	// On a narrow screen, only the last two fields are kept: the clock, and
	// the battery or the power mode.
	for len(right) > 2 && rightW > float32(a.w)/2 {
		rightW -= right[0].width(f, iconS) + float32(f.CellW)*2
		right = right[1:]
	}
	limit := float32(a.w) - rightW - float32(f.CellW)
	// The garage summary follows the visible panes: its room is reserved
	// first, so that it is never cut off.
	garage, waiting, blocked := a.garageSummary()
	if garage != "" {
		limit -= float32(gfx.TextWidth(f, garage)) + float32(f.CellW)*2
	}

	for i, p := range a.panes[:a.visibleCount()] {
		b := p.Base()
		key := barKey{i, displayName(p), isFloat(p), b.notify != ""}
		label := b.texts.bar.get(key, func() string {
			l := itoa(key.num+1) + " " + gfx.Truncate(key.name, 14)
			if key.float {
				l = itoa(key.num+1) + " ❐ " + gfx.Truncate(key.name, 14)
			}
			if key.notify {
				l += " ●"
			}
			return l
		})
		c := th.Dim
		if i == a.focus {
			c = th.FG
		}
		if b.notify != "" {
			c = th.Warn
		}
		icon, ic := "", th.Dim
		if t, ok := p.(*TermPane); ok {
			icon, ic = t.barIcon(th)
		}
		w := badgeWidth(f, icon, label) + float32(f.CellW)
		if x+w > limit {
			if x+float32(f.CellW) <= limit {
				x += P.Text(f, x, ty, "…", th.Dim, 1) + float32(f.CellW)*2
			}
			break
		}
		if i == a.focus {
			P.Rect(x-float32(f.CellW)/2, y+2*a.px, w, h-4*a.px, theme.Mix(th.BarBG, th.Accent, 0.25), 1)
		}
		x += P.Text(f, x, ty, label, c, 1)
		if icon != "" {
			s := iconSize(f)
			drawIcon(P, icon, x+float32(f.CellW), ty+(float32(f.CellH)-6*s)/2, s, ic)
			x += 6*s + float32(f.CellW)
		}
		x += float32(f.CellW) * 2
	}
	if garage != "" {
		c, alpha := th.Dim, float32(1)
		switch {
		case blocked:
			c, alpha = th.Warn, 0.55+0.45*pulse(ctx.Now)
		case waiting > 0:
			c = th.FG
		}
		P.Text(f, x, ty, garage, c, alpha)
	}

	rx := float32(a.w) - float32(f.CellW)
	for i := len(right) - 1; i >= 0; i-- {
		it := right[i]
		rx -= float32(gfx.TextWidth(f, it.text))
		P.Text(f, rx, ty, it.text, it.color, 1)
		for j := len(it.icons) - 1; j >= 0; j-- {
			ic := it.icons[j]
			rx -= iconWidth(ic.kind, iconS) + float32(f.CellW)/2
			drawIcon(P, ic.kind, rx, y+(h-6*iconS)/2, iconS, ic.color)
		}
		rx -= float32(f.CellW) * 2
		if i > 0 {
			P.Text(f, rx+float32(f.CellW)/2, ty, "│", th.Border, 1)
		}
	}
}

var powerLabels = map[string]string{"eco": "ECO", "balanced": "AUTO", "max": "MAX"}

// barItem is a field on the right of the status bar. Its icons are drawn
// before the text.
type barItem struct {
	text  string
	color color.RGBA
	icons []barIcon
}

type barIcon struct {
	kind  string
	color color.RGBA
}

func (it barItem) width(f *gfx.Font, iconS float32) float32 {
	w := float32(gfx.TextWidth(f, it.text))
	for _, ic := range it.icons {
		w += iconWidth(ic.kind, iconS) + float32(f.CellW)/2
	}
	return w
}

// iconWidth returns the width of a pixel icon (see drawIcon).
func iconWidth(kind string, s float32) float32 {
	if rows := icons[kind]; len(rows) > 0 {
		return float32(len(rows[0])) * s
	}
	return 0
}

// barKey describes a pane's label in the status bar.
type barKey struct {
	num           int
	name          string
	float, notify bool
}

// clockLabel returns the status bar clock, rebuilt every minute.
func (a *App) clockLabel(now time.Time) string {
	if m := now.Truncate(time.Minute); !m.Equal(a.clockAt) || a.clock == "" {
		a.clockAt, a.clock = m, now.Format("15:04")
	}
	return a.clock
}

func (a *App) drawToasts(ctx *Ctx, dst *ebiten.Image) {
	f := a.uiFont
	P := ctx.P
	y := float32(f.CellH) * 2
	if a.hud {
		y += float32(f.CellH) * 16
	}
	for i := len(a.toasts) - 1; i >= 0; i-- {
		t := a.toasts[i]
		age := ctx.Now.Sub(t.at)
		in := float32(min(1, age.Seconds()/0.18))
		out := float32(min(1, (toastLife-age).Seconds()/0.4))
		alpha := min(in, max(out, 0))
		text := gfx.Truncate(t.text, 60)
		w := float32(gfx.TextWidth(f, text) + 3*f.CellW)
		h := float32(f.CellH) * 1.8
		x := float32(a.w) - w - float32(f.CellW) + (1-in)*w*0.5
		P.Rect(x, y, w, h, a.theme.BarBG, 0.95*alpha)
		P.Rect(x, y, 3*a.px, h, t.color, alpha)
		P.Border(image.Rect(int(x), int(y), int(x+w), int(y+h)), a.px, t.color, 0.6*alpha)
		P.Text(f, x+float32(f.CellW)*1.5, y+(h-float32(f.CellH))/2, text, a.theme.FG, alpha)
		y += h + float32(f.CellH)/2
	}
	P.Flush(dst)
}

func (a *App) drawHUD(ctx *Ctx, dst *ebiten.Image) {
	f := a.uiFont
	P := ctx.P
	th := a.theme
	s := &a.stats
	cells, hist := 0, 0
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.s != nil {
			cells += t.cols * t.rows
			t.t.Lock()
			hist += t.t.HistoryBytes()
			t.t.Unlock()
		}
	}
	wake := "idle (0 fps)"
	if fps := a.pacer.FPS(); fps > 0 {
		wake = fmt.Sprintf("active %d fps", fps)
	}
	var maxFT float32
	for _, v := range s.frameTimes {
		maxFT = max(maxFT, v)
	}
	lines := []string{
		"NEONMUX // PERF",
		fmt.Sprintf("fps        %d", s.fps),
		fmt.Sprintf("update max %.2f ms", maxFT),
		fmt.Sprintf("Go heap    %s", mb(s.heap)),
		fmt.Sprintf("Go runtime %s", mb(s.sys)),
		fmt.Sprintf("peak RSS   %s", mb(uint64(s.rss))),
		fmt.Sprintf("GC         %d", s.gcs),
		fmt.Sprintf("goroutines %d", runtime.NumGoroutine()),
		fmt.Sprintf("panes      %d (garage %d)", len(a.panes), len(a.parkedPanes())),
		fmt.Sprintf("cells      %d", cells),
		fmt.Sprintf("scrollback %s", mb(uint64(hist))),
		fmt.Sprintf("atlas      %d p / %d gl", a.termFont.AtlasPages(), a.termFont.GlyphCount()),
		fmt.Sprintf("render     %s", wake),
	}
	w := float32(26 * f.CellW)
	h := float32((len(lines)+3)*f.CellH) + float32(f.CellH)
	x := float32(a.w) - w - float32(f.CellW)
	y := float32(f.CellH)
	P.Rect(x, y, w, h, th.BarBG, 0.92)
	P.Border(image.Rect(int(x), int(y), int(x+w), int(y+h)), a.px, th.Accent2, 0.8)
	for i, l := range lines {
		c := th.FG
		if i == 0 {
			c = th.Accent2
		}
		P.Text(f, x+float32(f.CellW), y+float32(f.CellH)*(0.5+float32(i)), l, c, 1)
	}
	// Graph of update times, drawn freehand.
	gy := y + float32(f.CellH)*(float32(len(lines))+1)
	gh := float32(f.CellH) * 2
	bw := (w - 2*float32(f.CellW)) / float32(len(s.frameTimes))
	for i := range s.frameTimes {
		v := s.frameTimes[(s.idx+i)%len(s.frameTimes)]
		bh := min(gh, v/4*gh) + a.px
		P.Rect(x+float32(f.CellW)+float32(i)*bw, gy+gh-bh, max(bw-a.px, a.px), bh, th.Accent, 0.8)
	}
	P.Flush(dst)
}

// focusBorderShown reports whether the active pane shows its focus border.
// Alone on screen, it has nothing to stand out from: the normal border is
// enough, unless focus_border_alone asks for it.
func (a *App) focusBorderShown() bool {
	if a.cfg.FocusBorder <= 0 {
		return false
	}
	if a.cfg.SoloBorder {
		return true
	}
	shown := 0
	for _, p := range a.panes[:a.visibleCount()] {
		if !p.Base().hidden {
			shown++
		}
	}
	return shown > 1
}

// focusGlow returns the intensity of the focus border, since after the pane
// got focus: full at first, it stays bright for a moment then eases down
// to rest (in %), reached after fade seconds.
func focusGlow(rest, fade float64, since time.Duration) float32 {
	low := rest / 100
	if fade <= 0 || since.Seconds() >= fade {
		return float32(low)
	}
	t := max(since.Seconds()/fade, 0)
	return float32(low + (1-low)*(1-t*t*(3-2*t)))
}

// focusFading reports whether the focus border is dimming.
func (a *App) focusFading(now time.Time) bool {
	return a.lit != nil && a.cfg.FocusRest < 100 && now.Sub(a.litAt).Seconds() < a.cfg.FocusFade &&
		a.focusBorderShown()
}

// borderLayer is one outline of the focus border: outward offset (in
// pixels, negative), thickness and opacity.
type borderLayer struct {
	inset int
	width float32
	alpha float32
}

// focusBorderLayers builds the focus border: a solid line of the requested
// thickness (focus_border), then a halo, and a second halo from 2 up (the
// original look).
func focusBorderLayers(thickness float64) []borderLayer {
	if thickness <= 0 {
		return nil
	}
	layers := []borderLayer{{0, float32(thickness), 1}, {-1, 1, 0.35}}
	if thickness < 2 {
		layers[1].alpha = 0.25
		return layers
	}
	return append(layers, borderLayer{-2, 1, 0.14})
}
