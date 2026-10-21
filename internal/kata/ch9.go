package kata

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// Chapter 9, Ex commands: :g, :v, :s, :sort and :norm, which act on many
// lines in one go.

func init() {
	register(
		Def{ID: "ex.global", Name: "Delete the lines that mention a word", Gesture: ":g", Chapter: 9, Gen: genExGlobal},
		Def{ID: "ex.vglobal", Name: "Keep only the lines that mention a word", Gesture: ":v", Chapter: 9, Gen: genExVglobal},
		Def{ID: "ex.substitute", Name: "Replace on this line", Gesture: ":s", Chapter: 9, Gen: genExSubstitute},
		Def{ID: "ex.substitute-all", Name: "Replace everywhere", Gesture: ":%s", Chapter: 9, Gen: genExSubstituteAll},
		Def{ID: "ex.sort", Name: "Sort these lines", Gesture: ":sort", Chapter: 9, Gen: genExSort},
		Def{ID: "ex.norm", Name: "Prefix every line", Gesture: ":norm", Chapter: 9, Gen: genExNorm},
	)
}

// mentions returns, for words never found inside a longer word, the lines
// that hold them, when there are between 2 and the number of text lines
// minus one.
func mentions(lines []string) map[string][]int {
	out := map[string][]int{}
	text := len(textLines(lines, func(int) bool { return true }))
	for _, w := range words(lines, 4) {
		if _, done := out[w.text]; done || len(occurrences(lines, w.text)) != len(wholeWords(lines, w.text)) {
			continue
		}
		var ls []int
		for l, s := range lines {
			if strings.Contains(s, w.text) {
				ls = append(ls, l)
			}
		}
		if len(ls) >= 2 && len(ls) < text {
			out[w.text] = ls
		}
	}
	return out
}

func pickKey(r *rand.Rand, m map[string][]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return pick(r, keys)
}

func genExGlobal(t Text, r *rand.Rand) (Mission, bool) {
	ms := mentions(t.Lines)
	if len(ms) == 0 {
		return Mission{}, false
	}
	x := pickKey(r, ms)
	target := slices.DeleteFunc(slices.Clone(t.Lines), func(s string) bool { return strings.Contains(s, x) })
	return editing(t.Lines, target, fmt.Sprintf("Delete every line that contains %q.", x), nil,
		Recipe{{":g/" + x + "/d<CR>", "on every line that matches, delete"}})
}

func genExVglobal(t Text, r *rand.Rand) (Mission, bool) {
	ms := mentions(t.Lines)
	if len(ms) == 0 {
		return Mission{}, false
	}
	x := pickKey(r, ms)
	target := slices.DeleteFunc(slices.Clone(t.Lines), func(s string) bool { return !strings.Contains(s, x) })
	return editing(t.Lines, target, fmt.Sprintf("Keep only the lines that contain %q.", x), nil,
		Recipe{{":v/" + x + "/d<CR>", "on every line that does not match, delete"}})
}

func genExSubstitute(t Text, r *rand.Rand) (Mission, bool) {
	type cand struct {
		l int
		x string
	}
	var cs []cand
	for l, s := range t.Lines {
		seen := map[string]bool{}
		for _, w := range wordSpans(s) {
			x := s[w[0]:w[1]]
			if seen[x] || len(x) < 3 || !isLetters(x) {
				continue
			}
			seen[x] = true
			if n := len(wholeWords([]string{s}, x)); n >= 2 && strings.Count(s, x) == n {
				cs = append(cs, cand{l, x})
			}
		}
	}
	if len(cs) == 0 {
		return Mission{}, false
	}
	c := pick(r, cs)
	y := replacement(r, c.x)
	target := slices.Clone(t.Lines)
	target[c.l] = strings.ReplaceAll(target[c.l], c.x, y)
	return editingFrom(t.Lines, target, Pos{c.l, 0}, fmt.Sprintf("Replace every %q with %q on this line only.", c.x, y),
		&Span{c.l, 0, len(t.Lines[c.l])},
		Recipe{{":s/" + c.x + "/" + y + "/g<CR>", "substitute on this line, every match"}})
}

func genExSubstituteAll(t Text, r *rand.Rand) (Mission, bool) {
	ms := mentions(t.Lines)
	if len(ms) == 0 {
		return Mission{}, false
	}
	x := pickKey(r, ms)
	y := replacement(r, x)
	n := len(wholeWords(t.Lines, x))
	return editing(t.Lines, replaceAll(t.Lines, x, y), fmt.Sprintf("Replace every %q with %q.", x, y), nil,
		Recipe{{":%s/" + x + "/" + y + "/g<CR>", "substitute on every line"}},
		Recipe{{"/" + x + "<CR>", "search for '" + x + "'"}, {"cgn" + y + "<Esc>", "change the match"}, dots(n - 1)})
}

func genExSort(t Text, r *rand.Rand) (Mission, bool) {
	start := slices.DeleteFunc(slices.Clone(t.Lines), func(s string) bool { return s == "" })
	target := slices.Clone(start)
	slices.Sort(target)
	return editing(start, target, "Sort the lines alphabetically.", nil, Recipe{{":sort<CR>", "sort the lines"}})
}

func genExNorm(t Text, r *rand.Rand) (Mission, bool) {
	target := slices.Clone(t.Lines)
	for i := range target {
		target[i] = "- " + target[i]
	}
	return editing(t.Lines, target, `Start every line, blank ones included, with "- ".`, nil,
		Recipe{{":%norm I- <CR>", "run I- on every line"}})
}
