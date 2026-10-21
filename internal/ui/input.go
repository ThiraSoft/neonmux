package ui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/keys"
	"github.com/ThiraSoft/neonmux/vt"
)

// Key types and key decoding live in the keys package, shared with the
// daemon; these aliases keep the usual names in ui.
type (
	Mods  = keys.Mods
	Combo = keys.Combo
)

const (
	ModShift = keys.ModShift
	ModCtrl  = keys.ModCtrl
	ModAlt   = keys.ModAlt
	ModMeta  = keys.ModMeta
)

func isPrintableName(n string) bool { return keys.IsPrintableName(n) }

// ParseCombo reads "ctrl+space", "cmd+shift+h" or "mod+1".
func ParseCombo(s string, mod Mods) (Combo, error) { return keys.ParseCombo(s, mod) }

// ParseMods reads a modifier on its own ("cmd", "ctrl+shift").
func ParseMods(s string) Mods { return keys.ParseMods(s) }

// EncodeKey translates a key into bytes for the terminal's program.
func EncodeKey(ev KeyEvent, modes vt.Mode, optionAsMeta bool) []byte {
	kev := keys.Event{Name: ev.Name, Mods: ev.Mods, Keypad: isKeypad(ev.Key) && !ev.Typed}
	if ev.Key >= ebiten.KeyDigit0 && ev.Key <= ebiten.KeyDigit9 {
		kev.Char = strings.ToLower(ebiten.KeyName(ev.Key)) // Ctrl follows the keyboard layout outside macOS
	}
	return keys.Encode(kev, modes, optionAsMeta)
}

// isKeypad reports a numeric keypad key. In Ebiten, the keypad is one
// contiguous range, from KeyNumpad0 to KeyNumpadSubtract: the ten digits,
// then Add, Decimal, Divide, Enter, Equal, Multiply and Subtract.
func isKeypad(k ebiten.Key) bool { return k >= ebiten.KeyNumpad0 && k <= ebiten.KeyNumpadSubtract }

// keypadNames names keypad keys by their character, as keys.Event.Keypad
// expects. On Linux, the system does not always provide one (keypad with
// num lock off) and the name used to fall back to "numpad1".
var keypadNames = map[ebiten.Key]string{
	ebiten.KeyNumpadAdd: "+", ebiten.KeyNumpadDecimal: ".", ebiten.KeyNumpadDivide: "/",
	ebiten.KeyNumpadEqual: "=", ebiten.KeyNumpadMultiply: "*", ebiten.KeyNumpadSubtract: "-",
}

// markTypedKeypad marks the keypad keys of a frame that also produced
// characters: num lock is on (a Mac has no num lock and always behaves
// this way). As in xterm, these keys then send their character even in
// application mode. With num lock off (Linux), the system gives no
// character and the SS3 sequence stays, as for the keypad enter key,
// which never produces one.
func markTypedKeypad(evs []KeyEvent, chars []rune) {
	if len(chars) == 0 {
		return
	}
	for i := range evs {
		if isKeypad(evs[i].Key) && isPrintableName(evs[i].Name) {
			evs[i].Typed = true
		}
	}
}

func currentMods() Mods {
	var m Mods
	if ebiten.IsKeyPressed(ebiten.KeyShift) {
		m |= ModShift
	}
	if ebiten.IsKeyPressed(ebiten.KeyControl) {
		m |= ModCtrl
	}
	if ebiten.IsKeyPressed(ebiten.KeyAlt) {
		m |= ModAlt
	}
	if ebiten.IsKeyPressed(ebiten.KeyMeta) {
		m |= ModMeta
	}
	return m
}

// KeyEvent is a pressed (or repeated) key.
type KeyEvent struct {
	Key    ebiten.Key
	Name   string // name from the keyboard layout: "a" on the Q key in AZERTY
	Mods   Mods
	Repeat bool
	Typed  bool // keypad key that also produced its character (num lock on)
}

var specialNames = map[ebiten.Key]string{
	ebiten.KeyEnter: "enter", ebiten.KeyNumpadEnter: "enter", ebiten.KeyTab: "tab",
	ebiten.KeySpace: "space", ebiten.KeyBackspace: "backspace", ebiten.KeyEscape: "escape",
	ebiten.KeyArrowUp: "up", ebiten.KeyArrowDown: "down", ebiten.KeyArrowLeft: "left",
	ebiten.KeyArrowRight: "right", ebiten.KeyHome: "home", ebiten.KeyEnd: "end",
	ebiten.KeyPageUp: "pageup", ebiten.KeyPageDown: "pagedown", ebiten.KeyInsert: "insert",
	ebiten.KeyDelete: "delete",
}

func isModifierKey(k ebiten.Key) bool {
	switch k {
	case ebiten.KeyShift, ebiten.KeyShiftLeft, ebiten.KeyShiftRight,
		ebiten.KeyControl, ebiten.KeyControlLeft, ebiten.KeyControlRight,
		ebiten.KeyAlt, ebiten.KeyAltLeft, ebiten.KeyAltRight,
		ebiten.KeyMeta, ebiten.KeyMetaLeft, ebiten.KeyMetaRight, ebiten.KeyCapsLock:
		return true
	}
	return false
}

// keyName gives a key a stable name. Digits use the physical position
// (in AZERTY the 1 key produces "&"), letters and punctuation use the
// name from the active layout.
func keyName(k ebiten.Key) string {
	if k >= ebiten.KeyDigit0 && k <= ebiten.KeyDigit9 {
		return string(rune('0' + int(k-ebiten.KeyDigit0)))
	}
	// Keypad digits are digits in every layout.
	if k >= ebiten.KeyNumpad0 && k <= ebiten.KeyNumpad9 {
		return string(rune('0' + int(k-ebiten.KeyNumpad0)))
	}
	if k >= ebiten.KeyF1 && k <= ebiten.KeyF12 {
		return "f" + strconv.Itoa(int(k-ebiten.KeyF1)+1)
	}
	if n, ok := specialNames[k]; ok {
		return n
	}
	if n := ebiten.KeyName(k); n != "" {
		return strings.ToLower(n)
	}
	if n, ok := keypadNames[k]; ok {
		return n
	}
	return strings.ToLower(strings.TrimPrefix(k.String(), "Key"))
}

// EncodeChars encodes typed text, prefixed with ESC when Alt acts as Meta.
func EncodeChars(rs []rune, mods Mods, optionAsMeta bool) []byte {
	if len(rs) == 0 {
		return nil
	}
	var out []byte
	metaAlt := mods&ModAlt != 0 && runtime.GOOS != "darwin"
	for _, r := range rs {
		if metaAlt {
			out = append(out, 0x1b)
		}
		out = append(out, string(r)...)
	}
	return out
}

// repeatable reports the keys whose repeat the app handles itself
// (characters are already repeated by the system).
func repeatable(ev KeyEvent) bool {
	switch ev.Name {
	case "backspace", "delete", "up", "down", "left", "right", "pageup", "pagedown", "enter", "tab", "home", "end":
		return true
	}
	return ev.Mods&ModCtrl != 0 && isPrintableName(ev.Name)
}

// keyRepeat reads the system key repeat settings. On macOS the
// values are in units of 15 ms.
func keyRepeat() (delay, interval time.Duration) {
	delay, interval = 300*time.Millisecond, 33*time.Millisecond
	if runtime.GOOS != "darwin" {
		return
	}
	read := func(key string) int {
		out, err := exec.Command("defaults", "read", "-g", key).Output()
		if err != nil {
			return 0
		}
		v, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		return v
	}
	if v := read("InitialKeyRepeat"); v > 0 {
		delay = time.Duration(v) * 15 * time.Millisecond
	}
	if v := read("KeyRepeat"); v > 0 {
		interval = time.Duration(v) * 15 * time.Millisecond
	}
	return
}

// SanitizePaste keeps pasted text from breaking out of bracketed paste
// mode and running commands. Removing "ESC [201~" is not enough: a
// crafted text ("ESC [20 ESC [201~ 1~") puts it back together afterwards.
// So every sequence introducer is removed: ESC and 8-bit CSI (U+009B).
// These are runes, not bytes: 0x9b is part of ordinary UTF-8 ("ě" is
// written C4 9B). Only a lone 0x9b byte, invalid in UTF-8, is removed
// too: it is the CSI of 8-bit terminals.
func SanitizePaste(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == 0x1b || r == 0x9b:
		case r == utf8.RuneError && size == 1 && s[i] == 0x9b:
		default:
			b.WriteString(s[i : i+size]) // any other invalid byte passes through as is
		}
		i += size
	}
	s = strings.ReplaceAll(b.String(), "\r\n", "\r")
	return strings.ReplaceAll(s, "\n", "\r")
}

// EncodeMouse produces a mouse event for the program (SGR 1006 or X10).
// button: 0 left, 1 middle, 2 right, 3 released (X10), 64/65 wheel.
func EncodeMouse(modes vt.Mode, button, x, y int, press, motion bool, mods Mods) []byte {
	b := button
	if motion {
		b += 32
	}
	if mods&ModShift != 0 {
		b += 4
	}
	if mods&ModAlt != 0 {
		b += 8
	}
	if mods&ModCtrl != 0 {
		b += 16
	}
	if modes&vt.ModeMouseSGR != 0 {
		final := 'M'
		if !press {
			final = 'm'
		}
		return []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", b, x+1, y+1, final))
	}
	if !press {
		b = 3 | b&^3
	}
	clampc := func(v int) byte { return byte(min(v+1+32, 255)) }
	return []byte{0x1b, '[', 'M', byte(b + 32), clampc(x), clampc(y)}
}
