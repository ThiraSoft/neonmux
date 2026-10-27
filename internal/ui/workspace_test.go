package ui

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

func TestSwitchWorkspace(t *testing.T) {
	a, ps := testApp(false, false, true)
	a.setFocus(1)
	a.layout = "columns"
	a.do("ws.2")
	if a.curWS() != 2 || a.visibleCount() != 0 || a.focused() != nil {
		t.Fatalf("workspace %d, %d visible, focus %d", a.curWS(), a.visibleCount(), a.focus)
	}
	for _, p := range ps {
		if b := p.Base(); !b.parked || !b.hidden || b.away != 1 || isTile(p) || isFloat(p) {
			t.Fatalf("pane %d must be put away with workspace 1", b.id)
		}
	}
	if !ps[2].Base().floating {
		t.Error("a floating pane put away keeps floating")
	}
	if a.garageCount() != 0 {
		t.Error("the panes of a hidden workspace are not in the garage")
	}
	if a.layout != "tiles" {
		t.Errorf("a new workspace starts in tiles, not %q", a.layout)
	}
	// A pane opened here belongs to workspace 2.
	q := &fakePane{}
	q.id = 9
	a.addPane(q)
	a.do("ws.1")
	if want := []Pane{ps[0], ps[1], ps[2], q}; !slices.Equal(a.panes, want) {
		t.Fatalf("panes %v", ids(a.panes))
	}
	if a.focused() != ps[1] || a.layout != "columns" || !isFloat(ps[2]) || q.away != 2 {
		t.Errorf("focus %d, layout %q, away %d", a.focus, a.layout, q.away)
	}
	a.do("ws.1") // already shown: nothing changes
	if a.curWS() != 1 || a.focused() != ps[1] {
		t.Error("switching to the shown workspace does nothing")
	}
}

func TestMoveToWorkspace(t *testing.T) {
	a, ps := testApp(false, false)
	a.setFocus(0)
	a.do("ws.move.3")
	if ps[0].Base().away != 3 || a.focused() != ps[1] || a.visibleCount() != 1 {
		t.Fatalf("away %d, focus %d", ps[0].Base().away, a.focus)
	}
	a.do("ws.3")
	if a.focused() != ps[0] || ps[1].Base().away != 1 {
		t.Errorf("focus %d", a.focus)
	}
	// The tiles of workspace 1 are full: nothing moves.
	a.cfg.MaxPanes = 1
	a.do("ws.move.1")
	if ps[0].Base().away != 0 || a.focused() != ps[0] {
		t.Error("no free tile over there: the pane stays")
	}
}

// goTo (mod+n, the picker, the agent board) shows the pane's workspace.
func TestGoToAwayPane(t *testing.T) {
	a, ps := testApp(false, false)
	a.setFocus(1)
	a.do("ws.move.2")
	a.goTo(ps[1])
	if a.curWS() != 2 || a.focused() != ps[1] || ps[0].Base().away != 1 {
		t.Errorf("workspace %d, focus %d", a.curWS(), a.focus)
	}
	if a.paneTag(ps[0]) != "[workspace 1]" {
		t.Errorf("tag %q", a.paneTag(ps[0]))
	}
}

// The garage is shared: a pane in it stays there whatever the workspace,
// and the visitor goes back to it on a switch.
func TestWorkspaceAndGarage(t *testing.T) {
	a, ps := testApp(false, false)
	a.park(ps[0])
	a.visit(ps[0])
	a.do("ws.2")
	if !inGarage(ps[0]) || a.visitor != nil || ps[1].Base().away != 1 {
		t.Fatalf("garage %v, visitor %v", inGarage(ps[0]), a.visitor)
	}
	a.do("ws.1")
	if !inGarage(ps[0]) || a.focused() != ps[1] {
		t.Error("the garage pane stays in the garage")
	}
}

// mod+digit switches with the top row and with the keypad.
func TestWorkspaceKeys(t *testing.T) {
	a, _ := keysApp(t, 1)
	ctx := a.frameCtx(time.Now())
	a.handleKey(ctx, KeyEvent{Key: ebiten.KeyNumpad3, Name: keyName(ebiten.KeyNumpad3), Mods: a.mod})
	if a.curWS() != 3 {
		t.Fatalf("keypad: workspace %d", a.curWS())
	}
	a.handleKey(ctx, KeyEvent{Key: ebiten.KeyDigit1, Name: keyName(ebiten.KeyDigit1), Mods: a.mod})
	if a.curWS() != 1 || a.visibleCount() != 1 {
		t.Fatalf("top row: workspace %d", a.curWS())
	}
}

func TestLayoutRoundTripWorkspaces(t *testing.T) {
	t.Setenv("NEONMUX_STATE", t.TempDir())
	a, _ := testApp()
	a.focus = -1
	var ps []Pane
	for i := range 3 {
		h := NewHelpPane(i + 1)
		h.name = "help " + itoa(i+1)
		a.addPane(h)
		ps = append(ps, h)
	}
	ps[2].Base().floating, ps[2].Base().geom = true, floatGeom{X: 1, Y: 1, W: 300, H: 200}
	a.setFocus(1)
	a.layout = "rows"
	a.do("ws.4")
	a.do("ws.move.1") // nothing focused here: nothing moves
	st := a.layoutState()
	if st.Workspace != 4 || st.Panes[0].WS != 1 || st.Panes[0].Garage || st.Workspaces[1].Layout != "rows" {
		t.Fatalf("saved state %+v %+v", st.Panes, st.Workspaces)
	}
	data, _ := json.Marshal(st)
	if err := os.WriteFile(layoutPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	b := &App{cfg: DefaultConfig(), theme: theme.Get("neon-tokyo"), layout: "tiles", ratio: 0.5, ratio2: 0.5}
	if !b.restoreLayout() {
		t.Fatal("restore refused")
	}
	if b.curWS() != 4 || b.visibleCount() != 0 || b.garageCount() != 0 {
		t.Fatalf("workspace %d, %d visible, garage %d", b.curWS(), b.visibleCount(), b.garageCount())
	}
	b.do("ws.1")
	if b.visibleCount() != 3 || b.layout != "rows" || displayName(b.focused()) != "help 2" || !isFloat(b.panes[2]) {
		t.Errorf("%d visible, layout %q, focus %d", b.visibleCount(), b.layout, b.focus)
	}
}
