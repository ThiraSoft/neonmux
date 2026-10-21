package kata

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// Chapter 3, Operators: an operator (d, c) followed by a motion.

func init() {
	register(
		Def{ID: "op.change-word", Name: "Replace a word", Gesture: "cw", Chapter: 3, Gen: genOpChangeWord},
		Def{ID: "op.delete-word", Name: "Delete a word", Gesture: "dw", Chapter: 3, Gen: genOpDeleteWord},
		Def{ID: "op.delete-to", Name: "Delete up to the comma", Gesture: "dt,", Chapter: 3, Gen: genOpDeleteTo},
		Def{ID: "op.change-to", Name: "Rewrite up to the period", Gesture: "ct.", Chapter: 3, Gen: genOpChangeTo},
		Def{ID: "op.delete-end", Name: "Delete the end of the line", Gesture: "D", Chapter: 3, Gen: genOpDeleteEnd},
		Def{ID: "op.change-end", Name: "Rewrite the end of the line", Gesture: "C", Chapter: 3, Gen: genOpChangeEnd},
	)
}

func genOpChangeWord(t Text, r *rand.Rand) (Mission, bool) {
	ws := unique(t.Lines, words(t.Lines, 3))
	if len(ws) == 0 {
		return Mission{}, false
	}
	w := pick(r, ws)
	y := replacement(r, w.text)
	target := edit(t.Lines, w.l, w.a, w.b, y)
	return editing(t.Lines, target, fmt.Sprintf("Replace %q with %q.", w.text, y), &Span{w.l, w.a, w.b},
		navThen(t.Lines, Pos{0, 0}, at(w.l, w.a), "cw"+y+"<Esc>", "change to the end of the word, type, leave insert mode"),
		navThen(t.Lines, Pos{0, 0}, within(w.l, w.a, w.b), "ciw"+y+"<Esc>", "change the whole word, from anywhere in it"))
}

func genOpDeleteWord(t Text, r *rand.Rand) (Mission, bool) {
	var ws []word
	for _, w := range unique(t.Lines, words(t.Lines, 2)) {
		if s := t.Lines[w.l]; w.b < len(s)-1 && s[w.b] == ' ' && s[w.b+1] != ' ' {
			ws = append(ws, w)
		}
	}
	if len(ws) == 0 {
		return Mission{}, false
	}
	w := pick(r, ws)
	target := edit(t.Lines, w.l, w.a, w.b+1, "")
	return editing(t.Lines, target, fmt.Sprintf("Delete the word %q.", w.text), &Span{w.l, w.a, w.b},
		navThen(t.Lines, Pos{0, 0}, at(w.l, w.a), "dw", "delete to the start of the next word"),
		navThen(t.Lines, Pos{0, 0}, within(w.l, w.a, w.b), "daw", "delete a word with its space, from anywhere in it"))
}

// clauses lists the words followed later on their line by sep, with the
// first sep after them.
func clauses(lines []string, sep byte) []struct {
	w word
	k int
} {
	var out []struct {
		w word
		k int
	}
	for _, w := range words(lines, 2) {
		s := lines[w.l]
		if w.a == 0 || s[w.a-1] != ' ' {
			continue
		}
		if k := strings.IndexByte(s[w.b:], sep); k > 0 {
			out = append(out, struct {
				w word
				k int
			}{w, w.b + k})
		}
	}
	return out
}

func genOpDeleteTo(t Text, r *rand.Rand) (Mission, bool) {
	cs := clauses(t.Lines, ',')
	if len(cs) == 0 {
		return Mission{}, false
	}
	c := pick(r, cs)
	s := t.Lines[c.w.l]
	target := edit(t.Lines, c.w.l, c.w.a, c.k, "")
	return editing(t.Lines, target, fmt.Sprintf("Delete %q, up to the comma.", s[c.w.a:c.k]), &Span{c.w.l, c.w.a, c.k},
		navThen(t.Lines, Pos{0, 0}, at(c.w.l, c.w.a), "dt,", "delete until just before the next comma"))
}

func genOpChangeTo(t Text, r *rand.Rand) (Mission, bool) {
	cs := clauses(t.Lines, '.')
	if len(cs) == 0 {
		return Mission{}, false
	}
	c := pick(r, cs)
	s := t.Lines[c.w.l]
	const y = "something else"
	target := edit(t.Lines, c.w.l, c.w.a, c.k, y)
	return editing(t.Lines, target, fmt.Sprintf("Replace %q with %q.", s[c.w.a:c.k], y), &Span{c.w.l, c.w.a, c.k},
		navThen(t.Lines, Pos{0, 0}, at(c.w.l, c.w.a), "ct."+y+"<Esc>", "change until just before the next period"))
}

// lineEnds lists the unique words that are not the last thing of their line.
func lineEnds(lines []string) []word {
	var out []word
	for _, w := range unique(lines, words(lines, 3)) {
		if w.b < len(lines[w.l])-2 {
			out = append(out, w)
		}
	}
	return out
}

func genOpDeleteEnd(t Text, r *rand.Rand) (Mission, bool) {
	ws := lineEnds(t.Lines)
	if len(ws) == 0 {
		return Mission{}, false
	}
	w := pick(r, ws)
	target := edit(t.Lines, w.l, w.b, len(t.Lines[w.l]), "")
	return editing(t.Lines, target, fmt.Sprintf("Delete everything after %q on its line.", w.text),
		&Span{w.l, w.b, len(t.Lines[w.l])},
		navThen(t.Lines, Pos{0, 0}, at(w.l, w.b), "D", "delete to the end of the line"))
}

func genOpChangeEnd(t Text, r *rand.Rand) (Mission, bool) {
	ws := lineEnds(t.Lines)
	if len(ws) == 0 {
		return Mission{}, false
	}
	w := pick(r, ws)
	const y = " and so on."
	target := edit(t.Lines, w.l, w.b, len(t.Lines[w.l]), y)
	return editing(t.Lines, target, fmt.Sprintf("Replace everything after %q with %q.", w.text, y),
		&Span{w.l, w.b, len(t.Lines[w.l])},
		navThen(t.Lines, Pos{0, 0}, at(w.l, w.b), "C"+y+"<Esc>", "change to the end of the line"))
}
