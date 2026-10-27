package ui

import (
	"slices"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/vt"
)

// Workspaces, as in cmux or i3: nine sets of panes, one shown at a time
// (mod+1..9). A pane of a hidden workspace is put away: it sits at the end
// of a.panes with the garage panes and keeps running the same way (parked
// and hidden), but its away field holds the number of its workspace. The
// garage stays shared by every workspace.

const maxWorkspaces = 9

// wsState is what a hidden workspace keeps of its layout.
type wsState struct {
	layout        string
	ratio, ratio2 float64
	zoomed        bool
	focus         Pane
	lastTile      Pane
	lastFloat     Pane
}

// isAway reports a pane that belongs to a hidden workspace.
func isAway(p Pane) bool { return p.Base().away != 0 }

// inGarage reports a pane in the garage, the panes of hidden workspaces
// excluded.
func inGarage(p Pane) bool { b := p.Base(); return b.parked && b.away == 0 }

// curWS returns the number of the workspace shown, 1 when none was chosen.
func (a *App) curWS() int { return max(a.ws, 1) }

// putAway hides p with its workspace ws, where it keeps its floating spot
// and the size of its PTY.
func (a *App) putAway(p Pane, ws int) {
	a.hide(p)
	b := p.Base()
	b.parked, b.hidden, b.away = true, true, ws
	a.movePane(p, len(a.panes))
}

// switchWorkspace shows workspace n. The visitor goes back to the garage
// first: it belongs to no workspace.
func (a *App) switchWorkspace(n int) {
	cur := a.curWS()
	if n == cur || n < 1 || n > maxWorkspaces {
		return
	}
	a.sendBack()
	a.leaveGame(nil)
	a.blur()
	a.wss[cur] = wsState{a.layout, a.ratio, a.ratio2, a.zoomed, a.focused(), a.lastTile, a.lastFloat}
	shown := slices.Clone(a.panes[:a.visibleCount()])
	a.focus = -1
	for _, p := range shown {
		a.putAway(p, cur)
	}
	k := 0
	for _, p := range slices.Clone(a.panes) {
		if b := p.Base(); b.away == n {
			b.parked, b.hidden, b.away = false, false, 0
			a.movePane(p, k)
			k++
		}
	}
	st := a.wss[n]
	a.wss[n] = wsState{}
	a.ws = n
	a.layout, a.ratio, a.ratio2, a.zoomed = st.layout, st.ratio, st.ratio2, st.zoomed
	if a.layout == "" {
		a.layout = "tiles"
	}
	if a.ratio <= 0 || a.ratio2 <= 0 {
		a.ratio, a.ratio2 = 0.5, 0.5
	}
	a.lastTile, a.lastFloat, a.zoomFloat = st.lastTile, st.lastFloat, nil
	a.focus = min(0, a.visibleCount()-1)
	if i := slices.Index(a.panes, st.focus); i >= 0 && !isParked(st.focus) {
		a.focus = i
	}
	a.arrive()
	a.relayout()
}

// moveToWorkspace sends the focused pane to workspace n, which is not shown.
// The focus moves to the neighboring pane, as when it goes to the garage.
func (a *App) moveToWorkspace(n int) {
	p := a.focused()
	if p == nil || n == a.curWS() || n < 1 || n > maxWorkspaces {
		return
	}
	float := isFloat(p) && p != a.visitor
	tiles, floats := 0, 0
	for _, q := range a.panes {
		if b := q.Base(); b.away == n {
			if b.floating {
				floats++
			} else {
				tiles++
			}
		}
	}
	switch {
	case float && floats >= maxFloats:
		a.toast("workspace "+itoa(n)+" already has "+itoa(maxFloats)+" floating panes", a.theme.Warn)
		return
	case !float && tiles >= a.cfg.MaxPanes:
		a.toast("no free tile in workspace "+itoa(n)+" ("+itoa(a.cfg.MaxPanes)+" at most)", a.theme.Warn)
		return
	}
	// The visitor becomes an ordinary tile over there.
	if p == a.visitor {
		p.Base().floating = false
		p.Base().geom = a.visitorGeom
		a.visitor, a.visitorBack = nil, nil
	}
	i := slices.Index(a.panes, p)
	a.blur()
	a.putAway(p, n)
	a.refocus(i, float)
	a.toast("\""+gfx.Truncate(displayName(p), 16)+"\" → workspace "+itoa(n), a.theme.Accent2)
	a.relayout()
}

// wsInfo sums up a workspace for the bottom bar: its number of panes, and
// whether one of them waits for the user (blocked: an agent is blocked).
func (a *App) wsInfo(n int) (panes int, waiting, blocked bool) {
	for _, p := range a.panes {
		b := p.Base()
		if n == a.curWS() && b.parked || n != a.curWS() && b.away != n {
			continue
		}
		panes++
		if b.notify != "" {
			waiting = true
		}
		if t, ok := p.(*TermPane); ok {
			_, needs := t.needsYou()
			waiting = waiting || needs
			blocked = blocked || t.hasProg && t.prog.State == vt.PSBlocked
		}
	}
	return panes, waiting, blocked
}

// workspacesShown reports whether the bottom bar lists the workspaces: as
// soon as one other than the first is in use.
func (a *App) workspacesShown() bool {
	if a.curWS() != 1 {
		return true
	}
	for _, p := range a.panes {
		if isAway(p) {
			return true
		}
	}
	return false
}
