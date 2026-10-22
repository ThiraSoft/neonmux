package ui

import (
	"unicode"

	"github.com/ThiraSoft/neonmux/vt"
)

// Vim text objects of the copy mode: after v or V, i or a then the
// object. Repeated on a selection already made, or given a count (v2iw),
// an object grows the selection the way vim does.

// textObject selects the object named by r, inner or around. With grow
// (the count past the first), it grows the selection even when it holds a
// single character.
func (p *TermPane) textObject(r rune, around, grow bool) {
	c := p.cm
	c.grow = grow || c.selected()
	switch r {
	case 'p':
		p.selectParagraph(around)
	case 'w', 'W':
		p.selectWordObject(around, r == 'W')
	case '(', ')', 'b':
		p.selectPair('(', ')', around)
	case '[', ']':
		p.selectPair('[', ']', around)
	case '{', '}', 'B':
		p.selectPair('{', '}', around)
	case '<', '>':
		p.selectPair('<', '>', around)
	case '"', '\'', '`':
		p.selectQuote(r, around)
	}
}

// selected tells whether the selection spans more than the cursor: an
// object then grows it instead of starting over.
func (c *copyMode) selected() bool {
	if c.visual == 2 {
		return c.anchor.row != c.pos.row
	}
	return c.anchor != c.pos
}

// span returns the selection in text order.
func (c *copyMode) span() (a, b selPos) {
	if c.pos.less(c.anchor) {
		return c.pos, c.anchor
	}
	return c.anchor, c.pos
}

// selectParagraph is vim's ip, or ap with around: the lines of the
// paragraph under the cursor (or the run of blank lines it sits on), plus
// the blank lines after it for ap, or before it when none follow. On a
// selection, the next paragraph past the cursor joins it.
func (p *TermPane) selectParagraph(around bool) {
	c := p.cm
	p.t.Lock()
	defer p.t.Unlock()
	first, last := p.copyRows()
	// run returns the end of the run of lines as blank as row, going dir.
	run := func(row, dir int64) int64 {
		blank := p.blankRow(row)
		for row+dir >= first && row+dir <= last && p.blankRow(row+dir) == blank {
			row += dir
		}
		return row
	}
	if c.grow {
		dir := int64(1)
		if c.pos.row < c.anchor.row {
			dir = -1
		}
		row := c.pos.row + dir
		if row < first || row > last {
			return
		}
		row = run(row, dir)
		if around && row+dir >= first && row+dir <= last {
			row = run(row+dir, dir)
		}
		c.pos.row, c.visual = row, 2
		return
	}
	row := min(max(c.pos.row, first), last)
	a, b := run(row, -1), run(row, 1)
	if c.visual == 2 && a == b && row+1 <= last {
		// As in vim, Vip on a single line would stay put: grow instead.
		c.anchor = c.pos
		b = run(row+1, 1)
		if around && b < last {
			b = run(b+1, 1)
		}
		c.pos.row = b
		return
	}
	if around {
		if b < last {
			b = run(b+1, 1)
		} else if a > first {
			a = run(a-1, -1)
		}
	}
	c.anchor, c.pos, c.visual = selPos{a, 0}, selPos{b, 0}, 2
}

// selectWordObject is vim's iw, or aw with around: the run of word
// characters, blanks or other characters under the cursor, plus the blanks
// after it for aw, or before it when none follow. Word characters are
// the ones a double click selects (paths, URLs), or any non-blank with
// big (iW aW). On a selection, the next run past the cursor joins it,
// within the line.
func (p *TermPane) selectWordObject(around, big bool) {
	c := p.cm
	line := p.copyLine(c.pos.row)
	class := func(x int) int {
		for x > 0 && x < len(line) && line[x].Attr()&vt.AttrWideCont != 0 {
			x--
		}
		if x < 0 || x >= len(line) {
			return 0
		}
		switch r := line[x].Rune(); {
		case r == 0 || unicode.IsSpace(r):
			return 0
		case big || isWordRune(r):
			return 1
		}
		return 2
	}
	// The blank cells that pad the line are not blanks of the text.
	n := len(line)
	for n > 0 && class(n-1) == 0 {
		n--
	}
	n = max(n, c.pos.col+1)
	// run returns the end of the run of x's class, going dir.
	run := func(x, dir int) int {
		k := class(x)
		for x+dir >= 0 && x+dir < n && class(x+dir) == k {
			x += dir
		}
		return x
	}
	// grow adds to the run ending at x, going dir, the next run for aw:
	// the blanks after a word, or the word after blanks.
	grow := func(start, x, dir int) int {
		if !around || x+dir < 0 || x+dir >= n {
			return x
		}
		if (class(start) == 0) != (class(x+dir) == 0) {
			return run(x+dir, dir)
		}
		return x
	}
	if c.visual == 2 {
		c.visual = 1
	}
	if c.grow {
		dir := 1
		if c.pos.less(c.anchor) {
			dir = -1
		}
		x := c.pos.col + dir
		if x < 0 || x >= n {
			return
		}
		c.pos.col = grow(x, run(x, dir), dir)
		return
	}
	x := min(max(c.pos.col, 0), n-1)
	a, b := run(x, -1), run(x, 1)
	if nb := grow(x, b, 1); nb != b || class(x) == 0 {
		b = nb
	} else {
		a = grow(x, a, -1)
	}
	c.anchor, c.pos = selPos{c.pos.row, a}, selPos{c.pos.row, b}
}

// textWalk steps through the characters of the copy mode text, across
// lines, under the terminal lock. A decoded line is kept for the next
// steps on it.
type textWalk struct {
	p           *TermPane
	first, last int64
	row         int64
	line        []vt.Cell
	n           int // length without the padding
}

func (w *textWalk) load(row int64) {
	if w.line != nil && w.row == row {
		return
	}
	w.line, _ = w.p.textLine(row)
	w.row, w.n = row, len(w.line)
	for w.n > 0 && w.line[w.n-1].IsBlank() {
		w.n--
	}
}

func (w *textWalk) at(pos selPos) rune {
	w.load(pos.row)
	if pos.col < 0 || pos.col >= w.n {
		return ' '
	}
	return w.line[pos.col].Rune()
}

// lineEnd returns the column of the last character of row, or -1.
func (w *textWalk) lineEnd(row int64) int {
	w.load(row)
	return w.n - 1
}

// next returns the character after pos, past the continuation cells of
// wide characters and on to the following lines.
func (w *textWalk) next(pos selPos) (selPos, bool) {
	w.load(pos.row)
	for pos.col++; pos.col < w.n; pos.col++ {
		if w.line[pos.col].Attr()&vt.AttrWideCont == 0 {
			return pos, true
		}
	}
	for row := pos.row + 1; row <= w.last; row++ {
		if w.lineEnd(row) >= 0 {
			return selPos{row, 0}, true
		}
	}
	return pos, false
}

// prev returns the character before pos.
func (w *textWalk) prev(pos selPos) (selPos, bool) {
	w.load(pos.row)
	for pos.col = min(pos.col, w.n) - 1; pos.col >= 0; pos.col-- {
		if w.line[pos.col].Attr()&vt.AttrWideCont == 0 {
			return pos, true
		}
	}
	for row := pos.row - 1; row >= w.first; row-- {
		if e := w.lineEnd(row); e >= 0 {
			return selPos{row, e}, true
		}
	}
	return pos, false
}

// selectPair is vim's i( a( and the like: what lies between the pair of
// brackets around the cursor, across lines, the brackets included with
// around. On a selection, or with a count, the next pair out.
func (p *TermPane) selectPair(open, close rune, around bool) {
	c := p.cm
	p.t.Lock()
	defer p.t.Unlock()
	w := &textWalk{p: p}
	w.first, w.last = p.copyRows()
	sa, sb := c.span()
	grow := c.grow

	// The opening bracket around the cursor: the one under it, or the
	// first one left open going back.
	o, ok := c.pos, true
	if w.at(o) != open {
		o, ok = w.outward(o, open, close)
	}
	for ok {
		cl, found := w.closing(o, open, close)
		if !found {
			return
		}
		a, b := o, cl
		if !around {
			a, b = w.inner(o, cl)
		}
		// Growing: the pair must hold the selection and be larger.
		if !grow || !sa.less(a) && !b.less(sb) && (a != sa || b != sb) {
			if b.less(a) {
				return // empty: (), nothing to select
			}
			c.anchor, c.pos, c.visual = a, b, 1
			return
		}
		o, ok = w.outward(o, open, close)
	}
}

// outward returns the opening bracket left open before pos.
func (w *textWalk) outward(pos selPos, open, close rune) (selPos, bool) {
	depth := 0
	for {
		var ok bool
		if pos, ok = w.prev(pos); !ok {
			return pos, false
		}
		switch w.at(pos) {
		case close:
			depth++
		case open:
			if depth == 0 {
				return pos, true
			}
			depth--
		}
	}
}

// closing returns the bracket that closes the one at o.
func (w *textWalk) closing(o selPos, open, close rune) (selPos, bool) {
	depth := 0
	pos := o
	for {
		var ok bool
		if pos, ok = w.next(pos); !ok {
			return pos, false
		}
		switch w.at(pos) {
		case open:
			depth++
		case close:
			if depth == 0 {
				return pos, true
			}
			depth--
		}
	}
}

// inner returns what lies between the brackets at o and cl. As in vim, a
// bracket ending its line or one with only blanks before it on its line
// leaves that line out, so that i{ on a block takes its lines and nothing
// else.
func (w *textWalk) inner(o, cl selPos) (a, b selPos) {
	a, _ = w.next(o)
	if o.col == w.lineEnd(o.row) && a.row > o.row && a.less(cl) {
		a = selPos{o.row + 1, 0}
	}
	b, _ = w.prev(cl)
	w.load(cl.row)
	onlyBlanks := true
	for x := range cl.col {
		if !w.line[x].IsBlank() {
			onlyBlanks = false
			break
		}
	}
	if onlyBlanks && cl.row > o.row {
		b = selPos{cl.row - 1, max(w.lineEnd(cl.row-1), 0)}
	}
	if a == cl || b.less(a) {
		return cl, o // empty
	}
	return a, b
}

// selectQuote is vim's i" a" and the like, within the line: the text
// between the quotes around the cursor, the quotes and the blanks after
// them (or before them when none follow) with around. Repeated, i" takes
// the quotes as well.
func (p *TermPane) selectQuote(q rune, around bool) {
	c := p.cm
	line := p.copyLine(c.pos.row)
	n := len(line)
	for n > 0 && line[n-1].IsBlank() {
		n--
	}
	ch := func(x int) rune {
		if x < 0 || x >= n || line[x].Rune() == 0 {
			return ' '
		}
		return line[x].Rune()
	}
	var quotes []int // unescaped quotes of the line
	for x := range n {
		if ch(x) == q && ch(x-1) != '\\' {
			quotes = append(quotes, x)
		}
	}
	x := c.pos.col
	o, cl := -1, -1
	for i, at := range quotes {
		if at == x {
			// On a quote: the quotes pair up from the start of the line.
			if i%2 == 0 && i+1 < len(quotes) {
				o, cl = at, quotes[i+1]
			} else if i%2 == 1 {
				o, cl = quotes[i-1], at
			}
			break
		}
		if at > x {
			if i > 0 {
				o, cl = quotes[i-1], at
			} else if len(quotes) > 1 {
				o, cl = quotes[0], quotes[1]
			}
			break
		}
	}
	if o < 0 {
		return
	}
	a, b := o+1, cl-1
	if around || c.grow {
		a, b = o, cl
	}
	if around {
		blank := func(x int) bool { return x >= 0 && x < n && unicode.IsSpace(ch(x)) }
		if blank(b + 1) {
			for blank(b + 1) {
				b++
			}
		} else {
			for blank(a - 1) {
				a--
			}
		}
	}
	if b < a {
		return // ""
	}
	c.anchor, c.pos, c.visual = selPos{c.pos.row, a}, selPos{c.pos.row, b}, 1
}
