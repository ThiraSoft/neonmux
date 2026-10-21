package ui

import (
	"slices"
	"testing"
	"time"
)

// fakeGame is a game pane that counts its pauses.
type fakeGame struct {
	fakePane
	suspended int
}

func (g *fakeGame) suspend(time.Time) { g.suspended++ }

func openFakeGame(a *App) *fakeGame {
	a.showGame(func(p Pane) bool { _, ok := p.(*fakeGame); return ok }, func(id int) Pane {
		g := &fakeGame{}
		g.id = id
		return g
	})
	for _, p := range a.panes {
		if g, ok := p.(*fakeGame); ok {
			return g
		}
	}
	return nil
}

func countGames(a *App) int {
	n := 0
	for _, p := range a.panes {
		if isGame(p) {
			n++
		}
	}
	return n
}

func TestGameOpensAsVisitor(t *testing.T) {
	a, _ := testApp(false, false)
	a.setFocus(1)
	g := openFakeGame(a)
	if g == nil || a.visitor != g || a.focused() != g || !isFloat(g) {
		t.Fatalf("the game must come out as the focused visitor (visitor %v, focus %d)", a.visitor, a.focus)
	}
	if geom := g.Base().geom; geom.X != 0.5 || geom.Y != 0.5 {
		t.Errorf("the game must be in the center: %+v", geom)
	}
	openFakeGame(a)
	if n := countGames(a); n != 1 {
		t.Errorf("%d games, want one instance", n)
	}
}

func TestGameParksWhenFocusGoesToTile(t *testing.T) {
	a, ps := testApp(false, false)
	a.setFocus(0)
	g := openFakeGame(a)
	a.setFocus(slices.Index(a.panes, ps[1]))
	if !isParked(g) || a.focused() != ps[1] || g.suspended != 1 {
		t.Fatalf("parked %v, focus %d, suspended %d", isParked(g), a.focus, g.suspended)
	}
	if slices.Contains(a.drawOrder(), Pane(g)) {
		t.Error("a parked game is not drawn over the tile")
	}
	a.focusBack()
	if a.focused() != g || a.visitor != g || isParked(g) {
		t.Errorf("focusBack must bring the game back (visitor %v, focus %d)", a.visitor, a.focus)
	}
}

func TestGameBackAfterParkedAgent(t *testing.T) {
	a, ps := testApp(false, false, false)
	a.setFocus(0)
	agent := ps[2]
	a.park(agent)
	g := openFakeGame(a)
	a.goTo(agent)
	if a.visitor != agent || !isParked(g) || a.focused() != agent {
		t.Fatalf("the agent comes out and the game parks (visitor %v)", a.visitor)
	}
	a.focusBack()
	if !isParked(agent) {
		t.Error("the agent goes back to the garage")
	}
	if a.visitor != g || a.focused() != g || isParked(g) {
		t.Errorf("the game comes back (visitor %v, focus %d)", a.visitor, a.focus)
	}
}

func TestGameParksOnFocusLayer(t *testing.T) {
	a, _ := testApp(false, false)
	a.setFocus(0)
	g := openFakeGame(a)
	a.focusLayer()
	if !isParked(g) || isGame(a.focused()) {
		t.Fatal("focusLayer must park the game")
	}
	a.focusBack()
	if a.focused() != g {
		t.Error("focusBack must bring the game back")
	}
}

func TestGameParksWhenAPaneOpens(t *testing.T) {
	a, _ := testApp(false)
	a.setFocus(0)
	g := openFakeGame(a)
	n := &fakePane{}
	n.id = 99
	a.addPane(n)
	if !isParked(g) || a.focused() != Pane(n) {
		t.Error("a new pane parks the game it would hide behind")
	}
}

func TestShowGameBringsParkedGameOut(t *testing.T) {
	a, _ := testApp(false)
	a.setFocus(0)
	g := openFakeGame(a)
	a.park(g)
	if !isParked(g) {
		t.Fatal("mod+m parks the game")
	}
	if openFakeGame(a) != g || a.visitor != g || a.focused() != g || countGames(a) != 1 {
		t.Error("the palette entry brings the same game back out")
	}
}

func TestKataMessagesIgnoredElsewhere(t *testing.T) {
	a, ts := coreApp(t, 1)
	a.runFX(ts[0], "kata=done:eyJ9")
	if len(a.toasts) != 0 {
		t.Errorf("toasts %v: kata messages mean nothing outside the kata", a.toasts)
	}
}
