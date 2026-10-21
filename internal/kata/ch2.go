package kata

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// Chapter 2, Typos: a mistake was slipped into the text; the target is the
// text as it was.

func init() {
	register(
		Def{ID: "typo.swap", Name: "Fix the swapped letters", Gesture: "xp", Chapter: 2, Gen: genTypoSwap},
		Def{ID: "typo.double", Name: "Remove the repeated word", Gesture: "dw", Chapter: 2, Gen: genTypoDouble},
		Def{ID: "typo.capital", Name: "Restore the capital letter", Gesture: "~", Chapter: 2, Gen: genTypoCapital},
		Def{ID: "typo.letter", Name: "Fix the wrong letter", Gesture: "r", Chapter: 2, Gen: genTypoLetter},
		Def{ID: "typo.extra", Name: "Remove the extra letter", Gesture: "x", Chapter: 2, Gen: genTypoExtra},
		Def{ID: "typo.join", Name: "Join the broken line", Gesture: "J", Chapter: 2, Gen: genTypoJoin},
	)
}

func genTypoSwap(t Text, r *rand.Rand) (Mission, bool) {
	type swap struct {
		w word
		i int
	}
	var cands []swap
	for _, w := range words(t.Lines, 4) {
		for i := 1; i < len(w.text)-1; i++ {
			if w.text[i] != w.text[i+1] {
				cands = append(cands, swap{w, i})
			}
		}
	}
	if len(cands) == 0 {
		return Mission{}, false
	}
	c := pick(r, cands)
	b := []byte(c.w.text)
	b[c.i], b[c.i+1] = b[c.i+1], b[c.i]
	start := edit(t.Lines, c.w.l, c.w.a, c.w.b, string(b))
	x := c.w.a + c.i
	return editing(start, t.Lines,
		fmt.Sprintf("Fix the swapped letters: %q should be %q.", string(b), c.w.text),
		&Span{c.w.l, c.w.a, c.w.b},
		navThen(start, Pos{0, 0}, at(c.w.l, x), "xp", "x cuts the letter, p puts it back after the next one"))
}

func genTypoDouble(t Text, r *rand.Rand) (Mission, bool) {
	var cands []word
	for _, w := range words(t.Lines, 2) {
		if s := t.Lines[w.l]; w.b < len(s) && s[w.b] == ' ' {
			cands = append(cands, w)
		}
	}
	if len(cands) == 0 {
		return Mission{}, false
	}
	w := pick(r, cands)
	start := edit(t.Lines, w.l, w.a, w.a, w.text+" ")
	second := w.a + len(w.text) + 1
	starts := either(at(w.l, w.a), at(w.l, second))
	inside := either(within(w.l, w.a, w.b), within(w.l, second, second+len(w.text)))
	return editing(start, t.Lines, fmt.Sprintf("Remove the repeated word %q.", w.text),
		&Span{w.l, w.a, second + len(w.text)},
		navThen(start, Pos{0, 0}, starts, "dw", "delete to the start of the next word"),
		navThen(start, Pos{0, 0}, inside, "daw", "delete a word, with its space, from anywhere in it"))
}

func genTypoCapital(t Text, r *rand.Rand) (Mission, bool) {
	var cands []word
	for _, w := range words(t.Lines, 2) {
		s := t.Lines[w.l]
		if w.a < 2 || s[w.a-1] != ' ' || w.text[0] < 'A' || w.text[0] > 'Z' {
			continue
		}
		// Not the start of a sentence: the capital is the word's own.
		if p := strings.TrimRight(s[:w.a], " "); p == "" || strings.ContainsAny(p[len(p)-1:], ".!?") {
			continue
		}
		cands = append(cands, w)
	}
	if len(cands) == 0 {
		return Mission{}, false
	}
	w := pick(r, cands)
	low := strings.ToLower(w.text[:1]) + w.text[1:]
	start := edit(t.Lines, w.l, w.a, w.b, low)
	return editing(start, t.Lines, fmt.Sprintf("%q needs its capital letter back.", w.text),
		&Span{w.l, w.a, w.b},
		navThen(start, Pos{0, 0}, at(w.l, w.a), "~", "switch the case of the letter under the cursor"))
}

func genTypoLetter(t Text, r *rand.Rand) (Mission, bool) {
	ws := words(t.Lines, 4)
	if len(ws) == 0 {
		return Mission{}, false
	}
	w := pick(r, ws)
	i := r.IntN(len(w.text))
	orig := w.text[i]
	if orig < 'a' || orig > 'z' {
		return Mission{}, false
	}
	bad := orig
	for bad == orig {
		bad = byte('a' + r.IntN(26))
	}
	b := []byte(w.text)
	b[i] = bad
	start := edit(t.Lines, w.l, w.a, w.b, string(b))
	return editing(start, t.Lines, fmt.Sprintf("Fix the wrong letter: %q should be %q.", string(b), w.text),
		&Span{w.l, w.a, w.b},
		navThen(start, Pos{0, 0}, at(w.l, w.a+i), "r"+string(orig), "replace the letter under the cursor"))
}

func genTypoExtra(t Text, r *rand.Rand) (Mission, bool) {
	ws := words(t.Lines, 3)
	if len(ws) == 0 {
		return Mission{}, false
	}
	w := pick(r, ws)
	i := r.IntN(len(w.text))
	bad := w.text[:i+1] + w.text[i:]
	start := edit(t.Lines, w.l, w.a, w.b, bad)
	return editing(start, t.Lines, fmt.Sprintf("Remove the extra letter: %q should be %q.", bad, w.text),
		&Span{w.l, w.a, w.a + len(bad)},
		navThen(start, Pos{0, 0}, either(at(w.l, w.a+i), at(w.l, w.a+i+1)), "x", "delete the letter under the cursor"))
}

func genTypoJoin(t Text, r *rand.Rand) (Mission, bool) {
	type cut struct{ l, k int }
	var cands []cut
	for l, s := range t.Lines {
		for k := 8; k < len(s)-8; k++ {
			// J puts one space back, except before a ")".
			if s[k] == ' ' && s[k-1] != ' ' && s[k+1] != ' ' && s[k+1] != ')' {
				cands = append(cands, cut{l, k})
			}
		}
	}
	if len(cands) == 0 {
		return Mission{}, false
	}
	c := pick(r, cands)
	s := t.Lines[c.l]
	start := slices.Concat(t.Lines[:c.l], []string{s[:c.k], s[c.k+1:]}, t.Lines[c.l+1:])
	return editing(start, t.Lines, "Join the broken line back together.",
		&Span{c.l, 0, c.k},
		navThen(start, Pos{0, 0}, onLines(c.l, c.l+1), "J", "join the next line to this one, with a space"))
}
