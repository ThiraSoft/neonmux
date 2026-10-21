package kata

import (
	"math/rand/v2"
	"slices"
	"strings"
)

// Helpers shared by the generators of editing missions.

// replacements are the words typed in place of another one.
var replacements = []string{"lake", "stone", "river", "light", "north", "garden", "paper", "signal"}

// replacement returns a word to type instead of w, with the same capital.
func replacement(r *rand.Rand, w string) string {
	for {
		y := pick(r, replacements)
		if strings.EqualFold(y, w) {
			continue
		}
		if w != "" && w[0] >= 'A' && w[0] <= 'Z' {
			y = strings.ToUpper(y[:1]) + y[1:]
		}
		return y
	}
}

// edit returns a copy of lines where bytes [from, to) of line l become s.
func edit(lines []string, l, from, to int, s string) []string {
	out := slices.Clone(lines)
	out[l] = out[l][:from] + s + out[l][to:]
	return out
}

func isLetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if !(s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z') {
			return false
		}
	}
	return s != ""
}

// word is a whole keyword word of a text.
type word struct {
	l, a, b int // line, start, end (excluded)
	text    string
}

// words lists the words made of letters only, of at least min letters.
func words(lines []string, min int) []word {
	var out []word
	for l, s := range lines {
		for _, w := range wordSpans(s) {
			if t := s[w[0]:w[1]]; len(t) >= min && isLetters(t) {
				out = append(out, word{l, w[0], w[1], t})
			}
		}
	}
	return out
}

// unique keeps the words that appear only once as whole words: an
// instruction that names them is not ambiguous.
func unique(lines []string, ws []word) []word {
	return slices.DeleteFunc(slices.Clone(ws), func(w word) bool { return len(wholeWords(lines, w.text)) != 1 })
}

// at is the region made of one place.
func at(l, c int) func(Pos) bool { return func(p Pos) bool { return p == Pos{l, c} } }

// within is the region of line l from column a to b (excluded).
func within(l, a, b int) func(Pos) bool {
	return func(p Pos) bool { return p.Line == l && p.Col >= a && p.Col < b }
}

// either joins regions.
func either(rs ...func(Pos) bool) func(Pos) bool {
	return func(p Pos) bool {
		for _, r := range rs {
			if r(p) {
				return true
			}
		}
		return false
	}
}

func onLines(a, b int) func(Pos) bool { return func(p Pos) bool { return p.Line >= a && p.Line < b } }

// editing builds an editing mission from the start of the text.
func editing(start, target []string, instruction string, hl *Span, candidates ...Recipe) (Mission, bool) {
	return editingFrom(start, target, Pos{0, 0}, instruction, hl, candidates...)
}

func editingFrom(start, target []string, from Pos, instruction string, hl *Span, candidates ...Recipe) (Mission, bool) {
	if slices.Equal(start, target) {
		return Mission{}, false
	}
	return Mission{Instruction: instruction, Start: start, Target: target, Cursor: from, Highlight: hl, Candidates: candidates}, true
}
