package ui

import (
	"image"
	"path/filepath"
	"slices"
	"time"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/vt"
)

// The garage: panes that keep running without being shown. A pane in the
// garage stays in a.panes (its process, its agent state, its notifications
// and its git counter carry on), but it is neither drawn nor placed in the
// layout. Visible panes always come first in a.panes, panes in the garage
// after them: the index of a visible pane still gives its number (bottom
// bar, mod+1..9), and a pane in the garage has none.
//
// mod+n, the picker and the agent board bring a pane out of the garage as a
// visitor: a large floating pane in the center, above everything else,
// without touching the layout. Only one visitor at a time. It stays there
// until it is sent back (mod+shift+n, mod+m) or kept on screen
// (mod+shift+f).

const (
	maxTotalPanes = 64   // all panes, garage included
	visitorW      = 0.72 // size of the visitor, as a share of the area
	visitorH      = 0.80
)

func isParked(p Pane) bool { return p.Base().parked }

// visibleCount returns the number of panes shown, at the start of a.panes.
func (a *App) visibleCount() int {
	for i, p := range a.panes {
		if p.Base().parked {
			return i
		}
	}
	return len(a.panes)
}

// parkedPanes returns the panes in the garage.
func (a *App) parkedPanes() []Pane { return a.panes[a.visibleCount():] }

// paneTag returns the tag of a pane in a toast: its number, or
// "garage".
func (a *App) paneTag(p Pane) string {
	if isParked(p) {
		return "[garage]"
	}
	return "[" + itoa(slices.Index(a.panes, p)+1) + "]"
}

// movePane moves p to index to in a.panes without changing the focused pane.
func (a *App) movePane(p Pane, to int) {
	cur := a.focused()
	i := slices.Index(a.panes, p)
	if i < 0 {
		return
	}
	a.panes = slices.Delete(a.panes, i, i+1)
	a.panes = slices.Insert(a.panes, min(to, len(a.panes)), p)
	a.focus = slices.Index(a.panes, cur)
}

// roomForPane checks the total pane cap before opening a new one.
func (a *App) roomForPane() bool {
	if len(a.panes) < maxTotalPanes {
		return true
	}
	a.toast("already "+itoa(maxTotalPanes)+" panes, garage included: close one first", a.theme.Warn)
	return false
}

// stow puts p in the garage, at the end of the list. The focused pane does
// not change, even if it is p: the caller picks the next one.
func (a *App) stow(p Pane) {
	b := p.Base()
	if b.parked {
		return
	}
	if p == a.visitor {
		// The visitor gets back its floating spot and the PTY size it had
		// before it came out.
		b.geom = a.visitorGeom
		if t, ok := p.(*TermPane); ok && a.visitorCols > 0 && t.s != nil && a.termFont != nil &&
			(t.cols != a.visitorCols || t.rows != a.visitorRows) {
			t.cols, t.rows = a.visitorCols, a.visitorRows
			t.s.Resize(t.cols, t.rows, t.cols*a.termFont.CellW, t.rows*a.termFont.CellH)
			t.dirty = true
		}
		a.visitor, a.visitorBack = nil, nil
	}
	// A zoomed tile that goes to the garage takes the zoom with it:
	// otherwise another tile would go fullscreen without being asked to.
	if a.zoomed && isTile(p) && p == a.activeTile() {
		a.zoomed = false
	}
	if a.zoomFloat == p {
		a.zoomFloat = nil
	}
	if a.drag != nil && a.drag.p == p {
		a.drag = nil
	}
	if a.pressPane == p {
		a.pressPane = nil
	}
	if a.renaming == p {
		a.renaming = nil
	}
	// Its texture is rebuilt when it shows again: in the garage it would only
	// hold on to video memory (several MB per terminal on Retina).
	if t, ok := p.(*TermPane); ok {
		t.r.dispose()
		t.dirty = true
	}
	if gp, ok := p.(gamePane); ok {
		gp.suspend(time.Now())
	}
	b.parked, b.floating, b.hidden = true, false, true
	a.movePane(p, len(a.panes))
}

// park sends p to the garage (mod+m). The focus moves to the neighboring
// pane on the same plane, as on close.
func (a *App) park(p Pane) {
	if p == nil || isParked(p) {
		return
	}
	if p == a.visitor {
		a.sendBack()
		return
	}
	i := slices.Index(a.panes, p)
	focused, wasFloat := a.focused() == p, isFloat(p)
	if focused {
		a.blur()
	}
	a.stow(p)
	if focused {
		a.refocus(i, wasFloat)
	}
	if !a.parkHinted {
		a.parkHinted = true
		a.toast("\""+gfx.Truncate(displayName(p), 16)+"\" in the garage · "+a.hintFor("agents.pick")+" to find it again", a.theme.Accent2)
	}
	a.relayout()
}

// refocus picks the focused pane when the focused pane, which was at index
// i, has just left: the neighboring tile after a tile, the last focused tile
// after a floating pane, never a pane in the garage. With no visible pane,
// no pane is focused.
func (a *App) refocus(i int, wasFloat bool) {
	a.focus = min(i, a.visibleCount()-1)
	if j := slices.Index(a.panes, a.lastTile); wasFloat && j >= 0 && isTile(a.lastTile) {
		a.focus = j
	} else if j := a.nearestTile(i); !wasFloat && j >= 0 {
		a.focus = j
	}
	a.arrive()
}

// visit brings a pane out of the garage as a visitor, in the center and
// above everything else, and gives it the focus. The previous visitor goes
// back to the garage.
func (a *App) visit(p Pane) {
	b := p.Base()
	if !b.parked {
		a.setFocus(slices.Index(a.panes, p))
		return
	}
	back := a.focused()
	if v := a.visitor; v != nil {
		// A game left for another visitor is the place to come back to.
		if back == v && !isGame(v) {
			back = a.visitorBack
		}
		a.stow(v)
	}
	if back != nil && isParked(back) && !isGame(back) {
		back = nil
	}
	to := a.visibleCount()
	a.visitorGeom, a.visitorCols, a.visitorRows = b.geom, 0, 0
	if t, ok := p.(*TermPane); ok {
		a.visitorCols, a.visitorRows = t.cols, t.rows
	}
	b.parked, b.hidden, b.floating = false, false, true
	b.geom = a.visitorFloatGeom()
	b.target = image.Rectangle{} // it appears from the center
	a.movePane(p, to)
	a.visitor, a.visitorBack = p, back
	a.setFocus(slices.Index(a.panes, p))
	a.prevFocus = back // never the visitor that just went back
	a.raise(p)
	if !a.visitHinted {
		a.visitHinted = true
		a.toast(a.hintFor("focus.back")+" sends it back to the garage, "+a.hintFor("pane.float")+" keeps it", a.theme.Accent2)
	}
	a.relayout()
}

// sendBack sends the visitor back to the garage and returns the focus to
// the pane it was brought out from.
func (a *App) sendBack() {
	v := a.visitor
	if v == nil {
		return
	}
	back := a.visitorBack
	i := slices.Index(a.panes, v)
	focused := a.focused() == v
	if focused {
		a.blur()
	}
	a.stow(v)
	if focused {
		if j := slices.Index(a.panes, back); j >= 0 && !isParked(back) {
			a.focus = j
			a.arrive()
		} else if j >= 0 && isGame(back) {
			// The game went to the garage when the player left it: it
			// comes back, paused where it was.
			a.visit(back)
			return
		} else {
			a.refocus(i, true)
		}
	}
	a.relayout()
}

// keepVisitor keeps the visitor on screen (mod+shift+f): as a tile if one
// is still free, otherwise as an ordinary floating pane, where it is.
func (a *App) keepVisitor() {
	p := a.visitor
	b := p.Base()
	switch {
	case a.countPanes(false) < a.cfg.MaxPanes:
		b.floating = false
		b.geom = a.visitorGeom
	case a.countPanes(true) < maxFloats:
		a.toast("no free tile: it stays floating", a.theme.Accent2)
	default:
		a.toast("no free tile or floating slot ("+itoa(a.cfg.MaxPanes)+" tiles, "+itoa(maxFloats)+" floating)", a.theme.Warn)
		return
	}
	a.visitor, a.visitorBack = nil, nil
	a.zoomFloat = nil
	a.relayout()
}

// goTo gives the focus to p; a pane in the garage comes out as a visitor.
func (a *App) goTo(p Pane) {
	switch {
	case p == nil || !slices.Contains(a.panes, p):
	case isParked(p):
		a.visit(p)
	default:
		a.setFocus(slices.Index(a.panes, p))
	}
}

// visitorFloatGeom returns the visitor's spot: large, in the center.
func (a *App) visitorFloatGeom() floatGeom {
	if a.uiFont == nil || a.w == 0 {
		return floatGeom{X: 0.5, Y: 0.5, W: 1, H: 1} // no window (tests)
	}
	area := a.floatArea()
	return floatGeom{X: 0.5, Y: 0.5, W: float64(area.Dx()) * visitorW / a.dsf, H: float64(area.Dy()) * visitorH / a.dsf}
}

// visitorCells returns the size in cells of the visitor's content: the size
// used for a terminal in the garage whose size is unknown (old layout.json).
func (a *App) visitorCells() (int, int) {
	if a.termFont == nil || a.w == 0 {
		return 120, 40
	}
	_, header, _, pad := a.metrics()
	r := a.floatRect(a.visitorFloatGeom())
	return max(2, (r.Dx()-2*pad)/a.termFont.CellW), max(1, (r.Dy()-header-2*pad)/a.termFont.CellH)
}

// garageSummary sums up the garage for the bottom bar: "garage 46 · 3
// waiting". blocked reports a blocked agent (the summary pulses); a plain
// notification counts among the waiting ones, without pulsing.
func (a *App) garageSummary() (text string, waiting int, blocked bool) {
	parked := a.parkedPanes()
	if len(parked) == 0 {
		return "", 0, false
	}
	for _, p := range parked {
		notified, needs := p.Base().notify != "", false
		if t, ok := p.(*TermPane); ok {
			_, needs = t.needsYou()
			blocked = blocked || t.hasProg && t.prog.State == vt.PSBlocked
		}
		if needs || notified {
			waiting++
		}
	}
	text = "garage " + itoa(len(parked))
	switch {
	case waiting == 1:
		text += " · 1 waiting"
	case waiting > 1:
		text += " · " + itoa(waiting) + " waiting"
	}
	return text, waiting, blocked
}

// pickerItems builds the picker (mod+d): every pane, agents first, from
// most to least urgent, then the others in order. A pane in the garage has
// no number and carries the "garage" tag.
func (a *App) pickerItems() []command {
	var agents []*TermPane
	var others []Pane
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.hasProg {
			agents = append(agents, t)
		} else {
			others = append(others, p)
		}
	}
	slices.SortStableFunc(agents, func(x, y *TermPane) int {
		if rx, ry := agentRank(x), agentRank(y); rx != ry {
			return rx - ry
		}
		return x.prog.Since.Compare(y.prog.Since)
	})
	num := func(p Pane) string {
		if isParked(p) {
			return "·"
		}
		return itoa(slices.Index(a.panes, p) + 1)
	}
	mark := func(p Pane, hint string) string {
		if isParked(p) {
			return "garage  " + hint
		}
		return hint
	}
	cmds := make([]command, 0, len(a.panes))
	for _, t := range agents {
		dir := firstOf(t.git.root, t.workDir())
		label := num(t) + "  " + filepath.Base(dir) + "  " + displayName(t)
		if title := cleanTitle(t.prog.Title); title != "" {
			label += " · " + title
		}
		_, badge, _ := t.progBadge(a.theme, time.Now())
		hint := gfx.Truncate(badge, 28)
		if t.git.branch != "" {
			hint = t.git.branch + "  " + hint
		}
		cmds = append(cmds, command{label: label, hint: mark(t, hint), action: "focus.id:" + itoa(t.id)})
	}
	for _, p := range others {
		label := num(p) + "  "
		dir := a.paneDir(p)
		if dir != "" {
			label += filepath.Base(dir) + "  "
		}
		label += displayName(p)
		if n := p.Base().notify; n != "" {
			label += " ●"
		}
		cmds = append(cmds, command{label: label, hint: mark(p, compactPath(shortPath(dir), 28)), action: "focus.id:" + itoa(p.Base().id)})
	}
	return cmds
}

// openAgentPicker opens the pane picker: the palette, filterable by project
// (directory), branch or topic, garage included. mod+w closes the picked
// pane there, in the garage or not.
func (a *App) openAgentPicker() {
	if len(a.panes) == 0 {
		a.toast("no open panes", a.theme.Dim)
		return
	}
	a.openPalette("PANES", a.pickerItems())
	pal := a.palette
	pal.status = itoa(len(a.panes)) + " panes   enter go to   " + a.hintFor("pane.close") + " close   esc cancel"
	pal.onClose = func(c command) {
		for i, p := range a.panes {
			if "focus.id:"+itoa(p.Base().id) == c.action {
				a.closePane(i)
				break
			}
		}
		if len(a.panes) == 0 {
			a.palette = nil
			return
		}
		pal.refresh(a.pickerItems())
		pal.status = itoa(len(a.panes)) + " panes   enter go to   " + a.hintFor("pane.close") + " close   esc cancel"
	}
}

// startHidden starts (or reattaches) the session of a terminal in the
// garage, which has no area to sit in: it keeps the size it had.
func (a *App) startHidden(t *TermPane, cols, rows int) {
	if t.s != nil || t.startEr != nil {
		return
	}
	if cols <= 0 || rows <= 0 {
		cols, rows = a.visitorCells()
	}
	s, err := a.startSession(t, cols, rows, false)
	if err != nil {
		t.startEr = err
		return
	}
	t.s, t.t = s, s.Terminal()
	t.applyColors(a.theme)
	t.cols, t.rows = cols, rows
	if a.termFont != nil {
		t.t.SetPixelSize(cols*a.termFont.CellW, rows*a.termFont.CellH)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
