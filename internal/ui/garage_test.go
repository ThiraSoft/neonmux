package ui

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

func TestParkMovesToGarage(t *testing.T) {
	a, ps := testApp(false, false, false, true)
	a.setFocus(1)
	a.do("pane.park")
	if !isParked(ps[1]) || ps[1].(*fakePane).closed {
		t.Fatal("the pane must be in the garage, still alive")
	}
	// Garage at the end, the focus moves to the neighboring tile.
	if want := []Pane{ps[0], ps[2], ps[3], ps[1]}; !slices.Equal(a.panes, want) {
		t.Errorf("panes %v", ids(a.panes))
	}
	if a.focused() != ps[2] || a.visibleCount() != 3 {
		t.Errorf("focus %d, %d visible", a.focus, a.visibleCount())
	}
	if a.countPanes(false) != 2 || a.countPanes(true) != 1 || isTile(ps[1]) || isFloat(ps[1]) {
		t.Error("a pane in the garage counts neither as a tile nor as floating")
	}
	if slices.Contains(a.drawOrder(), ps[1]) {
		t.Error("a pane in the garage is not drawn")
	}
	// mod+4 points at a number in the bottom bar: there are only 3.
	a.do("focus.4")
	if a.focused() != ps[2] {
		t.Errorf("mod+4 must not reach the garage (focus %d)", a.focus)
	}
	// A floating pane sent to the garage leaves the focus on the last focused tile.
	a.setFocus(2)
	a.park(ps[3])
	if a.focused() != ps[2] || !isParked(ps[3]) || ps[3].Base().floating {
		t.Errorf("focus %d after the floating pane", a.focus)
	}
}

func TestParkLastVisiblePane(t *testing.T) {
	a, ps := testApp(false)
	a.focus = 0
	a.park(ps[0])
	if a.focus != -1 || a.focused() != nil || a.visibleCount() != 0 {
		t.Errorf("with no visible pane, no pane is focused: focus %d", a.focus)
	}
	a.visit(ps[0])
	if a.focused() != ps[0] || a.visitor != ps[0] {
		t.Error("the pane must come out again")
	}
}

func TestVisitAndSendBack(t *testing.T) {
	a, ps := testApp(false, false, false)
	a.setFocus(2)
	a.park(ps[0])
	a.park(ps[1])
	a.setFocus(slices.Index(a.panes, ps[2]))
	// Out: in the center, floating, on top, without touching the tiles.
	a.visit(ps[1])
	if a.visitor != ps[1] || !isFloat(ps[1]) || isParked(ps[1]) || a.focused() != ps[1] {
		t.Fatalf("visitor %v, focus %d", a.visitor, a.focus)
	}
	if g := ps[1].Base().geom; g.X != 0.5 || g.Y != 0.5 {
		t.Errorf("the visitor must be in the center: %+v", g)
	}
	if want := []Pane{ps[2], ps[1], ps[0]}; !slices.Equal(a.panes, want) {
		t.Errorf("the visitor takes a number after the visible panes: %v", ids(a.panes))
	}
	if a.countPanes(true) != 0 {
		t.Error("the visitor does not count toward the floating limit")
	}
	// Only one visitor: the next one sends the first back to the garage, and
	// going back still leads to the original pane.
	a.visit(ps[0])
	if !isParked(ps[1]) || a.visitor != ps[0] || a.visitorBack != ps[2] {
		t.Fatalf("visitor %v, back %v", a.visitor, a.visitorBack)
	}
	a.do("focus.back")
	if !isParked(ps[0]) || a.visitor != nil || a.focused() != ps[2] {
		t.Errorf("mod+shift+n: visitor %v, focus %d", a.visitor, a.focus)
	}
	if ps[0].Base().geom.X == 0.5 && ps[0].Base().geom.W == 1 {
		t.Error("when it goes back, the pane gets back its previous floating spot")
	}
}

func TestParkVisitorSendsBack(t *testing.T) {
	a, ps := testApp(false, false)
	a.setFocus(1)
	a.park(ps[0])
	a.visit(ps[0])
	a.do("pane.park") // mod+m on the visitor: same effect as mod+shift+n
	if !isParked(ps[0]) || a.visitor != nil || a.focused() != ps[1] {
		t.Errorf("focus %d, visitor %v", a.focus, a.visitor)
	}
}

func TestKeepVisitor(t *testing.T) {
	// A free tile: the visitor stays there.
	a, ps := testApp(false, false)
	a.setFocus(1)
	a.park(ps[0])
	a.visit(ps[0])
	a.do("pane.float")
	if a.visitor != nil || !isTile(ps[0]) || isParked(ps[0]) {
		t.Errorf("tile expected: visitor %v, tile %v", a.visitor, isTile(ps[0]))
	}
	// Tiles full: it stays floating, but is no longer a visitor.
	a, ps = testApp(false, false, false, false, false)
	a.setFocus(4)
	a.park(ps[4])
	a.addPane(&fakePane{})
	a.visit(ps[4])
	a.do("pane.float")
	if a.visitor != nil || !isFloat(ps[4]) || isParked(ps[4]) {
		t.Errorf("floating expected: visitor %v", a.visitor)
	}
	a.do("focus.back")
	if isParked(ps[4]) {
		t.Error("kept on screen, it no longer goes back to the garage with mod+shift+n")
	}
}

func TestCloseVisitorReturnsFocus(t *testing.T) {
	a, ps := testApp(false, false)
	a.setFocus(0)
	a.park(ps[1])
	a.visit(ps[1])
	a.closePane(slices.Index(a.panes, ps[1]))
	if a.visitor != nil || a.focused() != ps[0] {
		t.Errorf("focus %d, visitor %v", a.focus, a.visitor)
	}
	// Closing a pane in the garage leaves the focus alone and does not fade it out on screen.
	a, ps = testApp(false, false, false)
	a.setFocus(2)
	a.park(ps[0])
	a.closePane(slices.Index(a.panes, ps[0]))
	if a.focused() != ps[2] || len(a.closing) != 0 || !ps[0].(*fakePane).closed {
		t.Errorf("focus %d, %d closing", a.focus, len(a.closing))
	}
}

func TestNextAttentionVisitsGarage(t *testing.T) {
	t0 := time.Now()
	tile := agentPane(1, vt.PSWorking, t0, false)
	parked := agentPane(2, vt.PSBlocked, t0.Add(-time.Minute), false)
	a := &App{cfg: DefaultConfig(), theme: theme.Get("neon-tokyo"), panes: []Pane{tile, parked}}
	a.setFocus(0)
	a.park(parked)
	a.nextAttention()
	if a.visitor != parked || a.focused() != parked {
		t.Fatalf("the blocked agent in the garage must come out: visitor %v", a.visitor)
	}
	a.focusBack()
	if !isParked(parked) || a.focused() != tile {
		t.Errorf("back: focus %d", a.focus)
	}
}

// mod+n cycles through every waiting agent, instead of coming back to the
// first two over and over when nobody answers them.
func TestNextAttentionCyclesThroughAll(t *testing.T) {
	t0 := time.Now()
	tile := agentPane(1, vt.PSWorking, t0, false)
	b1 := agentPane(2, vt.PSBlocked, t0.Add(-3*time.Minute), false)
	b2 := agentPane(3, vt.PSBlocked, t0.Add(-2*time.Minute), false)
	b3 := agentPane(4, vt.PSBlocked, t0.Add(-time.Minute), false)
	a := &App{cfg: DefaultConfig(), theme: theme.Get("neon-tokyo"), panes: []Pane{tile, b1, b2, b3}}
	a.setFocus(0)
	for _, p := range []Pane{b1, b2, b3} {
		a.park(p)
	}
	for i, want := range []Pane{b1, b2, b3, b1} {
		a.nextAttention()
		if a.focused() != want {
			t.Fatalf("mod+n #%d: focus on pane %d, want pane %d",
				i+1, a.focused().Base().id, want.Base().id)
		}
	}
}

func TestGarageSummary(t *testing.T) {
	t0 := time.Now()
	a := &App{cfg: DefaultConfig(), theme: theme.Get("neon-tokyo")}
	if s, _, _ := a.garageSummary(); s != "" {
		t.Errorf("empty garage: %q", s)
	}
	visible := agentPane(1, vt.PSBlocked, t0, false)
	done := agentPane(2, vt.PSDone, t0, false)
	working := agentPane(3, vt.PSWorking, t0, false)
	a.panes = []Pane{visible, done, working}
	a.focus = 0
	a.park(done)
	a.park(working)
	if s, n, blocked := a.garageSummary(); s != "garage 2 · 1 waiting" || n != 1 || blocked {
		t.Errorf("%q, %d, %v", s, n, blocked)
	}
	working.prog.State = vt.PSBlocked
	if s, _, blocked := a.garageSummary(); s != "garage 2 · 2 waiting" || !blocked {
		t.Errorf("%q, blocked %v", s, blocked)
	}
}

func TestPaneTag(t *testing.T) {
	a, ps := testApp(false, false)
	a.focus = 0
	a.park(ps[0])
	if a.paneTag(ps[0]) != "[garage]" || a.paneTag(ps[1]) != "[1]" {
		t.Errorf("%q %q", a.paneTag(ps[0]), a.paneTag(ps[1]))
	}
}

func TestPickerListsAllPanes(t *testing.T) {
	t0 := time.Now()
	shell := NewTermPane(1, nil, "/tmp/project", nil)
	idle := agentPane(2, vt.PSIdle, t0, false)
	blocked := agentPane(3, vt.PSBlocked, t0, false)
	a := &App{cfg: DefaultConfig(), theme: theme.Get("neon-tokyo"), panes: []Pane{shell, idle, blocked}}
	a.focus = 1
	a.park(shell)
	items := a.pickerItems()
	if len(items) != 3 {
		t.Fatalf("%d items", len(items))
	}
	// Agents first, by urgency; the terminal in the garage can still be found.
	if items[0].action != "focus.id:3" || items[1].action != "focus.id:2" || items[2].action != "focus.id:1" {
		t.Errorf("order %v", items)
	}
	if !strings.HasPrefix(items[2].hint, "garage") || !strings.Contains(items[2].label, "project") {
		t.Errorf("terminal in the garage: %+v", items[2])
	}
	// Picking it brings it out in the center.
	a.do(items[2].action)
	if a.visitor != shell {
		t.Error("picking a pane in the garage must bring it out")
	}
}

func TestPickerCloseKey(t *testing.T) {
	a, ps := testApp(false, false)
	a.mod = ModMeta
	a.buildBindings()
	a.focus = 0
	a.park(ps[1])
	a.openAgentPicker()
	a.palette.chars([]rune("·")) // keeps only the pane in the garage, which has no number
	if len(a.palette.items) != 1 {
		t.Fatalf("%d filtered items", len(a.palette.items))
	}
	a.palette.key(KeyEvent{Name: "w", Mods: ModMeta})
	if !ps[1].(*fakePane).closed || len(a.panes) != 1 || a.palette == nil {
		t.Errorf("mod+w must close the picked pane: %d panes", len(a.panes))
	}
	if len(a.palette.items) != 0 {
		t.Errorf("the list must follow: %d items", len(a.palette.items))
	}
}

func TestFocusIDAction(t *testing.T) {
	a, ps := testApp(false, false, false)
	a.setFocus(0)
	a.do("focus.id:3")
	if a.focused() != ps[2] {
		t.Errorf("focus.id:3: focus %d", a.focus)
	}
}

func TestPaneCap(t *testing.T) {
	a, _ := testApp()
	for range maxTotalPanes + 3 {
		a.addPane(&fakePane{})
	}
	if len(a.panes) != maxTotalPanes || a.visibleCount() != a.cfg.MaxPanes {
		t.Errorf("%d panes, %d visible", len(a.panes), a.visibleCount())
	}
}

func TestGarageBindingsLinux(t *testing.T) {
	// On Linux, mod is ctrl+shift: mod+shift+n is the same as mod+n, so
	// sending back to the garage goes through the leader (shift+n), as the
	// help and the toasts say.
	a := &App{cfg: DefaultConfig(), mod: ModCtrl | ModShift}
	a.buildBindings()
	c, _ := ParseCombo("mod+m", a.mod)
	if got := a.bindings[c]; got != "pane.park" {
		t.Errorf("mod+m: %q", got)
	}
	if got, want := a.hintFor("focus.back"), a.leader.String()+" shift+n"; got != want {
		t.Errorf("back to the garage: %q, want %q", got, want)
	}
	if got := a.bindings[Combo{Name: "leader+m"}]; got != "pane.park" {
		t.Errorf("leader m: %q", got)
	}
}

func TestSendBackGivesFocusFully(t *testing.T) {
	a, ps := testApp(false, false)
	a.setFocus(0)
	a.park(ps[1])
	a.visit(ps[1])
	ps[0].Base().notify = "bell" // the tile rings while the user answers the visitor
	a.do("focus.back")
	if a.focused() != ps[0] || ps[0].Base().notify != "" {
		t.Errorf("focus %d, notification %q: going back must read the notification", a.focus, ps[0].Base().notify)
	}
}

func TestVisitorNeverBecomesPrevFocus(t *testing.T) {
	t1, pa, pb := &fakePane{}, &fakePane{}, &fakePane{}
	t1.id, pa.id, pb.id = 1, 2, 3
	a := &App{cfg: DefaultConfig(), theme: theme.Get("neon-tokyo"), panes: []Pane{t1, pa, pb}}
	a.setFocus(0)
	a.park(pa)
	a.park(pb)
	a.visit(pa)
	a.visit(pb) // A goes back, B comes out
	a.do("pane.float")
	a.do("focus.back")
	if a.focused() != t1 || !isParked(pa) {
		t.Errorf("focus %d: expected back on the original tile, without bringing A out again", a.focus)
	}
}

func TestParkedSingletonShortcut(t *testing.T) {
	a, _ := testApp(false)
	a.focus = 0
	h := NewHelpPane(9)
	h.floating = true
	a.addPane(h)
	a.park(h)
	a.toggleHelp() // the help shortcut brings it out of the garage
	if a.visitor != h || a.focused() != Pane(h) {
		t.Fatalf("help: visitor %v, focus %d", a.visitor, a.focus)
	}
	a.toggleHelp() // a second time: it closes
	if slices.Contains(a.panes, Pane(h)) {
		t.Error("the second press must close the help")
	}
}

func TestLayoutRoundTripGarage(t *testing.T) {
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
	home := floatGeom{X: 1, Y: 0, W: 300, H: 200}
	ps[1].Base().floating, ps[1].Base().geom = true, home
	a.park(ps[1])
	a.park(ps[2])
	a.visit(ps[1])
	st := a.layoutState()
	if !st.Panes[1].Visitor || st.Panes[1].Home == nil || *st.Panes[1].Home != home || !st.Panes[2].Garage {
		t.Fatalf("saved state %+v", st.Panes)
	}
	// An old layout with more tiles than max_panes: the extra ones go to the
	// garage instead of disappearing.
	for i := range 4 {
		st.Panes = append(st.Panes, paneState{Kind: "agents", Name: "old " + itoa(i)})
	}
	data, _ := json.Marshal(st)
	if err := os.WriteFile(layoutPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	b := &App{cfg: DefaultConfig(), theme: theme.Get("neon-tokyo")}
	if !b.restoreLayout() {
		t.Fatal("restore refused")
	}
	if len(b.panes) != 7 || b.visibleCount() != 5 {
		t.Fatalf("%d panes, %d visible", len(b.panes), b.visibleCount())
	}
	v := b.visitor
	if v == nil || displayName(v) != "help 2" || b.visitorGeom != home || b.countPanes(false) != 4 {
		t.Fatalf("visitor %v, spot %+v, %d tiles", v, b.visitorGeom, b.countPanes(false))
	}
	for _, p := range b.panes[b.visibleCount():] {
		if !isParked(p) {
			t.Errorf("%s should be in the garage", displayName(p))
		}
	}
	b.focus = slices.Index(b.panes, v)
	b.sendBack()
	if v.Base().geom != home {
		t.Errorf("when it goes back, the visitor gets its spot back: %+v", v.Base().geom)
	}
}
