package ui

import (
	"image"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/vt"
)

// Zen mode (mod+z): the panes and the bottom bar fade out and leave only
// the animated background and the logo. The same key brings them back.
// Programs keep running: only display and input are suspended. Toasts
// stay visible, so an agent that is waiting still gets noticed as it
// goes by.

const zenDur = 320 * time.Millisecond

func (a *App) zenProgress(now time.Time) float64 {
	if a.zenAt.IsZero() {
		return 1
	}
	return min(1, now.Sub(a.zenAt).Seconds()/zenDur.Seconds())
}

func (a *App) zenAnimating(now time.Time) bool { return a.zenProgress(now) < 1 }

// zenVisibility returns the opacity of the panes: 1 visible, 0 hidden.
func (a *App) zenVisibility(now time.Time) float32 {
	t := a.zenProgress(now)
	t = t * t * (3 - 2*t) // smooth start and finish
	if a.zen {
		return float32(1 - t)
	}
	return float32(t)
}

// toggleZen hides or brings back the panes. A toggle during the fade starts
// from the current opacity, without a jump.
func (a *App) toggleZen() {
	now := time.Now()
	done := a.zenProgress(now)
	a.zen = !a.zen
	a.zenAt = now.Add(-time.Duration((1 - done) * float64(zenDur)))
	a.needRedraw = true
	if a.zen {
		a.closeExplorer()
		a.palette, a.renaming, a.mode, a.drag, a.pressPane = nil, nil, "", nil, nil
		ebiten.SetCursorShape(ebiten.CursorShapeDefault)
	} else if t := a.focusedTerm(); t != nil {
		t.markSeen() // back again: the agent on screen is finally seen
	}
}

// zenKey filters the keyboard in zen mode: only leaving the mode (its
// shortcut or esc, since no program receives keys), quitting and picking
// a background respond.
func (a *App) zenKey(ev KeyEvent) {
	if ev.Name == "escape" && ev.Mods == 0 {
		a.toggleZen()
		return
	}
	if act := a.bindings[Combo{Mods: ev.Mods, Name: ev.Name}]; zenAllows(act) {
		a.do(act)
	}
}

// zenAllows reports the actions allowed in zen mode. The whole background
// is visible there: a good time to pick one (mod+shift+b).
func zenAllows(act string) bool {
	return act == "view.zen" || act == "quit" || strings.HasPrefix(act, "bg.")
}

// Alert frame: while an agent is blocked (permission, question), a frame
// in the accent color blinks around the edge of the window. It catches the
// eye from the side, without having to look straight at the screen. It
// costs only two frames per second, so it stays active in eco mode.

const (
	zenAlertBlink = 500 * time.Millisecond // on then off, once per second
	zenAlertWidth = 4                      // thickness, in logical pixels
)

// zenAlert reports that an agent is waiting for an answer during zen mode.
func (a *App) zenAlert() bool {
	if !a.zen {
		return false
	}
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.hasProg && t.prog.State == vt.PSBlocked {
			return true
		}
	}
	return false
}

// zenAlertLit reports whether the frame is lit at this moment.
func (a *App) zenAlertLit(now time.Time) bool {
	return a.zenAlert() && now.UnixMilli()/zenAlertBlink.Milliseconds()%2 == 0
}

// zenAlertNext returns the time of the frame's next toggle.
func zenAlertNext(now time.Time) time.Time {
	ms := zenAlertBlink.Milliseconds()
	return time.UnixMilli((now.UnixMilli()/ms + 1) * ms)
}

// drawZenAlert draws the frame with the fade opacity and remembers whether
// it is shown, so that schedule knows when to redraw.
func (a *App) drawZenAlert(dst *ebiten.Image, now time.Time, alpha float32) {
	a.zenLit = alpha > 0 && a.zenAlertLit(now)
	if !a.zenLit {
		return
	}
	a.painter.Border(dst.Bounds(), zenAlertWidth*a.px, a.theme.Accent, alpha)
	a.painter.Flush(dst)
}

// fadeLayer returns the intermediate fade image, allocated only while the
// animation runs.
func (a *App) fadeLayer(b image.Rectangle) *ebiten.Image {
	if a.fade == nil || a.fade.Bounds() != b {
		a.releaseFade()
		a.fade = ebiten.NewImageWithOptions(b, &ebiten.NewImageOptions{Unmanaged: true})
	}
	a.fade.Clear()
	return a.fade
}

func (a *App) releaseFade() {
	if a.fade != nil {
		a.fade.Deallocate()
		a.fade = nil
	}
}
