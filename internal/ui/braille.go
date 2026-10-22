package ui

import (
	"strings"
	"unicode"
)

// The braille view renders pane contents in 8-dot computer braille, mapping
// each printable character to a single braille cell. Using 8-dot braille ensures
// a 1:1 spatial correspondence with standard text without shifting column
// alignment. Printable ASCII characters are mapped according to standard Braille
// ASCII with dot 7 indicating uppercase or shifted characters. Common French
// accented letters are mapped using their traditional 6-dot French braille
// cells plus dot 8, with dot 7 added for uppercase. Everything else, such as
// whitespace, control characters, box-drawing characters, block elements,
// existing braille cells, wide characters, emojis, and unsupported accents,
// remains in plain text unchanged.

const brailleASCII = " A1B'K2L@CIF/MSP\"E3H9O6R^DJG>NTQ,*5<-U8V.%[$+X!&;:4\\0Z7(_?W]#Y)="

var asciiBraille = func() [128]rune {
	var table [128]rune
	for i := range table {
		table[i] = rune(i)
	}
	for i := 0x21; i <= 0x7e; i++ {
		r := rune(i)
		switch {
		case r >= 0x21 && r <= 0x3f:
			idx := strings.IndexRune(brailleASCII, r)
			table[i] = 0x2800 + rune(idx)
		case r >= 0x40 && r <= 0x5f:
			idx := strings.IndexRune(brailleASCII, r)
			table[i] = 0x2800 + rune(idx) | 0x40
		case r >= 0x60 && r <= 0x7e:
			idx := strings.IndexRune(brailleASCII, r-0x20)
			table[i] = 0x2800 + rune(idx)
		}
	}
	return table
}()

var frenchBraille = map[rune]rune{
	'â': 0x21,
	'ê': 0x23,
	'î': 0x29,
	'ô': 0x39,
	'û': 0x31,
	'à': 0x37,
	'è': 0x2e,
	'ù': 0x3e,
	'é': 0x3f,
	'ë': 0x2b,
	'ï': 0x3b,
	'ü': 0x33,
	'ç': 0x2f,
	'œ': 0x2a,
}

// brailleRune converts a single character to its 8-dot braille representation.
// Characters without a braille definition return unmodified.
func brailleRune(r rune) rune {
	if uint32(r) < 0x80 {
		return asciiBraille[r]
	}
	if r < 0xc0 {
		return r
	}
	low := unicode.ToLower(r)
	if motif, ok := frenchBraille[low]; ok {
		cell := 0x2800 + 0x80 + motif
		if r != low {
			cell |= 0x40
		}
		return cell
	}
	return r
}

// toggleBraille switches between standard text and 8-dot computer braille.
func (a *App) toggleBraille() {
	a.braille = !a.braille
	if a.braille {
		a.toast("braille view", a.theme.Accent2)
	} else {
		a.toast("text view", a.theme.Accent2)
	}
}

// chromeRunes returns brailleRune when the braille view is active, or nil
// to leave chrome and pane contents in plain text.
func (a *App) chromeRunes() func(rune) rune {
	if a.braille {
		return brailleRune
	}
	return nil
}

// brailleBanner renders a string as a 3-line ASCII-art banner using braille
// cell dots. Each dot is drawn as an upper half block ('▀'), columns within a
// cell are separated by one space, and adjacent cells are separated by two spaces.
// Dots 7 and 8 are ignored in the banner.
func brailleBanner(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	lines := make([]string, 3)
	for y := 0; y < 3; y++ {
		var cells []string
		for _, r := range runes {
			var val rune
			if b := brailleRune(r); b >= 0x2800 && b <= 0x28ff {
				val = b - 0x2800
			}
			c0 := ' '
			if val&(1<<y) != 0 {
				c0 = '▀'
			}
			c1 := ' '
			if val&(1<<(3+y)) != 0 {
				c1 = '▀'
			}
			cells = append(cells, string([]rune{c0, ' ', c1}))
		}
		lines[y] = strings.Join(cells, "  ")
	}
	return strings.Join(lines, "\n")
}

var brailleLogo = brailleBanner("neonmux")
