package ui

import (
	"strings"
	"unicode"

	"github.com/ThiraSoft/neonmux/vt"
)

// Vim motions of the copy mode beyond the plain ones: words, ^, f F t T
// with ; and , then % and the * # searches.

// isKeyword tells whether r belongs to a vim word (w e b, * #).
func isKeyword(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// wordCursor walks the copy mode text cell by cell across lines, for the
// word motions. Unlike textWalk.next, it stops on the line breaks: each
// line ends with a virtual blank at col n, and an empty line is a word.
type wordCursor struct {
	textWalk
	col int
}

// class is 0 for a blank or a line break, 1 for a word, 2 for other
// characters, 3 for an empty line. With big, words are any run of
// non-blanks (W B E).
func (w *wordCursor) class(big bool) int {
	if w.n == 0 {
		return 3
	}
	if w.col >= w.n {
		return 0
	}
	r := w.line[w.col].Rune()
	switch {
	case r == 0 || unicode.IsSpace(r):
		return 0
	case big || isKeyword(r):
		return 1
	}
	return 2
}

// next and prev step one cell, false at the ends of the copy mode text.
func (w *wordCursor) next() bool {
	if w.col >= w.n {
		if w.row >= w.last {
			return false
		}
		w.load(w.row + 1)
		w.col = 0
		return true
	}
	w.col++
	for w.col < w.n && w.line[w.col].Attr()&vt.AttrWideCont != 0 {
		w.col++
	}
	return true
}

func (w *wordCursor) prev() bool {
	if w.col == 0 {
		if w.row <= w.first {
			return false
		}
		w.load(w.row - 1)
		w.col = w.n
		return true
	}
	w.col--
	for w.col > 0 && w.line[w.col].Attr()&vt.AttrWideCont != 0 {
		w.col--
	}
	return true
}

// copyWord is vim's w, b, e and their big variants W, B, E, or ge and
// gE with r 'g' and 'G'.
func (p *TermPane) copyWord(r rune) {
	c := p.cm
	p.t.Lock()
	defer p.t.Unlock()
	w := &wordCursor{textWalk: textWalk{p: p}}
	w.first, w.last = p.copyRows()
	w.load(min(max(c.pos.row, w.first), w.last))
	w.col = min(max(c.pos.col, 0), w.n)
	for w.col > 0 && w.col < w.n && w.line[w.col].Attr()&vt.AttrWideCont != 0 {
		w.col--
	}
	big := unicode.IsUpper(r)
	switch unicode.ToLower(r) {
	case 'w':
		k := w.class(big)
		ok := w.next()
		for ok && (k == 1 || k == 2) && w.class(big) == k {
			ok = w.next()
		}
		for ok && w.class(big) == 0 {
			ok = w.next()
		}
	case 'e':
		ok := w.next()
		for ok && (w.class(big) == 0 || w.class(big) == 3) {
			ok = w.next()
		}
		for k := w.class(big); ok && w.next(); {
			if w.class(big) != k {
				w.prev()
				break
			}
		}
	case 'b':
		ok := w.prev()
		for ok && w.class(big) == 0 {
			ok = w.prev()
		}
		for k := w.class(big); ok && (k == 1 || k == 2) && w.prev(); {
			if w.class(big) != k {
				w.next()
				break
			}
		}
	case 'g':
		k := w.class(big)
		ok := w.prev()
		for ok && (k == 1 || k == 2) && w.class(big) == k {
			ok = w.prev()
		}
		for ok && w.class(big) == 0 {
			ok = w.prev()
		}
	}
	if w.col >= w.n {
		w.col = max(w.n-1, 0)
	}
	c.pos = selPos{w.row, w.col}
}

// lineChars returns the line of the cursor and the columns of its
// characters, past the continuation cells and the trailing blanks, with
// the index of the character under the cursor.
func (p *TermPane) lineChars() (line []vt.Cell, cols []int, at int) {
	c := p.cm
	line = p.copyLine(c.pos.row)
	n := len(line)
	for n > 0 && line[n-1].IsBlank() {
		n--
	}
	for x := range n {
		if line[x].Attr()&vt.AttrWideCont != 0 {
			continue
		}
		if x <= c.pos.col {
			at = len(cols)
		}
		cols = append(cols, x)
	}
	return line, cols, at
}

// firstNonBlank is vim's ^.
func (p *TermPane) firstNonBlank() {
	line, cols, _ := p.lineChars()
	p.cm.pos.col = 0
	for _, x := range cols {
		if r := line[x].Rune(); r != 0 && !unicode.IsSpace(r) {
			p.cm.pos.col = x
			return
		}
	}
}

// findChar is vim's f F t T on the cursor line: the nth ch after or
// before the cursor, or the cell next to it for t and T. Repeated by ; or
// , a t or T looks past the character it stopped against.
func (p *TermPane) findChar(kind, ch rune, n int, repeat bool) {
	line, cols, at := p.lineChars()
	if len(cols) == 0 {
		return
	}
	dir, till := 1, kind == 't' || kind == 'T'
	if kind == 'F' || kind == 'T' {
		dir = -1
	}
	i := at + dir
	if till && repeat {
		i += dir
	}
	for ; i >= 0 && i < len(cols); i += dir {
		r := line[cols[i]].Rune()
		if r == 0 {
			r = ' '
		}
		if r == ch {
			if n--; n == 0 {
				break
			}
		}
	}
	if i < 0 || i >= len(cols) {
		return // not found: vim stays put
	}
	if till {
		i -= dir
	}
	p.cm.pos.col = cols[i]
}

// repeatFind is ; or, with reverse, the , of vim.
func (p *TermPane) repeatFind(n int, reverse bool) {
	c := p.cm
	kind, ch := c.lastFind[0], c.lastFind[1]
	if kind == 0 {
		return
	}
	if reverse {
		kind = map[rune]rune{'f': 'F', 'F': 'f', 't': 'T', 'T': 't'}[kind]
	}
	p.findChar(kind, ch, n, true)
}

// matchPair is vim's %: from the first bracket under or after the cursor
// on its line, the one that matches it, across lines.
func (p *TermPane) matchPair() {
	c := p.cm
	p.t.Lock()
	defer p.t.Unlock()
	w := &textWalk{p: p}
	w.first, w.last = p.copyRows()
	w.load(c.pos.row)
	for x := max(c.pos.col, 0); x < w.n; x++ {
		r := w.line[x].Rune()
		i := strings.IndexRune("([{)]}", r)
		if i < 0 {
			continue
		}
		var to selPos
		var ok bool
		if i < 3 {
			to, ok = w.closing(selPos{c.pos.row, x}, r, rune(")]}"[i]))
		} else {
			to, ok = w.outward(selPos{c.pos.row, x}, rune("([{"[i-3]), r)
		}
		if ok {
			c.pos = to
		}
		return
	}
}

// starSearch is vim's * and, with back, #: search the word under the
// cursor, or the next one on its line, as a whole word with the case
// ignored.
func (p *TermPane) starSearch(back bool) {
	c := p.cm
	line, cols, at := p.lineChars()
	keyword := func(i int) bool { return i >= 0 && i < len(cols) && isKeyword(line[cols[i]].Rune()) }
	i := at
	for i < len(cols) && !keyword(i) {
		i++
	}
	if i == len(cols) {
		return
	}
	for keyword(i - 1) {
		i--
	}
	var word []rune
	for j := i; keyword(j); j++ {
		word = append(word, line[cols[j]].Rune())
	}
	c.pat, c.word, c.back, c.hitsOK = append(c.pat[:0], word...), true, back, false
	c.pos.col = cols[i]
	p.copySearch(back)
}
