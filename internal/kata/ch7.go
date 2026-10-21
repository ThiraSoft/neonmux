package kata

import (
	"math/rand/v2"
	"slices"
)

// Chapter 7, Moving text: deleted text goes to a register, p puts it back
// elsewhere; ctrl+r inserts a register while typing.

func init() {
	register(
		Def{ID: "move.swap-lines", Name: "Swap these two lines", Gesture: "ddp", Chapter: 7, Gen: genMoveSwapLines},
		Def{ID: "move.swap-paragraphs", Name: "Swap the first two paragraphs", Gesture: "dap}p", Chapter: 7, Gen: genMoveSwapParagraphs},
		Def{ID: "move.line-to-end", Name: "Move this line to the end", Gesture: "ddGp", Chapter: 7, Gen: genMoveLineToEnd},
		Def{ID: "move.line-to-top", Name: "Move this line to the top", Gesture: "ddggP", Chapter: 7, Gen: genMoveLineToTop},
		Def{ID: "move.reuse-word", Name: "Reuse a long word", Gesture: "<C-r>", Chapter: 7, Gen: genMoveReuseWord},
	)
}

// textLines lists the non-blank lines that satisfy keep.
func textLines(lines []string, keep func(int) bool) []int {
	var out []int
	for l, s := range lines {
		if s != "" && keep(l) {
			out = append(out, l)
		}
	}
	return out
}

func genMoveSwapLines(t Text, r *rand.Rand) (Mission, bool) {
	ks := textLines(t.Lines, func(l int) bool { return l+1 < len(t.Lines) && t.Lines[l+1] != "" })
	if len(ks) == 0 {
		return Mission{}, false
	}
	k := pick(r, ks)
	target := slices.Clone(t.Lines)
	target[k], target[k+1] = target[k+1], target[k]
	return editingFrom(t.Lines, target, Pos{k, 0}, "Swap this line with the next one.", &Span{k, 0, len(t.Lines[k])},
		Recipe{{"dd", "cut the line"}, {"p", "put it below the next one"}},
		Recipe{{":m+<CR>", "move the line one down"}})
}

func genMoveSwapParagraphs(t Text, r *rand.Rand) (Mission, bool) {
	ps := paragraphs(t.Lines)
	if len(ps) < 3 {
		return Mission{}, false
	}
	p1, p2 := ps[0], ps[1]
	target := slices.Concat(t.Lines[p2[0]:p2[1]], []string{""}, t.Lines[p1[0]:p1[1]], t.Lines[p2[1]:])
	return editing(t.Lines, target, "Swap the first two paragraphs.", nil,
		Recipe{{"dap", "delete the paragraph and its blank line"}, {"}", "to the blank line after the next paragraph"}, {"p", "put the paragraph below it"}})
}

func genMoveLineToEnd(t Text, r *rand.Rand) (Mission, bool) {
	ks := textLines(t.Lines, func(l int) bool { return l < len(t.Lines)-1 })
	if len(ks) == 0 {
		return Mission{}, false
	}
	k := pick(r, ks)
	target := append(slices.Concat(t.Lines[:k], t.Lines[k+1:]), t.Lines[k])
	return editingFrom(t.Lines, target, Pos{k, 0}, "Move this line to the end of the text.", &Span{k, 0, len(t.Lines[k])},
		Recipe{{"dd", "cut the line"}, {"G", "last line"}, {"p", "put it below"}},
		Recipe{{":m$<CR>", "move the line after the last one"}})
}

func genMoveLineToTop(t Text, r *rand.Rand) (Mission, bool) {
	ks := textLines(t.Lines, func(l int) bool { return l > 0 })
	if len(ks) == 0 {
		return Mission{}, false
	}
	k := pick(r, ks)
	target := slices.Concat([]string{t.Lines[k]}, t.Lines[:k], t.Lines[k+1:])
	return editingFrom(t.Lines, target, Pos{k, 0}, "Move this line to the top of the text.", &Span{k, 0, len(t.Lines[k])},
		Recipe{{"dd", "cut the line"}, {"gg", "first line"}, {"P", "put it above"}},
		Recipe{{":m0<CR>", "move the line before the first one"}})
}

func genMoveReuseWord(t Text, r *rand.Rand) (Mission, bool) {
	ws := words(t.Lines, 9)
	if len(ws) == 0 {
		return Mission{}, false
	}
	w := pick(r, ws)
	last := len(t.Lines) - 1
	target := edit(t.Lines, last, len(t.Lines[last]), len(t.Lines[last]), " "+w.text)
	return editingFrom(t.Lines, target, Pos{w.l, w.a}, `Add " `+w.text+`" at the end of the last line.`, &Span{w.l, w.a, w.b},
		Recipe{{"yiw", "copy the word under the cursor (register 0)"}, {"G", "last line"}, {"A ", "append, type a space"},
			{"<C-r>0", "insert register 0 while typing"}, {"<Esc>", "leave insert mode"}},
		Recipe{{"G", "last line"}, {"A " + w.text + "<Esc>", "append and type the word"}})
}
