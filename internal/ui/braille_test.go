package ui

import (
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
)

func TestBrailleRune(t *testing.T) {
	cases := map[rune]rune{
		'a': '⠁', 'z': '⠵', 'w': '⠺', 'H': '⡓', 'W': '⡺',
		'1': '⠂', '0': '⠴', '=': '⠿', '?': '⠹', ',': '⠠',
		'[': '⡪', '{': '⠪', '_': '⡸', '~': '⠘',
		'é': 0x28bf, 'à': 0x28b7, 'ç': 0x28af, 'œ': 0x28aa, 'É': 0x28ff, 'Ê': 0x28e3,
	}
	for in, want := range cases {
		if got := brailleRune(in); got != want {
			t.Errorf("brailleRune(%q) = %U (%c), want %U (%c)", in, got, got, want, want)
		}
	}
}

// " ─│╭█▓⣿ñø漢🐙\x7f": every rune comes back unchanged.
func TestBrailleRuneLeavesTheRest(t *testing.T) {
	const rest = " ─│╭█▓⣿ñø漢🐙\x7f"
	for _, r := range rest {
		if got := brailleRune(r); got != r {
			t.Errorf("brailleRune(%q) = %U, want %U (unchanged)", r, got, r)
		}
	}
	if got := brailleRune(-1); got != -1 {
		t.Errorf("brailleRune(-1) = %U, want -1 (unchanged)", got)
	}
}

// '!' to '~' plus "àâçéèêëîïôœùûüÀÂÇÉÈÊËÎÏÔŒÙÛÜ": every result is in
// U+2800..U+28FF and no two characters share a cell.
func TestBrailleRuneOneToOne(t *testing.T) {
	var input []rune
	for r := rune('!'); r <= '~'; r++ {
		input = append(input, r)
	}
	input = append(input, []rune("àâçéèêëîïôœùûüÀÂÇÉÈÊËÎÏÔŒÙÛÜ")...)

	seen := make(map[rune]rune)
	for _, r := range input {
		got := brailleRune(r)
		if got < 0x2800 || got > 0x28ff {
			t.Errorf("brailleRune(%q) = %U, outside U+2800..U+28FF", r, got)
		}
		if prev, exists := seen[got]; exists {
			t.Errorf("brailleRune(%q) = %U duplicates cell for %q", r, got, prev)
		}
		seen[got] = r
	}
}

func TestBrailleBanner(t *testing.T) {
	want := "▀ ▀  ▀    ▀    ▀ ▀  ▀ ▀  ▀    ▀ ▀\n" +
		"  ▀    ▀    ▀    ▀               \n" +
		"▀         ▀    ▀    ▀    ▀ ▀  ▀ ▀"
	if got := brailleBanner("neonmux"); got != want {
		t.Errorf("brailleBanner(\"neonmux\") =\n%q\nwant:\n%q", got, want)
	}
	if brailleLogo != want {
		t.Errorf("brailleLogo =\n%q\nwant:\n%q", brailleLogo, want)
	}
}

// The shortcut switches the view, the next render redraws every line, and
// the terminal keeps its text.
func TestBrailleToggleRedraws(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	f, th := a.termFont, a.theme
	p.r.render(p.t, f, th, 0, a.braille)
	a.do("view.braille")
	if !a.braille {
		t.Fatal("braille view expected")
	}
	p.r.render(p.t, f, th, 0, a.braille)
	if p.r.rows != p.rows {
		t.Errorf("toggle: %d lines redrawn out of %d", p.r.rows, p.rows)
	}
	p.t.Lock()
	r := p.t.ScreenLine(0)[0].Rune()
	p.t.Unlock()
	if r > 0x7f {
		t.Errorf("terminal text modified: %U (%c)", r, r)
	}
	a.do("view.braille")
	if a.braille {
		t.Fatal("text view expected")
	}
}

// While reading the history, the switch redraws the whole view too.
func TestBrailleToggleInHistory(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	f, th := a.termFont, a.theme
	p.offset = 5
	p.r.render(p.t, f, th, 5, false)
	p.r.render(p.t, f, th, 5, true)
	if p.r.rows != p.rows {
		t.Errorf("toggle in history: %d lines redrawn out of %d", p.r.rows, p.rows)
	}
}

// Copying in the braille view gives the real text.
func TestBrailleCopyKeepsText(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	p.t.Lock()
	_, screen := p.t.AbsBase()
	p.t.Unlock()
	p.sel = selection{active: true, a: selPos{row: screen - 5}, b: selPos{row: screen - 5, col: 10}}
	plain := p.SelectedText()
	a.do("view.braille")
	if got := p.SelectedText(); got != plain {
		t.Errorf("SelectedText in braille = %q, want %q", got, plain)
	}
	if !strings.HasPrefix(plain, "line") {
		t.Errorf("SelectedText %q does not start with \"line\"", plain)
	}
}

// Headers, status bar and splash follow the view, and the painter is back
// to plain text afterwards for the toasts and the palette.
func TestBrailleChrome(t *testing.T) {
	a, _ := coreApp(t, 2)
	if a.chromeRunes() != nil {
		t.Fatal("chromeRunes initially expected to be nil")
	}
	a.do("view.braille")
	f := a.chromeRunes()
	if f == nil || f('N') != '⡝' {
		t.Fatalf("chromeRunes in braille view: want f('N') == '⡝'")
	}
	screen := ebiten.NewImage(a.w, a.h)
	settle(a, screen)
	if a.painter.Runes != nil {
		t.Errorf("painter.Runes after settle is not nil")
	}
	a.drawPane(a.frameCtx(time.Now()), screen, a.panes[0], 0, true)
	if a.painter.Runes != nil {
		t.Errorf("painter.Runes after drawPane is not nil")
	}
	a.drawSplash(a.frameCtx(time.Now()), screen, true, 1)
	if a.painter.Runes != nil {
		t.Errorf("painter.Runes after drawSplash is not nil")
	}
	a.drawStatus(a.frameCtx(time.Now()), screen)
	if a.painter.Runes != nil {
		t.Errorf("painter.Runes after drawStatus is not nil")
	}

	// In an empty app (splash mode), drawing in braille leaves painter.Runes nil.
	a0, _ := coreApp(t, 0)
	a0.braille = true
	a0.needRedraw = true
	a0.Draw(screen)
	if a0.painter.Runes != nil {
		t.Errorf("painter.Runes after splash in braille view is not nil")
	}
}

// A full frame costs no more allocations in the braille view.
func TestBrailleFullFrameAllocs(t *testing.T) {
	a, _ := coreApp(t, 4)
	screen := ebiten.NewImage(a.w, a.h)
	settle(a, screen)
	frame := func() {
		a.needRedraw = true
		a.Draw(screen)
	}
	plain := testing.AllocsPerRun(20, frame)
	a.braille = true
	frame()
	braille := testing.AllocsPerRun(20, frame)
	if braille > plain+1 {
		t.Errorf("full frame in braille view: %.0f allocs, want <= %.0f+1", braille, plain)
	}
}

// Typing test words and typed text are mapped through the painter's runes.
func TestBrailleTypingPaneDrawRunes(t *testing.T) {
	a, _ := coreApp(t, 1)
	f := a.uiFont
	var P gfx.Painter
	var seen []rune
	P.Runes = func(r rune) rune {
		seen = append(seen, r)
		return brailleRune(r)
	}
	white := color.RGBA{255, 255, 255, 255}
	w := drawRunes(&P, f, 0, 0, "a b", white, 1)
	if len(seen) != 2 || seen[0] != 'a' || seen[1] != 'b' {
		t.Fatalf("drawRunes mapped runes: got %c, want ['a', 'b']", seen)
	}
	if want := float32(3 * f.CellW); w != want {
		t.Fatalf("drawRunes width: got %v, want %v", w, want)
	}

	// With brailleRune, 'a' maps to 0x2801 ('⠁').
	var seenBraille []rune
	P.Runes = func(r rune) rune {
		br := brailleRune(r)
		seenBraille = append(seenBraille, br)
		return br
	}
	drawRunes(&P, f, 0, 0, "a", white, 1)
	if len(seenBraille) != 1 || seenBraille[0] != '⠁' {
		t.Fatalf("drawRunes with braille: got %c, want ['⠁']", seenBraille)
	}

	// The braille glyph exists in the font and has a distinct atlas location.
	var P2 gfx.Painter
	P2.Runes = brailleRune
	w = drawRunes(&P2, f, 0, 0, "a", white, 1)
	if w != float32(f.CellW) {
		t.Fatalf("drawRunes width with brailleRune: got %v, want %v", w, float32(f.CellW))
	}
	gBraille := f.Glyph(brailleRune('a'), 0, false)
	if !gBraille.Exists {
		t.Fatal("braille rune glyph does not exist in font")
	}
	gPlain := f.Glyph('a', 0, false)
	if gBraille.X == gPlain.X && gBraille.Y == gPlain.Y {
		t.Fatal("braille glyph and plain glyph share the same atlas position")
	}
	qs := P2.Quads()
	if len(qs) != 1 {
		t.Fatalf("drawRunes quads: got %d, want 1", len(qs))
	}
	if qs[0].SrcX != float32(gBraille.X) || qs[0].SrcY != float32(gBraille.Y) {
		t.Fatalf("drawRunes quad src = (%v, %v), want braille glyph (%v, %v)",
			qs[0].SrcX, qs[0].SrcY, gBraille.X, gBraille.Y)
	}
}

// The help pane stays in plain text in the braille view: both its header and
// its shortcut listings must be drawn with plain glyphs (Runes == nil).
func TestBrailleHelpStaysPlain(t *testing.T) {
	a, _ := coreApp(t, 1)
	screen := ebiten.NewImage(a.w, a.h)
	settle(a, screen)

	a.do("view.braille")
	if !a.braille {
		t.Fatal("braille view expected")
	}

	a.toggleHelp()
	var h *HelpPane
	for _, p := range a.panes {
		if hp, ok := p.(*HelpPane); ok {
			h = hp
			break
		}
	}
	if h == nil {
		t.Fatal("help pane not found")
	}

	settle(a, screen)

	if h.headerRunes != nil {
		t.Errorf("help header drawn with braille runes (%p), want nil (plain text)", h.headerRunes)
	}
	if h.drawRunes != nil {
		t.Errorf("help text drawn with braille runes (%p), want nil (plain text)", h.drawRunes)
	}
}

// TermPane.Draw re-renders the terminal when the braille view is toggled,
// even if the pane is clean and has no damaged lines.
func TestBrailleTermPaneDrawReRendersOnToggle(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	screen := ebiten.NewImage(a.w, a.h)
	settle(a, screen)

	if p.r.braille {
		t.Fatal("expected p.r.braille == false initially")
	}
	if p.dirty {
		t.Fatal("expected p.dirty == false after settle")
	}

	a.do("view.braille")
	if !a.braille {
		t.Fatal("expected braille view active")
	}

	p.dirty = false
	p.r.rows = 0

	ctx := a.frameCtx(time.Now())
	_, header, _, pad := a.metrics()
	content := contentRect(p.Base().rect(ctx.Now), header, pad)
	p.Draw(ctx, screen, content, true)

	if !p.r.braille {
		t.Errorf("p.r.braille = false, want true after Draw in braille view")
	}
	if p.r.rows != p.rows {
		t.Errorf("p.r.rows = %d, want %d redrawn lines", p.r.rows, p.rows)
	}
}

// The braille logo is centered vertically for its 3-line height, while the
// block logo keeps its original 2-line vertical position.
func TestBrailleSplashVerticalCentering(t *testing.T) {
	a, _ := coreApp(t, 0)
	scale := float32(2)
	cellH := float32(a.uiFont.CellH)

	blockLines := strings.Split(logo, "\n")
	wantBlockY := float32(a.h)/2 - cellH*scale*2
	if got := a.splashY(blockLines); got != wantBlockY {
		t.Errorf("block logo splashY = %v, want %v", got, wantBlockY)
	}

	brailleLines := strings.Split(brailleLogo, "\n")
	wantBrailleY := float32(a.h)/2 - cellH*scale*2.5
	if got := a.splashY(brailleLines); got != wantBrailleY {
		t.Errorf("braille logo splashY = %v, want %v", got, wantBrailleY)
	}
}
