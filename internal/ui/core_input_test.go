package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/vt"
)

// TestSanitizePasteNoRebuiltEnd: removing "ESC [201~" only once let a
// crafted text rebuild the end of the paste and run what follows. No ESC
// or 8-bit CSI may get through anymore, without touching ordinary UTF-8
// ("ě" is written C4 9B).
func TestSanitizePasteNoRebuiltEnd(t *testing.T) {
	for _, in := range []string{
		"\x1b[20\x1b[201~1~\recho pwned\r",
		"\x1b\x1b[201~[201~\necho pwned\n",
		"\u009b201~echo pwned\r",
		"ls\x1b[201~; rm -rf /\n",
	} {
		got := SanitizePaste(in)
		if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x9b) {
			t.Errorf("%q → %q: an ESC or a CSI survived", in, got)
		}
		if wrapped := "\x1b[200~" + got + "\x1b[201~"; strings.Count(wrapped, "\x1b[201~") != 1 {
			t.Errorf("%q: the paste ends too early", in)
		}
	}
	if got := SanitizePaste("ě ž\r\nend\n"); got != "ě ž\rend\r" {
		t.Errorf("ordinary UTF-8 damaged: %q", got)
	}
	// A lone 0x9b byte (invalid UTF-8) is the CSI of 8-bit terminals.
	if got := SanitizePaste("a\x9b201~b"); got != "a201~b" {
		t.Errorf("lone 0x9b byte: %q", got)
	}
	if got := SanitizePaste("a\xffb"); got != "a\xffb" {
		t.Errorf("other invalid bytes pass through as is: %q", got)
	}
}

// TestEncodeKeypad: in application keypad mode (DECKPAM), a keypad key is
// sent as SS3; outside that mode, its digit goes through the characters.
func TestEncodeKeypad(t *testing.T) {
	ev := KeyEvent{Key: ebiten.KeyNumpad1, Name: keyName(ebiten.KeyNumpad1)}
	if got := string(EncodeKey(ev, vt.ModeAppKeypad, false)); got != "\x1bOq" {
		t.Errorf("keypad 1 in application mode: %q, want ESC O q", got)
	}
	if got := EncodeKey(ev, 0, false); got != nil {
		t.Errorf("keypad 1 in normal mode: %q, want nothing (typed character)", got)
	}
	enter := KeyEvent{Key: ebiten.KeyNumpadEnter, Name: keyName(ebiten.KeyNumpadEnter)}
	if got := string(EncodeKey(enter, vt.ModeAppKeypad, false)); got != "\x1bOM" {
		t.Errorf("keypad enter in application mode: %q", got)
	}
	// The digit from the top row is not a keypad key.
	digit := KeyEvent{Key: ebiten.KeyDigit1, Name: keyName(ebiten.KeyDigit1)}
	if got := EncodeKey(digit, vt.ModeAppKeypad, false); got != nil {
		t.Errorf("digit 1: %q, want nothing", got)
	}
}

// TestKeyNameNumpad: without a name from the system (Linux, keypad with
// num lock off), a keypad key keeps the name of its character.
func TestKeyNameNumpad(t *testing.T) {
	for k, want := range map[ebiten.Key]string{
		ebiten.KeyNumpad0: "0", ebiten.KeyNumpad7: "7", ebiten.KeyNumpadAdd: "+",
		ebiten.KeyNumpadSubtract: "-", ebiten.KeyNumpadMultiply: "*", ebiten.KeyNumpadDivide: "/",
		ebiten.KeyNumpadDecimal: ".", ebiten.KeyNumpadEqual: "=", ebiten.KeyNumpadEnter: "enter",
	} {
		if got := keyName(k); got != want {
			t.Errorf("%v: %q, want %q", k, got, want)
		}
	}
}

// TestKeypadSS3ConsumesChars: a keypad key sent as SS3 (num lock off)
// consumes the characters of the same frame.
func TestKeypadSS3ConsumesChars(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	be := p.s.(*fakeBackend)
	p.t.Write([]byte("\x1b=")) // DECKPAM
	ctx := a.frameCtx(time.Now())
	ev := KeyEvent{Key: ebiten.KeyNumpad1, Name: keyName(ebiten.KeyNumpad1)}
	consumed := a.handleKey(ctx, ev)
	if got := be.sent(); got != "\x1bOq" {
		t.Fatalf("sent %q, want ESC O q", got)
	}
	if !consumed {
		t.Error("the digit from the same frame would be sent twice")
	}
	// Outside application mode, the digit goes through the characters:
	// nothing consumed.
	p.t.Write([]byte("\x1b>")) // DECKPNM
	if a.handleKey(ctx, ev) {
		t.Error("keypad in normal mode: the character must get through")
	}
}

// TestKeypadTypedSendsChar: a keypad that types its characters has num
// lock on (always, on a Mac). As in xterm, its keys then send their
// character even in application mode: Neovim, which turns that mode on,
// ignores digits received as SS3. The keypad enter key, which has no
// character, keeps its sequence.
func TestKeypadTypedSendsChar(t *testing.T) {
	one := KeyEvent{Key: ebiten.KeyNumpad1, Name: keyName(ebiten.KeyNumpad1)}
	enter := KeyEvent{Key: ebiten.KeyNumpadEnter, Name: keyName(ebiten.KeyNumpadEnter)}
	top := KeyEvent{Key: ebiten.KeyDigit2, Name: keyName(ebiten.KeyDigit2)}
	evs := []KeyEvent{one, enter, top}
	markTypedKeypad(evs, []rune("1"))
	if !evs[0].Typed || evs[2].Typed {
		t.Fatalf("typed character: keypad %v, top row %v", evs[0].Typed, evs[2].Typed)
	}
	if got := EncodeKey(evs[0], vt.ModeAppKeypad, false); got != nil {
		t.Errorf("keypad 1 with its character: %q, want nothing (the character goes through)", got)
	}
	if got := string(EncodeKey(evs[1], vt.ModeAppKeypad, false)); got != "\x1bOM" {
		t.Errorf("keypad enter: %q, want ESC O M", got)
	}
	evs = []KeyEvent{one}
	markTypedKeypad(evs, nil)
	if evs[0].Typed {
		t.Error("without a character (num lock off), the key keeps its sequence")
	}

	a, ts := coreApp(t, 1)
	p := ts[0]
	be := p.s.(*fakeBackend)
	p.t.Write([]byte("\x1b=")) // DECKPAM
	ctx := a.frameCtx(time.Now())
	one.Typed = true
	if a.handleKey(ctx, one) {
		t.Error("the typed digit must get through")
	}
	p.Chars(ctx, []rune("1"))
	if got := be.sent(); got != "1" {
		t.Errorf("sent %q, want 1", got)
	}
}
