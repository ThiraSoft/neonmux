package keys

import (
	"testing"

	"github.com/ThiraSoft/neonmux/vt"
)

func TestEncodeTokens(t *testing.T) {
	cases := []struct {
		toks  []string
		modes vt.Mode
		want  string
	}{
		{[]string{"text:ls -la", "enter"}, 0, "ls -la\r"},
		{[]string{"ctrl+c"}, 0, "\x03"},
		{[]string{"up"}, 0, "\x1b[A"},
		{[]string{"up"}, vt.ModeAppCursor, "\x1bOA"},
		{[]string{"J", "j", "space", "/", "+"}, 0, "Jj /+"},
		{[]string{"escape", "tab", "backspace"}, 0, "\x1b\t\x7f"},
		{[]string{"alt+b", "shift+tab", "f1"}, 0, "\x1bb\x1b[Z\x1bOP"},
	}
	for _, c := range cases {
		got, err := EncodeTokens(c.toks, c.modes)
		if err != nil || string(got) != c.want {
			t.Errorf("EncodeTokens(%q) = %q, %v; want %q", c.toks, got, err, c.want)
		}
	}
	for _, bad := range [][]string{{"cmd+e"}, {"hello"}} {
		if _, err := EncodeTokens(bad, 0); err == nil {
			t.Errorf("EncodeTokens(%q) should fail", bad)
		}
	}
}

// fakeLayout simulates a layout: for each macOS virtual key code, the
// character without shift, then with shift.
type fakeLayout map[uint16][2]string

var (
	// US QWERTY: digits, period, comma, slash, B.
	qwerty = fakeLayout{0x12: {"1", "!"}, 0x1c: {"8", "*"}, 0x13: {"2", "@"}, 0x2f: {".", ">"},
		0x2b: {",", "<"}, 0x2c: {"/", "?"}, 0x0b: {"b", "B"}}
	// French AZERTY (PC): the digit row gives & é " ' ( - è _ ç à.
	azerty = fakeLayout{0x12: {"&", "1"}, 0x13: {"é", "2"}, 0x14: {"\"", "3"}, 0x15: {"'", "4"},
		0x17: {"(", "5"}, 0x16: {"-", "6"}, 0x1a: {"è", "7"}, 0x1c: {"_", "8"}, 0x19: {"ç", "9"},
		0x1d: {"à", "0"}, 0x2f: {":", "/"}, 0x2b: {";", "."}, 0x2e: {",", "?"}, 0x0b: {"b", "B"}}
)

func (l fakeLayout) translate(code uint16, shift bool) string {
	c, ok := l[code]
	if !ok {
		return ""
	}
	if shift {
		return c[1]
	}
	return c[0]
}

// useLayout plugs in a simulated layout for the duration of a test.
func useLayout(t *testing.T, l fakeLayout) {
	t.Helper()
	old := keyChar
	keyChar = func(name string, shift bool) string { return layoutChar(name, shift, l.translate) }
	t.Cleanup(func() { keyChar = old })
}

// With option_as_meta, opt+shift+key sends ESC followed by the character
// the key produces with shift, according to the layout: M-> for opt+shift+.
// in QWERTY, M-& for opt+& in AZERTY (the 1 key).
func TestOptionAsMetaFollowsLayout(t *testing.T) {
	cases := []struct {
		l    fakeLayout
		ev   Event
		want string
	}{
		{qwerty, Event{Name: ".", Mods: ModAlt | ModShift}, "\x1b>"},
		{qwerty, Event{Name: ",", Mods: ModAlt | ModShift}, "\x1b<"},
		{qwerty, Event{Name: "1", Mods: ModAlt | ModShift}, "\x1b!"},
		{qwerty, Event{Name: "/", Mods: ModAlt | ModShift}, "\x1b?"},
		{qwerty, Event{Name: "b", Mods: ModAlt}, "\x1bb"},
		{qwerty, Event{Name: "b", Mods: ModAlt | ModShift}, "\x1bB"},
		{azerty, Event{Name: "1", Mods: ModAlt}, "\x1b&"},
		{azerty, Event{Name: "1", Mods: ModAlt | ModShift}, "\x1b1"},
		{azerty, Event{Name: ":", Mods: ModAlt | ModShift}, "\x1b/"},
		{azerty, Event{Name: "7", Mods: ModAlt}, "\x1bè"},
	}
	for _, c := range cases {
		useLayout(t, c.l)
		if got := string(Encode(c.ev, 0, true)); got != c.want {
			t.Errorf("%+v: %q, want %q", c.ev, got, c.want)
		}
	}
}

// Ctrl follows the layout's character: in AZERTY, ctrl+_ (the 8 key) sends
// 0x1f, not 0x7f. A key whose character has no control code keeps the xterm
// convention for digits.
func TestCtrlFollowsLayout(t *testing.T) {
	cases := []struct {
		l    fakeLayout
		ev   Event
		want string
	}{
		{azerty, Event{Name: "8", Mods: ModCtrl}, "\x1f"},
		{azerty, Event{Name: "6", Mods: ModCtrl}, "\x1f"},
		{azerty, Event{Name: "7", Mods: ModCtrl}, "\x1f"},
		{azerty, Event{Name: "2", Mods: ModCtrl}, "\x00"},
		{azerty, Event{Name: "1", Mods: ModCtrl}, ""},
		{qwerty, Event{Name: "8", Mods: ModCtrl}, "\x7f"},
		{qwerty, Event{Name: "2", Mods: ModCtrl}, "\x00"},
		// Outside macOS, the UI provides the character itself.
		{nil, Event{Name: "8", Mods: ModCtrl, Char: "_"}, "\x1f"},
		{nil, Event{Name: "8", Mods: ModCtrl}, "\x7f"},
	}
	for _, c := range cases {
		useLayout(t, c.l)
		if got := string(Encode(c.ev, 0, false)); got != c.want {
			t.Errorf("%+v: %q, want %q", c.ev, got, c.want)
		}
	}
}

// Scripts (send-keys) name characters, not keys: the keyboard layout does
// not change what they send.
func TestEncodeTokensIgnoresLayout(t *testing.T) {
	useLayout(t, azerty)
	got, err := EncodeTokens([]string{"alt+1", "ctrl+8", "alt+shift+."}, 0)
	if err != nil || string(got) != "\x1b1\x7f\x1b." {
		t.Fatalf("%q, %v", got, err)
	}
}

// In keypad application mode (DECKPAM), keypad keys send their SS3
// sequences; outside that mode, they stay text.
func TestKeypadApplicationMode(t *testing.T) {
	cases := []struct {
		ev    Event
		modes vt.Mode
		want  string
	}{
		{Event{Name: "5", Keypad: true}, vt.ModeAppKeypad, "\x1bOu"},
		{Event{Name: "0", Keypad: true}, vt.ModeAppKeypad, "\x1bOp"},
		{Event{Name: ".", Keypad: true}, vt.ModeAppKeypad, "\x1bOn"},
		{Event{Name: "+", Keypad: true}, vt.ModeAppKeypad, "\x1bOk"},
		{Event{Name: "-", Keypad: true}, vt.ModeAppKeypad, "\x1bOm"},
		{Event{Name: "*", Keypad: true}, vt.ModeAppKeypad, "\x1bOj"},
		{Event{Name: "/", Keypad: true}, vt.ModeAppKeypad, "\x1bOo"},
		{Event{Name: "enter", Keypad: true}, vt.ModeAppKeypad, "\x1bOM"},
		{Event{Name: "5", Keypad: true}, 0, ""},
		{Event{Name: "enter", Keypad: true}, 0, "\r"},
		{Event{Name: "5"}, vt.ModeAppKeypad, ""},
		{Event{Name: "5", Keypad: true, Mods: ModCtrl}, vt.ModeAppKeypad, "\x1d"}, // with a modifier: as before
	}
	for _, c := range cases {
		if got := string(Encode(c.ev, c.modes, false)); got != c.want {
			t.Errorf("%+v, modes %b: %q, want %q", c.ev, c.modes, got, c.want)
		}
	}
}

func TestEncodeKitty(t *testing.T) {
	k := vt.ModeKittyKeys
	cases := []struct {
		ev   Event
		want string
	}{
		{Event{Name: "escape"}, "\x1b[27u"},
		{Event{Name: "enter"}, "\r"},
		{Event{Name: "enter", Mods: ModShift}, "\x1b[13;2u"},
		{Event{Name: "tab"}, "\t"},
		{Event{Name: "tab", Mods: ModShift}, "\x1b[9;2u"},
		{Event{Name: "tab", Mods: ModCtrl}, "\x1b[9;5u"},
		{Event{Name: "backspace", Mods: ModAlt}, "\x1b[127;3u"},
		{Event{Name: "space", Mods: ModCtrl}, "\x1b[32;5u"},
		{Event{Name: "i", Mods: ModCtrl}, "\x1b[105;5u"},
		{Event{Name: "a", Mods: ModCtrl | ModShift}, "\x1b[97;6u"},
		{Event{Name: "up", Mods: ModCtrl}, "\x1b[1;5A"},
		{Event{Name: "up"}, "\x1b[A"},
		{Event{Name: "f1"}, "\x1b[P"},
		{Event{Name: "f3"}, "\x1b[13~"},
		{Event{Name: "f3", Mods: ModShift}, "\x1b[13;2~"},
		{Event{Name: "f5"}, "\x1b[15~"},
	}
	for _, c := range cases {
		if got := string(encode(c.ev, k, true, false)); got != c.want {
			t.Errorf("%+v = %q, want %q", c.ev, got, c.want)
		}
	}
	// Text keys still go out as text, shift included.
	for _, ev := range []Event{{Name: "a"}, {Name: "a", Mods: ModShift}, {Name: "space"}, {Name: "space", Mods: ModShift}} {
		if got := encode(ev, k, true, false); got != nil {
			t.Errorf("%+v = %q, want text", ev, got)
		}
	}
	got, err := EncodeTokens([]string{"alt+b", "escape"}, k)
	if err != nil || string(got) != "\x1b[98;3u\x1b[27u" {
		t.Errorf("EncodeTokens = %q, %v", got, err)
	}
}
