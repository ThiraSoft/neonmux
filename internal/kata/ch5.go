package kata

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// Chapter 5, Repeat: one change, then . (and n, cgn) for the others.

func init() {
	register(
		Def{ID: "rep.rename", Name: "Rename every occurrence", Gesture: "cgn", Chapter: 5, Gen: genRepRename},
		Def{ID: "rep.delete-each", Name: "Delete every occurrence", Gesture: "dgn", Chapter: 5, Gen: genRepDeleteEach},
		Def{ID: "rep.append", Name: "End these lines with a semicolon", Gesture: "A .", Chapter: 5, Gen: genRepAppend},
		Def{ID: "rep.prefix", Name: "Start these lines with a dash", Gesture: "I .", Chapter: 5, Gen: genRepPrefix},
		Def{ID: "rep.capitalize", Name: "Capitalize every occurrence", Gesture: "~ n.", Chapter: 5, Gen: genRepCapitalize},
	)
}

// repeated lists the words found n times (between lo and hi) as whole
// words and never inside a longer word, so that a plain search finds
// exactly them.
func repeated(lines []string, minLen, lo, hi int, keep func(string) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words(lines, minLen) {
		if seen[w.text] || !keep(w.text) {
			continue
		}
		seen[w.text] = true
		n := len(wholeWords(lines, w.text))
		if n >= lo && n <= hi && len(occurrences(lines, w.text)) == n {
			out = append(out, w.text)
		}
	}
	return out
}

func anyWord(string) bool { return true }

// replaceAll replaces every occurrence of x by y.
func replaceAll(lines []string, x, y string) []string {
	out := slices.Clone(lines)
	for i := range out {
		out[i] = strings.ReplaceAll(out[i], x, y)
	}
	return out
}

func dots(n int) Step {
	return Step{strings.Repeat(".", n), "repeat the change on each next one"}
}

func genRepRename(t Text, r *rand.Rand) (Mission, bool) {
	xs := repeated(t.Lines, 4, 3, 5, anyWord)
	if len(xs) == 0 {
		return Mission{}, false
	}
	x := pick(r, xs)
	y := replacement(r, x)
	n := len(wholeWords(t.Lines, x))
	return editing(t.Lines, replaceAll(t.Lines, x, y), fmt.Sprintf("Rename every %q to %q.", x, y), nil,
		Recipe{{"/" + x + "<CR>", "search for '" + x + "'"}, {"cgn" + y + "<Esc>", "change the match (gn selects it)"}, dots(n - 1)},
		Recipe{{":%s/" + x + "/" + y + "/g<CR>", "substitute on every line"}})
}

func genRepDeleteEach(t Text, r *rand.Rand) (Mission, bool) {
	xs := repeated(t.Lines, 3, 3, 5, anyWord)
	var ok []string
	for _, x := range xs {
		if len(occurrences(t.Lines, x+" ")) == len(wholeWords(t.Lines, x)) {
			ok = append(ok, x)
		}
	}
	if len(ok) == 0 {
		return Mission{}, false
	}
	x := pick(r, ok)
	n := len(wholeWords(t.Lines, x))
	return editing(t.Lines, replaceAll(t.Lines, x+" ", ""), fmt.Sprintf("Delete every %q and its space.", x), nil,
		Recipe{{"/" + x + " <CR>", "search for '" + x + " '"}, {"dgn", "delete the match"}, dots(n - 1)},
		Recipe{{":%s/" + x + " //g<CR>", "substitute with nothing on every line"}})
}

// blocks lists the runs of 4 non-blank lines, by their first line.
func blocks(lines []string) []int {
	var out []int
	for _, p := range paragraphs(lines) {
		for k := p[0]; k+4 <= p[1]; k++ {
			out = append(out, k)
		}
	}
	return out
}

// eachLine changes the lines k to k+3.
func eachLine(lines []string, k int, f func(string) string) []string {
	out := slices.Clone(lines)
	for i := k; i < k+4; i++ {
		out[i] = f(out[i])
	}
	return out
}

func genRepAppend(t Text, r *rand.Rand) (Mission, bool) {
	bs := blocks(t.Lines)
	if len(bs) == 0 {
		return Mission{}, false
	}
	k := pick(r, bs)
	target := eachLine(t.Lines, k, func(s string) string { return s + ";" })
	return editingFrom(t.Lines, target, Pos{k, 0}, "End these four lines with a semicolon.", &Span{k, 0, len(t.Lines[k])},
		Recipe{{"A;<Esc>", "append at the end of the line"}, {"j.j.j.", "down, repeat, three times"}},
		Recipe{{"<C-v>3j", "block over the four lines"}, {"$A;<Esc>", "append at the end of each line"}})
}

func genRepPrefix(t Text, r *rand.Rand) (Mission, bool) {
	bs := blocks(t.Lines)
	if len(bs) == 0 {
		return Mission{}, false
	}
	k := pick(r, bs)
	target := eachLine(t.Lines, k, func(s string) string { return "- " + s })
	return editingFrom(t.Lines, target, Pos{k, 0}, `Start these four lines with "- ".`, &Span{k, 0, len(t.Lines[k])},
		Recipe{{"I- <Esc>", "insert at the start of the line"}, {"j.j.j.", "down, repeat, three times"}},
		Recipe{{"<C-v>3j", "block over the four lines"}, {"I- <Esc>", "insert on each line of the block"}})
}

func genRepCapitalize(t Text, r *rand.Rand) (Mission, bool) {
	lower := func(s string) bool { return strings.ToLower(s) == s }
	xs := repeated(t.Lines, 4, 3, 4, lower)
	if len(xs) == 0 {
		return Mission{}, false
	}
	x := pick(r, xs)
	up := strings.ToUpper(x[:1]) + x[1:]
	n := len(wholeWords(t.Lines, x))
	return editing(t.Lines, replaceAll(t.Lines, x, up), fmt.Sprintf("Capitalize every %q.", x), nil,
		Recipe{{"/" + x + "<CR>", "search for '" + x + "'"}, {"~", "switch the case of the first letter"},
			{strings.Repeat("n.", n-1), "next match, repeat"}},
		Recipe{{":%s/" + x + "/" + up + "/g<CR>", "substitute on every line"}})
}
