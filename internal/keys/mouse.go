package keys

import (
	"fmt"

	"github.com/ThiraSoft/neonmux/vt"
)

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

// Wheel turns steps of the wheel (positive: up, toward the history) at cell
// x, y into input for a full-screen program, as the window's wheel does
// (ui termpane.go Mouse): wheel events when the program tracks the mouse,
// arrow keys on the alternate screen. app is false when neither applies:
// the caller scrolls the history instead.
func Wheel(modes vt.Mode, steps, x, y int) (b []byte, app bool) {
	n, btn, name := steps, 64, "up"
	if steps < 0 {
		n, btn, name = -steps, 65, "down"
	}
	switch {
	case modes&vt.ModeMouseMask != 0:
		for i := 0; i < n; i++ {
			b = append(b, EncodeMouse(modes, btn, x, y, true, false, 0)...)
		}
	case modes&vt.ModeAltScreen != 0:
		for i := 0; i < n; i++ {
			b = append(b, Encode(Event{Name: name}, modes, false)...)
		}
	default:
		return nil, false
	}
	return b, true
}
