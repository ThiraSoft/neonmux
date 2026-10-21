// Package wiki fetches random English Wikipedia articles and turns them into
// plain text that can be typed on any keyboard: the waiting games (typing
// test, vim katas) play on it.
package wiki

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// typeable replaces what a keyboard does not have, and the letters that
// Unicode decomposition leaves alone (Æ, ø, ß…).
var typeable = strings.NewReplacer(
	"‘", "'", "’", "'", "‚", "'", "′", "'",
	"“", `"`, "”", `"`, "„", `"`, "″", `"`, "«", `"`, "»", `"`,
	"–", "-", "—", "-", "―", "-", "−", "-", "‐", "-", "‑", "-",
	"…", "...", "×", "x",
	"Æ", "AE", "æ", "ae", "Œ", "OE", "œ", "oe", "Ø", "O", "ø", "o",
	"Ł", "L", "ł", "l", "Đ", "D", "Ð", "D", "đ", "d", "ð", "d",
	"Þ", "Th", "þ", "th", "ß", "ss",
)

// maxDropped is the share of letters that may have no ASCII equivalent
// before a paragraph is rejected: a stray symbol is fine, a sentence in
// another script is not.
const maxDropped = 0.02

// Normalize returns s with printable ASCII only and single spaces. The
// parentheses that still hold other characters once accents are removed
// (pronunciation in IPA, a name in its own script) are dropped whole. ok is
// false when too much text could not be converted.
func Normalize(s string) (string, bool) {
	s = typeable.Replace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	s = stripMarks(s)
	s = dropForeignParens(s)

	letters, dropped := 0, 0
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
		switch {
		case r == ' ':
			space = b.Len() > 0
			continue
		case r < 0x21 || r > 0x7e:
			dropped++
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String(), float64(dropped) <= maxDropped*float64(letters)
}

// stripMarks removes accents: decomposition puts them apart as combining
// marks (é becomes e and an acute accent).
func stripMarks(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFKD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// dropForeignParens removes every parenthesis (not nested) that still holds
// a character outside ASCII, along with the space before it.
func dropForeignParens(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for {
		open := strings.IndexByte(s, '(')
		if open < 0 {
			break
		}
		end := strings.IndexByte(s[open:], ')')
		if end < 0 {
			break
		}
		end += open
		inner := s[open+1 : end]
		if strings.IndexByte(inner, '(') >= 0 || isASCII(inner) {
			b.WriteString(s[:open+1])
			s = s[open+1:]
			continue
		}
		b.WriteString(strings.TrimRight(s[:open], " "))
		s = s[end+1:]
	}
	b.WriteString(s)
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7e {
			return false
		}
	}
	return true
}
