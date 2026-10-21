package vt

import "fmt"

// Kitty keyboard protocol (progressive enhancement). A program pushes the
// flags it wants (CSI > flags u), and gets the previous ones back when it
// pops them (CSI < u). Each screen has its own stack. Only flag 1
// (disambiguate escape codes) is supported: other flags are dropped, and
// the query (CSI ? u) answers with what is really in effect, so that the
// program knows. With flag 1, ModeKittyKeys is on and the keys package
// encodes ctrl+i, alt+x, shift+enter or escape without ambiguity.

const (
	kbdDisambiguate = 1
	kbdSupported    = kbdDisambiguate
	// kbdStackMax bounds each stack: a program that pushes without ever
	// popping loses its oldest entries, as in kitty.
	kbdStackMax = 16
)

func (t *Terminal) kbdStack() *[]uint8 {
	if t.g == t.alt {
		return &t.kbd[1]
	}
	return &t.kbd[0]
}

// kbdFlags returns the flags in effect on the active screen.
func (t *Terminal) kbdFlags() uint8 {
	if s := *t.kbdStack(); len(s) > 0 {
		return s[len(s)-1]
	}
	return 0
}

func (t *Terminal) syncKbd() {
	t.setMode(ModeKittyKeys, t.kbdFlags()&kbdDisambiguate != 0)
}

// csiKbd handles CSI ? u, CSI > flags u, CSI < n u and CSI = flags ; mode u.
func (t *Terminal) csiKbd(private byte) {
	p := &t.p
	s := t.kbdStack()
	switch private {
	case '?':
		t.replyString(fmt.Sprintf("\x1b[?%du", t.kbdFlags()))
		return
	case '>':
		if len(*s) == kbdStackMax {
			*s = append((*s)[:0], (*s)[1:]...)
		}
		*s = append(*s, uint8(p.paramZ(0, 0))&kbdSupported)
	case '<':
		n := min(p.param(0, 1), len(*s))
		*s = (*s)[:len(*s)-n]
	case '=':
		f := uint8(p.paramZ(0, 0)) & kbdSupported
		if len(*s) == 0 {
			*s = append(*s, 0)
		}
		top := &(*s)[len(*s)-1]
		switch p.param(1, 1) {
		case 1:
			*top = f
		case 2:
			*top |= f
		case 3:
			*top &^= f
		}
	}
	t.syncKbd()
}
