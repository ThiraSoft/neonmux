package ui

import (
	"slices"
	"time"
)

// The waiting games (typing test, vim kata) are panes like the others, with
// one rule of their own. A game comes out large in the center as the
// garage's visitor, and goes back to the garage as soon as the focus leaves
// it: floating above the tiles, it would otherwise hide the agent the player
// went to answer. mod+shift+n brings it back, paused where it was. It never
// moves without a gesture of the player.

// gamePane is implemented by the panes of the games.
type gamePane interface {
	Pane
	// suspend pauses the game: it is leaving the screen.
	suspend(now time.Time)
}

func isGame(p Pane) bool {
	_, ok := p.(gamePane)
	return ok
}

// playing tells whether the player has the game p in hand: the window has
// the system focus, p the focus, and no explorer above it takes the
// keyboard. Otherwise its clock stops.
func (a *App) playing(p Pane) bool {
	return windowFocused() && a.focused() == p && a.explorer == nil
}

// showGame goes to the game that match recognizes, bringing it out of the
// garage if it is there. Without one, create makes it and it comes out as
// the visitor, large in the center.
func (a *App) showGame(match func(Pane) bool, create func(id int) Pane) {
	for _, p := range a.panes {
		if match(p) {
			a.goTo(p)
			return
		}
	}
	if !a.roomForPane() {
		return
	}
	p := create(a.newID())
	b := p.Base()
	b.parked, b.hidden = true, true
	a.panes = append(a.panes, p)
	a.visit(p)
}

// leaveGame sends to the garage the game that had the focus, now that the
// focus goes to next.
func (a *App) leaveGame(next Pane) {
	g := a.arrived
	if g == nil || g == next || !isGame(g) || isParked(g) || !slices.Contains(a.panes, g) {
		return
	}
	a.stow(g)
	a.relayout()
}
