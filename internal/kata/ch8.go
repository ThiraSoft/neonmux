package kata

import (
	"math/rand/v2"
	"strings"
)

// Chapter 8, Visual block and macros: one edit on several lines at once
// (ctrl+v), or recorded once and replayed (qa, @a).

func init() {
	register(
		Def{ID: "block.quote", Name: "Quote these lines", Gesture: "<C-v>I", Chapter: 8, Gen: genBlockQuote},
		Def{ID: "block.append", Name: "Close these lines", Gesture: "<C-v>$A", Chapter: 8, Gen: genBlockAppend},
		Def{ID: "block.delete-prefix", Name: "Remove the dashes", Gesture: "<C-v>d", Chapter: 8, Gen: genBlockDeletePrefix},
		Def{ID: "macro.bold", Name: "Bold the first word of each line", Gesture: "qa @a", Chapter: 8, Gen: genMacroBold},
		Def{ID: "macro.drop-last", Name: "Drop the last word of each line", Gesture: "qa @a", Chapter: 8, Gen: genMacroDropLast},
	)
}

func genBlockQuote(t Text, r *rand.Rand) (Mission, bool) {
	bs := blocks(t.Lines)
	if len(bs) == 0 {
		return Mission{}, false
	}
	k := pick(r, bs)
	target := eachLine(t.Lines, k, func(s string) string { return "> " + s })
	return editingFrom(t.Lines, target, Pos{k, 0}, `Quote these four lines with "> ".`, &Span{k, 0, len(t.Lines[k])},
		Recipe{{"<C-v>3j", "block over the four lines"}, {"I> <Esc>", "insert on each line of the block"}},
		Recipe{{"I> <Esc>", "insert at the start of the line"}, {"j.j.j.", "down, repeat, three times"}})
}

func genBlockAppend(t Text, r *rand.Rand) (Mission, bool) {
	bs := blocks(t.Lines)
	if len(bs) == 0 {
		return Mission{}, false
	}
	k := pick(r, bs)
	target := eachLine(t.Lines, k, func(s string) string { return s + " ." })
	return editingFrom(t.Lines, target, Pos{k, 0}, `End these four lines with " .".`, &Span{k, 0, len(t.Lines[k])},
		Recipe{{"<C-v>3j", "block over the four lines"}, {"$A .<Esc>", "append at the end of each line"}},
		Recipe{{"A .<Esc>", "append at the end of the line"}, {"j.j.j.", "down, repeat, three times"}})
}

func genBlockDeletePrefix(t Text, r *rand.Rand) (Mission, bool) {
	bs := blocks(t.Lines)
	if len(bs) == 0 {
		return Mission{}, false
	}
	k := pick(r, bs)
	start := eachLine(t.Lines, k, func(s string) string { return "- " + s })
	return editingFrom(start, t.Lines, Pos{k, 0}, `Remove the "- " in front of these four lines.`, &Span{k, 0, 2},
		Recipe{{"<C-v>3jl", "block of two columns over the four lines"}, {"d", "delete it"}},
		Recipe{{":,+3s/^- //<CR>", "substitute on this line and the next three"}})
}

func genMacroBold(t Text, r *rand.Rand) (Mission, bool) {
	var ks []int
	for _, k := range blocks(t.Lines) {
		ok := true
		for i := k; i < k+4; i++ {
			ws := wordSpans(t.Lines[i])
			if len(ws) == 0 || ws[0][0] != 0 || !isLetters(t.Lines[i][:ws[0][1]]) {
				ok = false
			}
		}
		if ok {
			ks = append(ks, k)
		}
	}
	if len(ks) == 0 {
		return Mission{}, false
	}
	k := pick(r, ks)
	target := eachLine(t.Lines, k, func(s string) string {
		e := wordSpans(s)[0][1]
		return "**" + s[:e] + "**" + s[e:]
	})
	return editingFrom(t.Lines, target, Pos{k, 0}, "Put the first word of these four lines in **bold**.", &Span{k, 0, len(t.Lines[k])},
		Recipe{{"qa", "record into register a"}, {"I**<Esc>", "insert ** before the word"}, {"ea**<Esc>", "to its end, append **"},
			{"+", "first character of the next line"}, {"q", "stop recording"}, {"3@a", "play the macro three times"}})
}

// lastToken returns where the last word of s starts (after its last space)
// when that word is letters or digits followed by at most one of .,;:
func lastToken(s string) (int, bool) {
	sp := strings.LastIndexByte(s, ' ')
	if sp <= 0 {
		return 0, false
	}
	tok := strings.TrimRight(s[sp+1:], ".,;:")
	if len(s)-sp-1-len(tok) > 1 || len(tok) < 2 || s[sp-1] == ' ' {
		return 0, false
	}
	for i := 0; i < len(tok); i++ {
		if !isKeyword(tok[i]) || tok[i] == '_' {
			return 0, false
		}
	}
	return sp, true
}

func genMacroDropLast(t Text, r *rand.Rand) (Mission, bool) {
	var ks []int
	for _, k := range blocks(t.Lines) {
		ok := true
		for i := k; i < k+4; i++ {
			if _, good := lastToken(t.Lines[i]); !good {
				ok = false
			}
		}
		if ok {
			ks = append(ks, k)
		}
	}
	if len(ks) == 0 {
		return Mission{}, false
	}
	k := pick(r, ks)
	target := eachLine(t.Lines, k, func(s string) string {
		sp, _ := lastToken(s)
		return s[:sp]
	})
	return editingFrom(t.Lines, target, Pos{k, 0}, "Drop the last word of these four lines.", &Span{k, 0, len(t.Lines[k])},
		Recipe{{"qa", "record into register a"}, {"$b", "end of the line, back to the last word"}, {"hD", "from the space before it, delete to the end"},
			{"j", "next line"}, {"q", "stop recording"}, {"3@a", "play the macro three times"}},
		Recipe{{":,+3norm $bhD<CR>", "run the same normal-mode keys on four lines"}})
}
