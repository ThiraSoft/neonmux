// Package keys parses key combinations ("ctrl+space", "mod+1") and
// translates them into bytes for the program in a terminal. It is shared by
// the window and the daemon (neonmux send-keys).
package keys

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ThiraSoft/neonmux/vt"
)

// Mods is the state of the modifier keys.
type Mods uint8

const (
	ModShift Mods = 1 << iota
	ModCtrl
	ModAlt
	ModMeta
)

// IsPrintableName reports a key that normally produces a character.
func IsPrintableName(n string) bool {
	return len([]rune(n)) == 1
}

// Combo is the key combination of a shortcut.
type Combo struct {
	Mods Mods
	Name string
}

func (c Combo) String() string {
	var parts []string
	if c.Mods&ModCtrl != 0 {
		parts = append(parts, "ctrl")
	}
	if c.Mods&ModAlt != 0 {
		parts = append(parts, alt())
	}
	if c.Mods&ModMeta != 0 {
		parts = append(parts, meta())
	}
	if c.Mods&ModShift != 0 {
		parts = append(parts, "shift")
	}
	parts = append(parts, c.Name)
	return strings.Join(parts, "+")
}

// Modifiers are shown as words (cmd+shift+n): the cmd and option glyphs
// are hard to read in small fonts, and some fonts (Iosevka) cut them off.
// It is also the config syntax.
func meta() string {
	if runtime.GOOS == "darwin" {
		return "cmd"
	}
	return "super"
}

func alt() string {
	if runtime.GOOS == "darwin" {
		return "opt"
	}
	return "alt"
}

// ParseCombo parses "ctrl+space", "cmd+shift+h" or "mod+1".
func ParseCombo(s string, mod Mods) (Combo, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return Combo{}, fmt.Errorf("empty shortcut")
	}
	var c Combo
	parts := strings.Split(s, "+")
	// "mod++": the last "+" is the key.
	if strings.HasSuffix(s, "++") {
		parts = append(strings.Split(strings.TrimSuffix(s, "++"), "+"), "+")
	}
	for i, p := range parts {
		if i == len(parts)-1 {
			c.Name = p
			break
		}
		switch p {
		case "mod":
			c.Mods |= mod
		case "cmd", "super", "meta", "win":
			c.Mods |= ModMeta
		case "ctrl", "control":
			c.Mods |= ModCtrl
		case "alt", "opt", "option":
			c.Mods |= ModAlt
		case "shift":
			c.Mods |= ModShift
		default:
			return Combo{}, fmt.Errorf("unknown modifier %q", p)
		}
	}
	return c, nil
}

// ParseMods parses modifiers alone ("cmd", "ctrl+shift").
func ParseMods(s string) Mods {
	c, err := ParseCombo(s+"+x", 0)
	if err != nil {
		return ModMeta
	}
	return c.Mods
}

// Event is a key to translate: its name ("enter", "up", "a") and its
// modifiers.
type Event struct {
	Name string
	Mods Mods
	// Keypad flags a numeric keypad key; Name is then the key's character
	// ("1", "+", ".") or "enter".
	Keypad bool
	// Char is the character the key produces in the active layout, without
	// modifiers (ebiten.KeyName), when Name follows the physical position:
	// in AZERTY, the 8 key has Name "8" and Char "_". When empty, Encode asks
	// the system for it if it can.
	Char string
}

// keyChar returns the character that the key named name produces in the
// active layout, with or without shift, without option or ctrl, or "" if
// the system cannot tell. macOS plugs it in (layout_darwin.go); elsewhere,
// only Event.Char tells about the layout. Replaced in the tests.
var keyChar = func(name string, shift bool) string { return "" }

// keypadSS3 gives the final letter of the SS3 sequence (ESC O x) of each
// numeric keypad key in application mode.
var keypadSS3 = map[string]byte{
	"0": 'p', "1": 'q', "2": 'r', "3": 's', "4": 't', "5": 'u', "6": 'v', "7": 'w', "8": 'x', "9": 'y',
	".": 'n', ",": 'l', "+": 'k', "-": 'm', "*": 'j', "/": 'o', "=": 'X', "enter": 'M',
}

// Encode translates a keyboard key into bytes for the terminal's program.
// It returns nil for keys that produce text, which is sent separately
// through the typed characters.
//
// Digits here stand for a physical position (in AZERTY, the 1 key produces
// "&"): with ctrl, or option as meta, Encode asks the active layout for the
// key's real character.
func Encode(ev Event, modes vt.Mode, optionAsMeta bool) []byte {
	return encode(ev, modes, optionAsMeta, true)
}

// encode does the work of Encode. layout is false for scripts, whose names
// are characters, not keys.
func encode(ev Event, modes vt.Mode, optionAsMeta, layout bool) []byte {
	// char returns the key's character in the active layout, or "" if it is
	// unknown.
	char := func(shift bool) string {
		switch {
		case !layout:
			return ""
		case !shift && !isDigit(ev.Name):
			return ev.Name // the name already is the layout's character
		}
		if c := keyChar(ev.Name, shift); c != "" {
			return c
		}
		if !shift {
			return ev.Char
		}
		return ""
	}
	if ev.Keypad && modes&vt.ModeAppKeypad != 0 && ev.Mods == 0 {
		if f, ok := keypadSS3[ev.Name]; ok {
			return []byte{0x1b, 'O', f}
		}
	}
	m := 1
	if ev.Mods&ModShift != 0 {
		m += 1
	}
	if ev.Mods&ModAlt != 0 {
		m += 2
	}
	if ev.Mods&ModCtrl != 0 {
		m += 4
	}
	appCursor := modes&vt.ModeAppCursor != 0
	metaAlt := ev.Mods&ModAlt != 0 && (optionAsMeta || runtime.GOOS != "darwin")
	csi := func(final byte) []byte {
		if m > 1 {
			return []byte(fmt.Sprintf("\x1b[1;%d%c", m, final))
		}
		if appCursor {
			return []byte{0x1b, 'O', final}
		}
		return []byte{0x1b, '[', final}
	}
	tilde := func(n int) []byte {
		if m > 1 {
			return []byte(fmt.Sprintf("\x1b[%d;%d~", n, m))
		}
		return []byte(fmt.Sprintf("\x1b[%d~", n))
	}

	// kitty keyboard protocol, flag 1 (vt.ModeKittyKeys): a key with
	// modifiers that legacy encoding would confuse with another goes out as
	// CSI code ; mods u, code being the key's unshifted character.
	kitty := modes&vt.ModeKittyKeys != 0
	csiU := func(code int) []byte {
		if m > 1 {
			return []byte(fmt.Sprintf("\x1b[%d;%du", code, m))
		}
		return []byte(fmt.Sprintf("\x1b[%du", code))
	}
	if kitty {
		switch ev.Name {
		case "escape":
			return csiU(27)
		case "enter", "tab", "backspace", "space":
			if m > 1 && !(ev.Name == "space" && m == 2) {
				return csiU(map[string]int{"enter": 13, "tab": 9, "backspace": 127, "space": 32}[ev.Name])
			}
		case "f1", "f2", "f4":
			if m == 1 {
				return []byte{0x1b, '[', map[string]byte{"f1": 'P', "f2": 'Q', "f4": 'S'}[ev.Name]}
			}
		case "f3":
			// CSI R would read as a cursor position report.
			return tilde(13)
		}
		if IsPrintableName(ev.Name) && (ev.Mods&ModCtrl != 0 || metaAlt) {
			c := char(false)
			if c == "" {
				c = ev.Name
			}
			if r, _ := utf8.DecodeRuneInString(strings.ToLower(c)); r != utf8.RuneError {
				return csiU(int(r))
			}
		}
	}

	switch ev.Name {
	case "enter":
		if ev.Mods&ModAlt != 0 {
			return []byte("\x1b\r")
		}
		return []byte("\r")
	case "tab":
		if ev.Mods&ModShift != 0 {
			return []byte("\x1b[Z")
		}
		return []byte("\t")
	case "backspace":
		switch {
		case ev.Mods&ModCtrl != 0:
			return []byte{0x08}
		case ev.Mods&ModAlt != 0:
			return []byte{0x1b, 0x7f}
		}
		return []byte{0x7f}
	case "escape":
		return []byte{0x1b}
	case "up":
		return csi('A')
	case "down":
		return csi('B')
	case "right":
		return csi('C')
	case "left":
		return csi('D')
	case "home":
		return csi('H')
	case "end":
		return csi('F')
	case "insert":
		return tilde(2)
	case "delete":
		return tilde(3)
	case "pageup":
		return tilde(5)
	case "pagedown":
		return tilde(6)
	case "space":
		if ev.Mods&ModCtrl != 0 {
			return []byte{0}
		}
		return nil
	}
	if strings.HasPrefix(ev.Name, "f") && len(ev.Name) > 1 {
		if n, err := strconv.Atoi(ev.Name[1:]); err == nil && n >= 1 && n <= 12 {
			if n <= 4 {
				if m > 1 {
					return []byte(fmt.Sprintf("\x1b[1;%d%c", m, 'P'+n-1))
				}
				return []byte{0x1b, 'O', byte('P' + n - 1)}
			}
			codes := [...]int{15, 17, 18, 19, 20, 21, 23, 24}
			return tilde(codes[n-5])
		}
	}

	if !IsPrintableName(ev.Name) {
		return nil
	}
	if ev.Mods&ModCtrl != 0 {
		// The layout's character first (ctrl+_ on the 8 key in AZERTY), then
		// the xterm convention for digits.
		c, ok := ctrlByte(char(false))
		if !ok {
			c, ok = ctrlByte(ev.Name)
		}
		if ok {
			if metaAlt {
				return []byte{0x1b, c}
			}
			return []byte{c}
		}
		return nil
	}
	if metaAlt && optionAsMeta {
		shift := ev.Mods&ModShift != 0
		name := char(shift)
		if name == "" {
			name = ev.Name
			if shift {
				name = strings.ToUpper(name)
			}
		}
		return append([]byte{0x1b}, name...)
	}
	return nil
}

// ctrlByte returns the control code of ctrl+s, if it has one.
func ctrlByte(s string) (byte, bool) {
	if len(s) != 1 {
		return 0, false
	}
	switch n := s[0]; {
	case n >= 'a' && n <= 'z':
		return n - 'a' + 1, true
	case n == '@' || n == '2':
		return 0, true
	case n == '[' || n == '3':
		return 0x1b, true
	case n == '\\' || n == '4':
		return 0x1c, true
	case n == ']' || n == '5':
		return 0x1d, true
	case n == '^' || n == '6':
		return 0x1e, true
	case n == '_' || n == '-' || n == '7' || n == '/':
		return 0x1f, true
	case n == '8' || n == '?':
		return 0x7f, true
	}
	return 0, false
}

// EncodeTokens translates keys in script format ("text:ls -la", "enter",
// "ctrl+c", "up", "J") into bytes for the program in a terminal, according
// to that terminal's modes (application cursor…).
func EncodeTokens(tokens []string, modes vt.Mode) ([]byte, error) {
	var out []byte
	for _, tok := range tokens {
		if text, ok := strings.CutPrefix(tok, "text:"); ok {
			out = append(out, text...)
			continue
		}
		// A single character goes out as is, uppercase included.
		if utf8.RuneCountInString(tok) == 1 {
			out = append(out, tok...)
			continue
		}
		c, err := ParseCombo(tok, ModMeta)
		if err != nil {
			return nil, err
		}
		if c.Mods&ModMeta != 0 {
			return nil, fmt.Errorf("%q: %s shortcuts stay with neonmux, they are not sent to the program", tok, meta())
		}
		// An explicit alt+x means ESC x, like option_as_meta.
		if b := encode(Event{Name: c.Name, Mods: c.Mods}, modes, true, false); b != nil {
			out = append(out, b...)
			continue
		}
		switch {
		case c.Name == "space":
			out = append(out, ' ')
		case IsPrintableName(c.Name) && c.Mods&ModCtrl == 0:
			out = append(out, c.Name...)
		default:
			return nil, fmt.Errorf("unknown key %q (for text: text:%s)", tok, tok)
		}
	}
	return out, nil
}
