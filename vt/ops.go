package vt

import (
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"golang.org/x/text/unicode/norm"
)

var widthCond = func() *runewidth.Condition {
	c := runewidth.NewCondition()
	c.EastAsianWidth = false
	return c
}()

// RuneWidth returns the width of a character in cells (0, 1 or 2).
func RuneWidth(r rune) int {
	if r < 0x300 {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return 0
		}
		return 1
	}
	return widthCond.RuneWidth(r)
}

// DEC graphics set (ESC ( 0), used by ncurses for borders.
var decGraphics = [...]rune{
	'_': ' ', '`': '◆', 'a': '▒', 'b': '␉', 'c': '␌', 'd': '␍', 'e': '␊', 'f': '°',
	'g': '±', 'h': '␤', 'i': '␋', 'j': '┘', 'k': '┐', 'l': '┌', 'm': '└', 'n': '┼',
	'o': '⎺', 'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽', 't': '├', 'u': '┤', 'v': '┴',
	'w': '┬', 'x': '│', 'y': '≤', 'z': '≥', '{': 'π', '|': '≠', '}': '£', '~': '·',
}

func (t *Terminal) graphicsActive() bool { return t.cur.charsets[t.cur.gl] }

// printASCII writes a run of printable ASCII (the most common path).
func (t *Terminal) printASCII(s []byte) {
	if t.graphicsActive() || t.modes&ModeInsert != 0 {
		for _, b := range s {
			t.print(rune(b))
		}
		return
	}
	fg, bg, at := t.cur.fg, t.cur.bg, t.cur.attr
	for len(s) > 0 {
		if t.cur.wrapNext {
			t.wrap()
		}
		line := t.g.lines[t.cur.y]
		x := t.cur.x
		n := min(len(s), t.cols-x)
		t.fixWide(line, x)
		t.fixWide(line, x+n-1)
		for i := 0; i < n; i++ {
			line[x+i] = MakeCell(rune(s[i]), at, fg, bg)
		}
		t.lastRune = rune(s[n-1])
		t.markDirty(t.cur.y)
		s = s[n:]
		x += n
		if x >= t.cols {
			t.cur.x = t.cols - 1
			if t.modes&ModeAutoWrap != 0 {
				t.cur.wrapNext = true
			} else {
				// Without autowrap, the rest overwrites the last column.
				if len(s) > 0 {
					line[t.cols-1] = MakeCell(rune(s[len(s)-1]), at, fg, bg)
					s = nil
				}
			}
		} else {
			t.cur.x = x
		}
	}
}

func (t *Terminal) print(r rune) {
	if r < 0x80 && t.graphicsActive() && r >= '_' && r <= '~' {
		r = decGraphics[r]
	}
	w := RuneWidth(r)
	if w == 0 {
		t.combine(r)
		return
	}
	if t.cur.wrapNext {
		t.wrap()
	}
	if w == 2 && t.cur.x == t.cols-1 {
		if t.modes&ModeAutoWrap == 0 {
			return
		}
		line := t.g.lines[t.cur.y]
		t.fixWide(line, t.cur.x)
		line[t.cur.x] = blankCell(t.cur.bg)
		t.wrap()
	}
	t.put(r, w, 1)
}

// put writes r, of width w, k times starting at the cursor. The line must
// be able to hold them: x + k*w ≤ cols. In insert mode, the rest of the
// line is first shifted by the same amount.
func (t *Terminal) put(r rune, w, k int) {
	line := t.g.lines[t.cur.y]
	x := t.cur.x
	end := x + k*w
	if t.modes&ModeInsert != 0 {
		t.shiftRight(line, x, end-x)
	} else {
		t.fixWide(line, x)
		if end-1 > x {
			t.fixWide(line, end-1)
		}
	}
	if w == 2 {
		c := MakeCell(r, t.cur.attr|AttrWide, t.cur.fg, t.cur.bg)
		cont := MakeCell(0, AttrWideCont, t.cur.fg, t.cur.bg)
		for i := x; i < end; i += 2 {
			line[i], line[i+1] = c, cont
		}
	} else {
		fillCells(line[x:end], MakeCell(r, t.cur.attr, t.cur.fg, t.cur.bg))
	}
	t.lastRune = r
	t.markDirty(t.cur.y)
	if end >= t.cols {
		t.cur.x = t.cols - 1
		t.cur.wrapNext = t.modes&ModeAutoWrap != 0
	} else {
		t.cur.x = end
	}
}

// repeat repeats r n times (REP) with the same result as n calls to print,
// but fills whatever fits on the line in one go: 64 KB of REP used to take
// nearly a second under the lock.
func (t *Terminal) repeat(r rune, n int) {
	if r < 0x80 && t.graphicsActive() && r >= '_' && r <= '~' {
		r = decGraphics[r]
	}
	w := RuneWidth(r)
	for n > 0 {
		if w == 0 || t.modes&ModeAutoWrap == 0 && !t.cur.wrapNext && t.cur.x == t.cols-1 {
			// Combining mark, or last column without autowrap: the rest
			// would rewrite the same cell (or nothing), so once is enough.
			t.print(r)
			return
		}
		k := 0
		if !t.cur.wrapNext {
			k = min(n, (t.cols-t.cur.x)/w)
		}
		if k == 0 {
			t.print(r) // line wrap, or wide character at the end of the line
			n--
			continue
		}
		t.put(r, w, k)
		n -= k
	}
}

// combine composes a combining mark with the previous character when the
// NFC form fits in a single rune: "e" followed by U+0301 gives U+00E9, as in
// NFD file names on macOS. A cell holds only one rune: without a composed
// form, the mark is ignored.
func (t *Terminal) combine(r rune) {
	if r < 0x300 {
		return // control characters: nothing to compose
	}
	// The previous character is left of the cursor, or under it when a
	// wrap is pending.
	x := t.cur.x
	if !t.cur.wrapNext {
		x--
	}
	line := t.g.lines[t.cur.y]
	if x > 0 && line[x].Attr()&AttrWideCont != 0 {
		x--
	}
	if x < 0 {
		return
	}
	c := line[x]
	base := c.Rune()
	if base == 0 || c.Attr()&AttrWideCont != 0 {
		return
	}
	var in, out [2 * utf8.UTFMax]byte
	nfc := norm.NFC.Append(out[:0], utf8.AppendRune(utf8.AppendRune(in[:0], base), r)...)
	cr, size := utf8.DecodeRune(nfc)
	if size != len(nfc) || cr == base || RuneWidth(cr) != RuneWidth(base) {
		return
	}
	line[x] = MakeCell(cr, c.Attr(), c.FG, c.BG)
	t.lastRune = cr
	t.markDirty(t.cur.y)
}

// fixWide erases the other half of a wide character about to be overwritten.
func (t *Terminal) fixWide(line []Cell, x int) {
	if x < 0 || x >= len(line) {
		return
	}
	a := line[x].Attr()
	if a&AttrWideCont != 0 && x > 0 {
		line[x-1] = blankCell(line[x-1].BG)
	}
	if a&AttrWide != 0 && x+1 < len(line) {
		line[x+1] = blankCell(line[x+1].BG)
	}
}

// shiftRight shifts line[x:] right by n cells (ICH, insert mode). A wide
// character split at x is erased entirely before the shift. One that loses
// its right half at the end of the line is erased after it.
func (t *Terminal) shiftRight(line []Cell, x, n int) {
	if line[x].Attr()&AttrWideCont != 0 {
		t.fixWide(line, x)
		line[x] = blankCell(line[x].BG)
	}
	copy(line[x+n:], line[x:])
	if last := len(line) - 1; line[last].Attr()&AttrWide != 0 {
		line[last] = blankCell(line[last].BG)
	}
}

func (t *Terminal) wrap() {
	t.cur.wrapNext = false
	t.g.wrapped[t.cur.y] = true
	t.cur.x = 0
	t.index()
}

func (t *Terminal) execute(b byte) {
	switch b {
	case 0x07:
		t.pushEvent(Event{Kind: EvBell})
	case 0x08:
		if t.cur.x > 0 {
			t.cur.x--
		}
		t.cur.wrapNext = false
	case 0x09:
		t.tabForward(1)
	case 0x0a, 0x0b, 0x0c:
		t.index()
		if t.modes&ModeLNM != 0 {
			t.cur.x = 0
		}
	case 0x0d:
		t.cur.x = 0
		t.cur.wrapNext = false
	case 0x0e:
		t.cur.gl = 1
	case 0x0f:
		t.cur.gl = 0
	}
}

func (t *Terminal) escDispatch(inter, final byte) {
	switch inter {
	case 0:
		switch final {
		case '7':
			t.saveCursor()
		case '8':
			t.restoreCursor()
		case 'D':
			t.index()
		case 'E':
			t.cur.x = 0
			t.index()
		case 'M':
			t.reverseIndex()
		case 'H':
			t.tabs[t.cur.x] = true
		case 'c':
			t.sb.clear()
			t.reset()
			t.progress = nil // RIS also clears the statuses (OSC 7501)
		case '=':
			t.setMode(ModeAppKeypad, true)
		case '>':
			t.setMode(ModeAppKeypad, false)
		}
	case '(', ')':
		idx := 0
		if inter == ')' {
			idx = 1
		}
		t.cur.charsets[idx] = final == '0'
	case '#':
		if final == '8' { // DECALN: fills the screen with E
			e := MakeCell('E', 0, 0, 0)
			for _, l := range t.g.lines {
				fillCells(l, e)
			}
			t.dirtyAll = true
		}
	}
}

func (t *Terminal) saveCursor() {
	i := 0
	if t.g == t.alt {
		i = 1
	}
	t.saved[i] = t.cur
}

func (t *Terminal) restoreCursor() {
	i := 0
	if t.g == t.alt {
		i = 1
	}
	t.cur = t.saved[i]
	t.cur.x = min(t.cur.x, t.cols-1)
	t.cur.y = min(t.cur.y, t.rows-1)
	t.setMode(ModeOrigin, t.cur.origin)
}

// index moves down one line, scrolling the region if needed. As in xterm,
// a pending wrap is canceled (LF, VT, FF, IND, NEL): the next character is
// written on the new line.
func (t *Terminal) index() {
	t.cur.wrapNext = false
	if t.cur.y == t.bot {
		t.scrollUp(t.top, t.bot, 1)
	} else if t.cur.y < t.rows-1 {
		t.cur.y++
	}
}

func (t *Terminal) reverseIndex() {
	t.cur.wrapNext = false
	if t.cur.y == t.top {
		t.scrollDown(t.top, t.bot, 1)
	} else if t.cur.y > 0 {
		t.cur.y--
	}
}

// scrollUp moves lines [top, bot] up by n. If the region starts at the top
// of the main screen, the lines scrolled out go to the history. Line slices
// are swapped around: no cells are copied.
func (t *Terminal) scrollUp(top, bot, n int) {
	if n <= 0 {
		return
	}
	n = min(n, bot-top+1)
	g := t.g
	if top == 0 && g == t.main {
		for i := 0; i < n; i++ {
			t.sb.push(g.lines[i], g.wrapped[i])
		}
	}
	rotateUp(g, top, bot, n)
	for y := bot - n + 1; y <= bot; y++ {
		clearCells(g.lines[y], t.cur.bg)
		g.wrapped[y] = false
	}
	t.markRange(top, bot)
}

func (t *Terminal) scrollDown(top, bot, n int) {
	if n <= 0 {
		return
	}
	n = min(n, bot-top+1)
	g := t.g
	rotateDown(g, top, bot, n)
	for y := top; y < top+n; y++ {
		clearCells(g.lines[y], t.cur.bg)
		g.wrapped[y] = false
	}
	t.markRange(top, bot)
}

// rotSpare is the number of lines a rotation sets aside on the stack.
// Beyond that (rare: SU or IL with a large count), three reversals.
const rotSpare = 16

// rotateUp rotates lines [top, bot] up by n: the first n move to the
// bottom. A one-line scroll, the common case, thus only moves a run of
// slice headers with copy.
func rotateUp(g *grid, top, bot, n int) {
	if n > rotSpare {
		reverse(g, top, top+n-1)
		reverse(g, top+n, bot)
		reverse(g, top, bot)
		return
	}
	var lines [rotSpare][]Cell
	var wrapped [rotSpare]bool
	copy(lines[:n], g.lines[top:top+n])
	copy(wrapped[:n], g.wrapped[top:top+n])
	copy(g.lines[top:], g.lines[top+n:bot+1])
	copy(g.wrapped[top:], g.wrapped[top+n:bot+1])
	copy(g.lines[bot-n+1:], lines[:n])
	copy(g.wrapped[bot-n+1:], wrapped[:n])
}

func rotateDown(g *grid, top, bot, n int) {
	if n > rotSpare {
		reverse(g, top, bot-n)
		reverse(g, bot-n+1, bot)
		reverse(g, top, bot)
		return
	}
	var lines [rotSpare][]Cell
	var wrapped [rotSpare]bool
	copy(lines[:n], g.lines[bot-n+1:bot+1])
	copy(wrapped[:n], g.wrapped[bot-n+1:bot+1])
	copy(g.lines[top+n:], g.lines[top:bot-n+1])
	copy(g.wrapped[top+n:], g.wrapped[top:bot-n+1])
	copy(g.lines[top:], lines[:n])
	copy(g.wrapped[top:], wrapped[:n])
}

func reverse(g *grid, a, b int) {
	for a < b {
		g.lines[a], g.lines[b] = g.lines[b], g.lines[a]
		g.wrapped[a], g.wrapped[b] = g.wrapped[b], g.wrapped[a]
		a++
		b--
	}
}

func (t *Terminal) tabForward(n int) {
	x := t.cur.x
	for ; n > 0; n-- {
		x++
		for x < t.cols-1 && !t.tabs[x] {
			x++
		}
	}
	t.cur.x = min(x, t.cols-1)
	t.cur.wrapNext = false
}

func (t *Terminal) tabBackward(n int) {
	x := t.cur.x
	for ; n > 0 && x > 0; n-- {
		x--
		for x > 0 && !t.tabs[x] {
			x--
		}
	}
	t.cur.x = x
	t.cur.wrapNext = false
}

func (t *Terminal) moveTo(x, y int) {
	minY, maxY := 0, t.rows-1
	if t.modes&ModeOrigin != 0 {
		y += t.top
		minY, maxY = t.top, t.bot
	}
	t.cur.x = min(max(x, 0), t.cols-1)
	t.cur.y = min(max(y, minY), maxY)
	t.cur.wrapNext = false
}

func (t *Terminal) eraseInLine(mode int) {
	line := t.g.lines[t.cur.y]
	switch mode {
	case 0:
		t.fixWide(line, t.cur.x)
		clearCells(line[t.cur.x:], t.cur.bg)
		t.g.wrapped[t.cur.y] = false
	case 1:
		t.fixWide(line, t.cur.x)
		clearCells(line[:t.cur.x+1], t.cur.bg)
	case 2:
		clearCells(line, t.cur.bg)
		t.g.wrapped[t.cur.y] = false
	}
	t.cur.wrapNext = false
	t.markDirty(t.cur.y)
}

func (t *Terminal) eraseInDisplay(mode int) {
	switch mode {
	case 0:
		t.eraseInLine(0)
		for y := t.cur.y + 1; y < t.rows; y++ {
			clearCells(t.g.lines[y], t.cur.bg)
			t.g.wrapped[y] = false
		}
		t.markRange(t.cur.y, t.rows-1)
	case 1:
		for y := 0; y < t.cur.y; y++ {
			clearCells(t.g.lines[y], t.cur.bg)
		}
		t.eraseInLine(1)
		t.markRange(0, t.cur.y)
	case 2:
		for y := range t.g.lines {
			clearCells(t.g.lines[y], t.cur.bg)
			t.g.wrapped[y] = false
		}
		t.dirtyAll = true
	case 3:
		t.sb.clear()
		t.dirtyAll = true
	}
}

func (t *Terminal) insertChars(n int) {
	line := t.g.lines[t.cur.y]
	x := t.cur.x
	n = min(n, t.cols-x)
	t.shiftRight(line, x, n)
	clearCells(line[x:x+n], t.cur.bg)
	t.cur.wrapNext = false
	t.markDirty(t.cur.y)
}

func (t *Terminal) deleteChars(n int) {
	line := t.g.lines[t.cur.y]
	x := t.cur.x
	n = min(n, t.cols-x)
	t.fixWide(line, x)
	t.fixWide(line, x+n-1)
	copy(line[x:], line[x+n:])
	clearCells(line[t.cols-n:], t.cur.bg)
	t.cur.wrapNext = false
	t.markDirty(t.cur.y)
}

func (t *Terminal) eraseChars(n int) {
	line := t.g.lines[t.cur.y]
	x := t.cur.x
	n = min(n, t.cols-x)
	t.fixWide(line, x)
	t.fixWide(line, x+n-1)
	clearCells(line[x:x+n], t.cur.bg)
	t.cur.wrapNext = false
	t.markDirty(t.cur.y)
}

func (t *Terminal) insertLines(n int) {
	if t.cur.y < t.top || t.cur.y > t.bot {
		return
	}
	t.scrollDown(t.cur.y, t.bot, n)
	t.cur.x = 0
	t.cur.wrapNext = false
}

func (t *Terminal) deleteLines(n int) {
	if t.cur.y < t.top || t.cur.y > t.bot {
		return
	}
	// No history here: the region does not necessarily start at the top.
	n = min(n, t.bot-t.cur.y+1)
	g := t.g
	rotateUp(g, t.cur.y, t.bot, n)
	for y := t.bot - n + 1; y <= t.bot; y++ {
		clearCells(g.lines[y], t.cur.bg)
		g.wrapped[y] = false
	}
	t.markRange(t.cur.y, t.bot)
	t.cur.x = 0
	t.cur.wrapNext = false
}

func (t *Terminal) switchScreen(alt bool) {
	if alt == (t.g == t.alt) {
		return
	}
	if alt {
		t.g = t.alt
	} else {
		t.g = t.main
	}
	t.setMode(ModeAltScreen, alt)
	t.dirtyAll = true
}
