package kata

import (
	"strconv"
	"strings"
)

// A model of Neovim's motions on ASCII text, with nvim --clean defaults
// (noignorecase, wrapscan, nostartofline, iskeyword=@,48-57,_,192-255).
// It follows the algorithms of vim's search.c (fwd_word, bck_word,
// end_word, findpar) closely, and the tests check it against the real
// Neovim on every position of the fixtures. A motion is only offered when
// the model is sure of it: % is left out on lines with quotes, where vim
// skips the brackets inside strings.

// WantEOL is the desired column after $: the end of every line.
const WantEOL = -1

// State is where the cursor is, plus the column vim wants to keep when it
// moves up and down (curswant).
type State struct {
	Pos
	Want int
}

// Edge is a motion from a state.
type Edge struct {
	Keys, Explain string
	To            State
}

// cursor walks the text one character at a time, the way vim's inc() and
// dec() do: the end of a line (col == len) is a place of its own.
type cursor struct {
	buf []string
	p   Pos
}

func (c *cursor) char() byte {
	l := c.buf[c.p.Line]
	if c.p.Col >= len(l) {
		return 0
	}
	return l[c.p.Col]
}

// inc moves forward: 0 within the line, 2 onto the end of the line, 1 onto
// the next line, -1 at the end of the text.
func (c *cursor) inc() int {
	l := c.buf[c.p.Line]
	if c.p.Col < len(l) {
		c.p.Col++
		if c.p.Col < len(l) {
			return 0
		}
		return 2
	}
	if c.p.Line < len(c.buf)-1 {
		c.p = Pos{c.p.Line + 1, 0}
		return 1
	}
	return -1
}

// dec moves backward: from the start of a line it lands on the end of the
// previous one.
func (c *cursor) dec() int {
	if c.p.Col > 0 {
		c.p.Col--
		return 0
	}
	if c.p.Line > 0 {
		c.p.Line--
		c.p.Col = len(c.buf[c.p.Line])
		return 1
	}
	return -1
}

func isKeyword(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

func isBlank(b byte) bool { return b == ' ' || b == '\t' || b == 0 }

// class is vim's cls(): 0 for blanks and line ends, 1 for punctuation, 2
// for keyword characters. For WORDs, every non-blank is 1.
func (c *cursor) class(big bool) int {
	b := c.char()
	switch {
	case isBlank(b):
		return 0
	case big:
		return 1
	case isKeyword(b):
		return 2
	}
	return 1
}

func (c *cursor) emptyLine() bool { return c.p.Col == 0 && c.buf[c.p.Line] == "" }

// adjust keeps the cursor off the end of a line, as normal mode does.
func (c *cursor) adjust() {
	if l := c.buf[c.p.Line]; c.p.Col > 0 && c.p.Col >= len(l) {
		c.p.Col = len(l) - 1
	}
}

func (c *cursor) skip(cls int, big, forward bool) bool {
	for c.class(big) == cls {
		var r int
		if forward {
			r = c.inc()
		} else {
			r = c.dec()
		}
		if r == -1 {
			return true
		}
	}
	return false
}

// fwdWord is w and W.
func fwdWord(buf []string, p Pos, count int, big bool) Pos {
	c := cursor{buf, p}
	for ; count > 0; count-- {
		sclass := c.class(big)
		last := c.p.Line == len(buf)-1
		i := c.inc()
		if i == -1 || i >= 1 && last {
			break
		}
		if sclass != 0 {
			stop := false
			for c.class(big) == sclass {
				if c.inc() == -1 {
					stop = true
					break
				}
			}
			if stop {
				break
			}
		}
		stop := false
		for c.class(big) == 0 {
			if c.emptyLine() {
				break
			}
			if c.inc() == -1 {
				stop = true
				break
			}
		}
		if stop {
			break
		}
	}
	if c.p.Line > p.Line || c.p.Line == p.Line && c.p.Col > p.Col {
		c.adjust()
	}
	return c.p
}

// bckWord is b and B.
func bckWord(buf []string, p Pos, count int, big bool) Pos {
	c := cursor{buf, p}
	for ; count > 0; count-- {
		if c.dec() == -1 {
			break
		}
		finished := false
		for c.class(big) == 0 {
			if c.emptyLine() {
				finished = true
				break
			}
			if c.dec() == -1 {
				return c.p
			}
		}
		if finished {
			continue
		}
		if c.skip(c.class(big), big, false) {
			return c.p
		}
		c.inc()
	}
	return c.p
}

// endWord is e and E.
func endWord(buf []string, p Pos, count int, big bool) Pos {
	c := cursor{buf, p}
	fail := func() Pos { c.adjust(); return c.p }
	for ; count > 0; count-- {
		sclass := c.class(big)
		if c.inc() == -1 {
			return fail()
		}
		if c.class(big) == sclass && sclass != 0 {
			if c.skip(sclass, big, true) {
				return fail()
			}
		} else {
			for c.class(big) == 0 {
				if c.inc() == -1 {
					return fail()
				}
			}
			if c.skip(c.class(big), big, true) {
				return fail()
			}
		}
		c.dec()
	}
	return c.p
}

// findPar is { and }: the next empty line, or the end of the text.
func findPar(buf []string, p Pos, count, dir int) (Pos, bool) {
	cur := p.Line
	for ; count > 0; count-- {
		skipped := false
		for first := true; ; first = false {
			if buf[cur] != "" {
				skipped = true
			}
			if !first && skipped && buf[cur] == "" {
				break
			}
			cur += dir
			if cur < 0 || cur >= len(buf) {
				if count > 1 {
					return p, false
				}
				cur -= dir
				break
			}
		}
	}
	if cur == len(buf)-1 && dir > 0 {
		return Pos{cur, max(len(buf[cur])-1, 0)}, true
	}
	return Pos{cur, 0}, true
}

func lastCol(s string) int { return max(len(s)-1, 0) }

func firstNonBlank(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' {
			return i
		}
	}
	return lastCol(s)
}

// colFor is coladvance(): the wanted column, within the line.
func colFor(s string, want int) int {
	if want == WantEOL {
		return lastCol(s)
	}
	return min(want, lastCol(s))
}

// matchBracket is % from p: the first bracket at or after the cursor on the
// line, then its partner, counting nested pairs.
func matchBracket(buf []string, p Pos) (Pos, bool) {
	line := buf[p.Line]
	i := strings.IndexAny(line[min(p.Col, len(line)):], "(){}[]")
	if i < 0 {
		return p, false
	}
	at := Pos{p.Line, p.Col + i}
	open := line[at.Col]
	var close byte
	dir := 1
	switch open {
	case '(':
		close = ')'
	case '[':
		close = ']'
	case '{':
		close = '}'
	case ')':
		close, dir = '(', -1
	case ']':
		close, dir = '[', -1
	case '}':
		close, dir = '{', -1
	}
	depth := 0
	c := cursor{buf, at}
	for {
		var r int
		if dir > 0 {
			r = c.inc()
		} else {
			r = c.dec()
		}
		if r == -1 {
			return p, false
		}
		switch c.char() {
		case open:
			depth++
		case close:
			if depth == 0 {
				return c.p, true
			}
			depth--
		}
	}
}

// percentSafe reports the lines where % behaves as the model says: without
// double quotes (vim skips the brackets inside strings), comments,
// preprocessor lines, escapes, or a bracket quoted as a character ('(').
// An apostrophe alone, as in a possessive, changes nothing.
func percentSafe(buf []string, from, to int) bool {
	if from > to {
		from, to = to, from
	}
	for l := from; l <= to; l++ {
		s := buf[l]
		if strings.ContainsAny(s, `"#/\`) {
			return false
		}
		for i := 1; i+1 < len(s); i++ {
			if strings.IndexByte("(){}[]", s[i]) >= 0 && s[i-1] == '\'' && s[i+1] == '\'' {
				return false
			}
		}
	}
	return true
}

// wordAt returns the keyword under p and where it starts.
func wordAt(s string, col int) (string, int) {
	if col >= len(s) || !isKeyword(s[col]) {
		return "", 0
	}
	a, b := col, col
	for a > 0 && isKeyword(s[a-1]) {
		a--
	}
	for b < len(s) && isKeyword(s[b]) {
		b++
	}
	return s[a:b], a
}

// wholeWords lists the places where word stands as a whole word.
func wholeWords(buf []string, word string) []Pos {
	var out []Pos
	for l, s := range buf {
		for i := 0; i+len(word) <= len(s); {
			j := strings.Index(s[i:], word)
			if j < 0 {
				break
			}
			j += i
			if (j == 0 || !isKeyword(s[j-1])) && (j+len(word) == len(s) || !isKeyword(s[j+len(word)])) {
				out = append(out, Pos{l, j})
			}
			i = j + 1
		}
	}
	return out
}

func before(a, b Pos) bool { return a.Line < b.Line || a.Line == b.Line && a.Col < b.Col }

// nextMatch returns the first place after p in ps (sorted), wrapping
// around; prev the last one before p.
func nextMatch(ps []Pos, p Pos) Pos {
	for _, q := range ps {
		if before(p, q) {
			return q
		}
	}
	return ps[0]
}

func prevMatch(ps []Pos, p Pos) Pos {
	for i := len(ps) - 1; i >= 0; i-- {
		if before(ps[i], p) {
			return ps[i]
		}
	}
	return ps[len(ps)-1]
}

var countWords = []string{"", "", "two", "three", "four", "five", "six", "seven", "eight", "nine"}

// Moves lists the motions from s that change the state, with where they
// land. Searches (/) are added by the navigation, which knows the goal.
func Moves(buf []string, s State) []Edge {
	var out []Edge
	line := buf[s.Line]
	add := func(keys, explain string, to Pos, want int) {
		st := State{to, want}
		if st != s {
			out = append(out, Edge{keys, explain, st})
		}
	}
	// at keeps the column as the wanted one.
	at := func(keys, explain string, to Pos) { add(keys, explain, to, to.Col) }

	for n := 1; n <= 9; n++ {
		prefix, times := "", ""
		if n > 1 {
			prefix, times = strconv.Itoa(n), countWords[n]+" "
		}
		plural := func(word string) string {
			if n > 1 {
				return times + word + "s"
			}
			return word
		}
		if c := max(s.Col-n, 0); s.Col > 0 && (n == 1 || s.Col-n >= 0) {
			at(prefix+"h", plural("character")+" left", Pos{s.Line, c})
		}
		if l := lastCol(line); s.Col < l && (n == 1 || s.Col+n <= l) {
			at(prefix+"l", plural("character")+" right", Pos{s.Line, min(s.Col+n, l)})
		}
		if s.Line < len(buf)-1 && (n == 1 || s.Line+n <= len(buf)-1) {
			l := min(s.Line+n, len(buf)-1)
			add(prefix+"j", plural("line")+" down", Pos{l, colFor(buf[l], s.Want)}, s.Want)
		}
		if s.Line > 0 && (n == 1 || s.Line-n >= 0) {
			l := max(s.Line-n, 0)
			add(prefix+"k", plural("line")+" up", Pos{l, colFor(buf[l], s.Want)}, s.Want)
		}
		at(prefix+"w", ifn(n, "next word", times+"words forward"), fwdWord(buf, s.Pos, n, false))
		at(prefix+"b", ifn(n, "start of the word (or the previous one)", times+"words back"), bckWord(buf, s.Pos, n, false))
		at(prefix+"e", ifn(n, "end of the word (or the next one)", "end of the "+ordinal(n)+" word"), endWord(buf, s.Pos, n, false))
		at(prefix+"W", ifn(n, "next WORD (spaces only end it)", times+"WORDs forward"), fwdWord(buf, s.Pos, n, true))
		at(prefix+"B", ifn(n, "previous WORD", times+"WORDs back"), bckWord(buf, s.Pos, n, true))
		at(prefix+"E", ifn(n, "end of the WORD", "end of the "+ordinal(n)+" WORD"), endWord(buf, s.Pos, n, true))
		if to, ok := findPar(buf, s.Pos, n, 1); ok {
			at(prefix+"}", ifn(n, "next blank line (end of the paragraph)", times+"paragraphs forward"), to)
		}
		if to, ok := findPar(buf, s.Pos, n, -1); ok {
			at(prefix+"{", ifn(n, "previous blank line (start of the paragraph)", times+"paragraphs back"), to)
		}
		findChars(line, s, n, prefix, at)
	}

	at("0", "start of the line", Pos{s.Line, 0})
	at("^", "first non-blank character", Pos{s.Line, firstNonBlank(line)})
	add("$", "end of the line", Pos{s.Line, lastCol(line)}, WantEOL)
	add("gg", "first line", Pos{0, colFor(buf[0], s.Want)}, s.Want)
	add("G", "last line", Pos{len(buf) - 1, colFor(buf[len(buf)-1], s.Want)}, s.Want)
	for l := 1; l < len(buf)-1; l++ {
		add(strconv.Itoa(l+1)+"G", "line "+strconv.Itoa(l+1), Pos{l, colFor(buf[l], s.Want)}, s.Want)
	}
	if s.Line < len(buf)-1 {
		at("+", "first character of the next line", Pos{s.Line + 1, firstNonBlank(buf[s.Line+1])})
	}
	if s.Line > 0 {
		at("-", "first character of the previous line", Pos{s.Line - 1, firstNonBlank(buf[s.Line-1])})
	}
	if to, ok := matchBracket(buf, s.Pos); ok && percentSafe(buf, s.Line, to.Line) {
		at("%", "matching bracket", to)
	}
	if w, start := wordAt(line, s.Col); w != "" {
		ps := wholeWords(buf, w)
		from := Pos{s.Line, start}
		at("*", "next occurrence of the word under the cursor", nextMatch(ps, from))
		at("#", "previous occurrence of the word under the cursor", prevMatch(ps, from))
	}
	return out
}

func ifn(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func ordinal(n int) string {
	return []string{"", "first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth"}[n]
}

// findChars adds f, F, t and T toward every character of the line, the
// count giving the n-th occurrence.
func findChars(line string, s State, n int, prefix string, at func(string, string, Pos)) {
	seen := map[byte]int{}
	for i := s.Col + 1; i < len(line); i++ {
		c := line[i]
		seen[c]++
		if seen[c] != n {
			continue
		}
		at(prefix+"f"+keyChar(c), ifn(n, "next "+quote(c)+" on the line", ordinal(n)+" "+quote(c)+" to the right"), Pos{s.Line, i})
		at(prefix+"t"+keyChar(c), ifn(n, "just before the next "+quote(c), "just before the "+ordinal(n)+" "+quote(c)), Pos{s.Line, i - 1})
	}
	clear(seen)
	for i := s.Col - 1; i >= 0; i-- {
		c := line[i]
		seen[c]++
		if seen[c] != n {
			continue
		}
		at(prefix+"F"+keyChar(c), ifn(n, "previous "+quote(c)+" on the line", ordinal(n)+" "+quote(c)+" to the left"), Pos{s.Line, i})
		at(prefix+"T"+keyChar(c), ifn(n, "just after the previous "+quote(c), "just after the "+ordinal(n)+" "+quote(c)+" to the left"), Pos{s.Line, i + 1})
	}
}

// keyChar writes a character in key notation.
func keyChar(c byte) string {
	if c == '<' {
		return "<lt>"
	}
	return string(c)
}

func quote(c byte) string {
	if c == ' ' {
		return "space"
	}
	return "'" + string(c) + "'"
}
