package kata

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// Chapter 4, Text objects: i( a( iw aw i" as ap, which act on a whole
// object wherever the cursor is in it.

func init() {
	register(
		Def{ID: "obj.empty-parens", Name: "Empty the parentheses", Gesture: "di(", Chapter: 4, Gen: genObjEmptyParens},
		Def{ID: "obj.change-parens", Name: "Replace the text in parentheses", Gesture: "ci(", Chapter: 4, Gen: genObjChangeParens},
		Def{ID: "obj.change-word", Name: "Change a word from inside", Gesture: "ciw", Chapter: 4, Gen: genObjChangeWord},
		Def{ID: "obj.change-quote", Name: "Replace the quoted text", Gesture: `ci"`, Chapter: 4, Gen: genObjChangeQuote},
		Def{ID: "obj.delete-sentence", Name: "Delete a sentence", Gesture: "das", Chapter: 4, Gen: genObjDeleteSentence},
		Def{ID: "obj.delete-paragraph", Name: "Delete a paragraph", Gesture: "dap", Chapter: 4, Gen: genObjDeleteParagraph},
	)
}

// parens lists the parentheses that open and close on one line, with at
// least 3 characters and no other parenthesis inside.
func parens(lines []string) [][3]int {
	var out [][3]int
	for l, s := range lines {
		for a := 0; a < len(s); a++ {
			if s[a] != '(' {
				continue
			}
			c := strings.IndexAny(s[a+1:], "()")
			if c >= 3 && s[a+1+c] == ')' {
				out = append(out, [3]int{l, a, a + 1 + c})
			}
		}
	}
	return out
}

func genObjEmptyParens(t Text, r *rand.Rand) (Mission, bool) {
	ps := parens(t.Lines)
	if len(ps) == 0 {
		return Mission{}, false
	}
	p := pick(r, ps)
	l, a, c := p[0], p[1], p[2]
	target := edit(t.Lines, l, a+1, c, "")
	region := within(l, a, c+1)
	return editing(t.Lines, target, "Empty the parentheses, keeping them.", &Span{l, a, c + 1},
		navThen(t.Lines, Pos{0, 0}, region, "di(", "delete inside the parentheses"),
		navThen(t.Lines, Pos{0, 0}, region, "ci(<Esc>", "change inside the parentheses, then leave insert mode"))
}

func genObjChangeParens(t Text, r *rand.Rand) (Mission, bool) {
	ps := parens(t.Lines)
	if len(ps) == 0 {
		return Mission{}, false
	}
	p := pick(r, ps)
	l, a, c := p[0], p[1], p[2]
	y := replacement(r, "")
	target := edit(t.Lines, l, a+1, c, y)
	return editing(t.Lines, target, fmt.Sprintf("Replace the text in parentheses with %q.", y), &Span{l, a, c + 1},
		navThen(t.Lines, Pos{0, 0}, within(l, a, c+1), "ci("+y+"<Esc>", "change inside the parentheses"))
}

func genObjChangeWord(t Text, r *rand.Rand) (Mission, bool) {
	ws := unique(t.Lines, words(t.Lines, 5))
	if len(ws) == 0 {
		return Mission{}, false
	}
	w := pick(r, ws)
	y := replacement(r, w.text)
	target := edit(t.Lines, w.l, w.a, w.b, y)
	from := Pos{w.l, 0}
	return editingFrom(t.Lines, target, from, fmt.Sprintf("Replace %q with %q.", w.text, y), &Span{w.l, w.a, w.b},
		navThen(t.Lines, from, within(w.l, w.a, w.b), "ciw"+y+"<Esc>", "change the whole word, from anywhere in it"),
		navThen(t.Lines, from, at(w.l, w.a), "cw"+y+"<Esc>", "change to the end of the word"))
}

func genObjChangeQuote(t Text, r *rand.Rand) (Mission, bool) {
	var ls []int
	for l, s := range t.Lines {
		if strings.Count(s, `"`) == 2 {
			if a := strings.IndexByte(s, '"'); strings.IndexByte(s[a+1:], '"') > 1 {
				ls = append(ls, l)
			}
		}
	}
	if len(ls) == 0 {
		return Mission{}, false
	}
	l := pick(r, ls)
	s := t.Lines[l]
	a := strings.IndexByte(s, '"')
	c := a + 1 + strings.IndexByte(s[a+1:], '"')
	y := replacement(r, "")
	target := edit(t.Lines, l, a+1, c, y)
	// ci" also finds the quotes from before them on the line.
	return editing(t.Lines, target, fmt.Sprintf("Replace the quoted text with %q.", y), &Span{l, a, c + 1},
		navThen(t.Lines, Pos{0, 0}, within(l, 0, c+1), `ci"`+y+"<Esc>", "change inside the quotes (from anywhere before them on the line)"))
}

// sentenceEnd returns the index after the end of the sentence that starts
// at a on s ([.!?] then closing ) ] " '), and whether a space and more text
// follow it on the line, as vim needs to see a sentence end.
func sentenceEnd(s string, a int) (int, bool) {
	for i := a; i < len(s); i++ {
		if !strings.ContainsRune(".!?", rune(s[i])) {
			continue
		}
		e := i + 1
		for e < len(s) && strings.ContainsRune(`)]"'`, rune(s[e])) {
			e++
		}
		if e == len(s) {
			return e, false
		}
		if s[e] == ' ' {
			return e, e+1 < len(s) && s[e+1] != ' '
		}
	}
	return len(s), false
}

func genObjDeleteSentence(t Text, r *rand.Rand) (Mission, bool) {
	var cands [][3]int // line, start, end
	for l, s := range t.Lines {
		for a := 2; a < len(s); a++ {
			// A sentence starts mid-line after ". ", and the word before
			// the period is not an abbreviation (Mr. Smith).
			if s[a-1] != ' ' || !strings.ContainsRune(".!?", rune(s[a-2])) || s[a] < 'A' || s[a] > 'Z' {
				continue
			}
			prev := strings.Fields(s[:a-1])
			if len(prev) == 0 || len(strings.Trim(prev[len(prev)-1], `.!?)]"'`)) < 4 {
				continue
			}
			e, ok := sentenceEnd(s, a)
			if !ok || len(strings.Fields(s[a:e])) < 3 || len(strings.Trim(lastField(s[a:e]), `.!?)]"'`)) < 4 {
				continue
			}
			cands = append(cands, [3]int{l, a, e})
		}
	}
	if len(cands) == 0 {
		return Mission{}, false
	}
	c := pick(r, cands)
	l, a, e := c[0], c[1], c[2]
	s := t.Lines[l]
	end := e
	for end < len(s) && s[end] == ' ' {
		end++
	}
	target := edit(t.Lines, l, a, end, "")
	first := strings.Join(strings.Fields(s[a:e])[:3], " ")
	return editing(t.Lines, target, fmt.Sprintf("Delete the sentence that starts with %q.", first), &Span{l, a, e},
		navThen(t.Lines, Pos{0, 0}, within(l, a, e), "das", "delete a sentence, with the space after it"))
}

func lastField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

func genObjDeleteParagraph(t Text, r *rand.Rand) (Mission, bool) {
	ps := paragraphs(t.Lines)
	if len(ps) < 3 {
		return Mission{}, false
	}
	p := ps[1]
	target := slices.Concat(t.Lines[:p[0]], t.Lines[p[1]+1:])
	return editing(t.Lines, target, "Delete the second paragraph.", &Span{p[0], 0, len(t.Lines[p[0]])},
		navThen(t.Lines, Pos{0, 0}, onLines(p[0], p[1]), "dap", "delete a paragraph, with the blank line after it"))
}
