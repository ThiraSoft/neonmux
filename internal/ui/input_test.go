package ui

import (
	"image"
	"strings"
	"testing"

	"github.com/ThiraSoft/neonmux/vt"
)

func TestEncodeKey(t *testing.T) {
	cases := []struct {
		ev    KeyEvent
		modes vt.Mode
		want  string
	}{
		{KeyEvent{Name: "enter"}, 0, "\r"},
		{KeyEvent{Name: "backspace"}, 0, "\x7f"},
		{KeyEvent{Name: "up"}, 0, "\x1b[A"},
		{KeyEvent{Name: "up"}, vt.ModeAppCursor, "\x1bOA"}, // vim turns on application cursor mode
		{KeyEvent{Name: "right", Mods: ModCtrl}, 0, "\x1b[1;5C"},
		{KeyEvent{Name: "tab", Mods: ModShift}, 0, "\x1b[Z"},
		{KeyEvent{Name: "delete"}, 0, "\x1b[3~"},
		{KeyEvent{Name: "f5"}, 0, "\x1b[15~"},
		{KeyEvent{Name: "f1"}, 0, "\x1bOP"},
		{KeyEvent{Name: "w", Mods: ModCtrl}, 0, "\x17"},
		{KeyEvent{Name: "[", Mods: ModCtrl}, 0, "\x1b"},
		{KeyEvent{Name: "space", Mods: ModCtrl}, 0, "\x00"},
		{KeyEvent{Name: "a"}, 0, ""}, // letters go through the typed characters
	}
	for _, c := range cases {
		if got := string(EncodeKey(c.ev, c.modes, false)); got != c.want {
			t.Errorf("%+v: %q, want %q", c.ev, got, c.want)
		}
	}
}

// shift+enter inserts a newline in Claude Code, and only there: elsewhere
// it stays a plain enter.
func TestShiftEnterForClaude(t *testing.T) {
	shiftEnter := KeyEvent{Name: "enter", Mods: ModShift}
	if got := string(claudeKey(shiftEnter)); got != "\x1b\r" {
		t.Errorf("shift+enter for Claude: %q", got)
	}
	for _, ev := range []KeyEvent{{Name: "enter"}, {Name: "enter", Mods: ModShift | ModCtrl}, {Name: "tab", Mods: ModShift}} {
		if b := claudeKey(ev); b != nil {
			t.Errorf("%+v: %q, want the usual encoding", ev, b)
		}
	}
	if got := string(EncodeKey(shiftEnter, 0, false)); got != "\r" {
		t.Errorf("shift+enter outside Claude: %q", got)
	}
	p := NewTermPane(1, nil, "", nil)
	if p.isClaude() {
		t.Error("a terminal without a program is not Claude")
	}
	p.fg = "claude"
	if !p.isClaude() {
		t.Error("claude in the foreground")
	}
	p.fg = "2.1.304"
	p.hasProg, p.prog.App = true, "claude-code"
	if !p.isClaude() {
		t.Error("Claude announced by OSC 7501")
	}
}

func TestParseCombo(t *testing.T) {
	c, err := ParseCombo("mod+shift+h", ModMeta)
	if err != nil || c.Mods != ModMeta|ModShift || c.Name != "h" {
		t.Fatalf("mod+shift+h → %+v %v", c, err)
	}
	c, err = ParseCombo("mod++", ModMeta)
	if err != nil || c.Name != "+" {
		t.Fatalf("mod++ → %+v %v", c, err)
	}
	if _, err := ParseCombo("hyper+x", 0); err == nil {
		t.Fatal("unknown modifier accepted")
	}
}

func TestFuzzyIgnoresAccents(t *testing.T) {
	if _, ok := fuzzy("theme matrix", "Thème: Matrix"); !ok {
		t.Fatal("search must ignore accents")
	}
	a, _ := fuzzy("nt", "New terminal")
	b, _ := fuzzy("nt", "Copy mode (vim-style history)")
	if a <= b {
		t.Fatalf("word starts must rank first: %d <= %d", a, b)
	}
}

func TestTile(t *testing.T) {
	r := tile("tiles", 3, rect(0, 0, 100, 100), 0.5, 0.5)
	if r[0] != rect(0, 0, 50, 100) || r[1] != rect(50, 0, 100, 50) || r[2] != rect(50, 50, 100, 100) {
		t.Fatalf("3 panes: %v", r)
	}
	if n := neighbor(r, 0, "right"); n != 1 {
		t.Fatalf("right neighbor = %d", n)
	}
	if n := neighbor(r, 1, "down"); n != 2 {
		t.Fatalf("neighbor below = %d", n)
	}
}

func TestSanitizePaste(t *testing.T) {
	// The ESC goes away, the rest of the text stays visible as is.
	if got := SanitizePaste("ls\x1b[201~; rm -rf /\n"); got != "ls[201~; rm -rf /\r" {
		t.Fatalf("paste = %q", got)
	}
}

func rect(x0, y0, x1, y1 int) image.Rectangle { return image.Rect(x0, y0, x1, y1) }

func TestRelaunchArgs(t *testing.T) {
	got := relaunchArgs([]string{"-config", "/x.json", "-script", "wait:1s|cmd+p", "-screenshot=/a.png",
		"-wait", "3s", "-theme", "abyss", "-fresh", "-e", "nvim", "-psn_0_123", "--persist"})
	want := []string{"-config", "/x.json", "-theme", "abyss", "--persist"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("relaunch: %q", got)
	}
}
