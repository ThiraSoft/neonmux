package ui

import (
	"strings"
	"testing"
)

func TestJoinHints(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"cmd+h", "cmd+j", "cmd+k", "cmd+l"}, "cmd+h/j/k/l"},
		{[]string{"cmd+shift+h", "cmd+shift+l"}, "cmd+shift+h/l"},
		{[]string{"cmd+t", "cmd+shift+t"}, "cmd+t  cmd+shift+t"},
		{[]string{"cmd+=", "", "cmd+0"}, "cmd+=/0"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := joinHints(c.in); got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}

func helpApp(bindings map[string]string) *App {
	a := &App{cfg: DefaultConfig(), mod: ModMeta}
	a.cfg.Bindings = bindings
	a.buildBindings()
	return a
}

// Behind the leader, every shortcut does the same thing as with mod, as
// the README promises. Only "," renames the pane, tmux style (it is
// documented). A leader shortcut that closes a pane instead of toggling the
// CRT would kill an agent at work.
func TestLeaderMirrorsMod(t *testing.T) {
	a := helpApp(nil)
	except := map[string]bool{"leader+,": true}
	for _, b := range defaultBindings {
		name := "leader+" + strings.TrimPrefix(b[0], "mod+")
		if except[name] {
			continue
		}
		if got := a.bindings[Combo{Name: name}]; got != b[1] {
			t.Errorf("%s: %q, want %q like %s", name, got, b[1], b[0])
		}
	}
}

func findRow(rows []helpRow, desc string) (helpRow, bool) {
	for _, r := range rows {
		if strings.HasPrefix(r.desc, desc) {
			return r, true
		}
	}
	return helpRow{}, false
}

func TestHelpRowsFollowConfig(t *testing.T) {
	a := helpApp(map[string]string{"mod+y": "fx.confetti", "mod+shift+f": "pane.zoom"})
	rows := a.helpRows()
	// mod+shift+f taken over by the config: the help shows the leader for floating.
	r, ok := findRow(rows, "float the pane")
	if !ok || r.keys != a.leader.String()+" shift+f" {
		t.Errorf("pane.float: %+v", r)
	}
	if r, ok := findRow(rows, "fx.confetti"); !ok || r.keys != (Combo{Mods: ModMeta, Name: "y"}).String() {
		t.Errorf("shortcut from the config missing: %+v", r)
	}
	if r, ok := findRow(rows, "focus the neighbor"); !ok || !strings.HasSuffix(r.keys, "+h/j/k/l") {
		t.Errorf("grouped focus: %+v", r)
	}
}

func TestHelpFilterKeepsSections(t *testing.T) {
	h := NewHelpPane(1)
	h.rows = helpApp(nil).helpRows()
	h.visible = 10
	h.filter = []rune("corners")
	h.applyFilter()
	if len(h.view) != 3 || h.view[0].section != "Floating panes" {
		t.Fatalf("filter \"corners\": %+v", h.view)
	}
	for _, r := range h.view[1:] {
		if !strings.Contains(r.desc, "corners") {
			t.Errorf("line without \"corners\": %+v", r)
		}
	}
	// A matching section keeps all its lines.
	h.filter = []rune("explorer")
	h.applyFilter()
	if n := h.matches(); n < 6 {
		t.Errorf("filter \"explorer\": %d lines", n)
	}
	h.Chars(nil, []rune("q"))
	if !h.closed {
		t.Error("q must close the help")
	}
}

func TestFullscreenHint(t *testing.T) {
	// On Linux, mod+ctrl+f is ctrl+shift+f, already taken by zoom: f11.
	a := &App{cfg: DefaultConfig(), mod: ModCtrl | ModShift}
	a.buildBindings()
	if got := a.hintFor("window.fullscreen"); got != "f11" {
		t.Errorf("Linux: %q, want f11", got)
	}
	c, _ := ParseCombo("mod+f", a.mod)
	if a.bindings[c] != "pane.zoom" {
		t.Error("zoom must keep mod+f")
	}
	a.mod = ModMeta
	a.buildBindings()
	if got, want := a.hintFor("window.fullscreen"), (Combo{Mods: ModMeta | ModCtrl, Name: "f"}).String(); got != want {
		t.Errorf("macOS: %q, want %q", got, want)
	}
}
