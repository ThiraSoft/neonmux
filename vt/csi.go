package vt

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (t *Terminal) csiDispatch(final byte) {
	p := &t.p
	switch p.private {
	case '?':
		t.csiPrivate(final)
		return
	case '>':
		switch final {
		case 'c': // DA2
			t.replyString("\x1b[>1;10;0c")
		case 'q': // XTVERSION
			t.replyString("\x1bP>|neonmux(0.1)\x1b\\")
		}
		return
	case 0:
	default:
		return
	}

	switch p.inter {
	case ' ':
		if final == 'q' { // DECSCUSR
			t.cursorShap = p.paramZ(0, 0)
		}
		return
	case '!':
		if final == 'p' { // DECSTR, soft reset
			t.cur.attr, t.cur.fg, t.cur.bg = 0, 0, 0
			t.setMode(ModeInsert|ModeOrigin|ModeAppCursor, false)
			t.setMode(ModeAutoWrap|ModeCursorVisible, true)
			t.top, t.bot = 0, t.rows-1
		}
		return
	case 0:
	default:
		return
	}

	switch final {
	case '@':
		t.insertChars(p.param(0, 1))
	case 'A':
		t.cursorUp(p.param(0, 1))
	case 'B', 'e':
		t.cursorDown(p.param(0, 1))
	case 'C', 'a':
		t.cur.x = min(t.cur.x+p.param(0, 1), t.cols-1)
		t.cur.wrapNext = false
	case 'D':
		t.cur.x = max(t.cur.x-p.param(0, 1), 0)
		t.cur.wrapNext = false
	case 'E':
		t.cur.x = 0
		t.cursorDown(p.param(0, 1))
	case 'F':
		t.cur.x = 0
		t.cursorUp(p.param(0, 1))
	case 'G', '`':
		t.cur.x = min(p.param(0, 1)-1, t.cols-1)
		t.cur.wrapNext = false
	case 'H', 'f':
		t.moveTo(p.param(1, 1)-1, p.param(0, 1)-1)
	case 'I':
		t.tabForward(p.param(0, 1))
	case 'Z':
		t.tabBackward(p.param(0, 1))
	case 'J':
		t.eraseInDisplay(p.paramZ(0, 0))
	case 'K':
		t.eraseInLine(p.paramZ(0, 0))
	case 'L':
		t.insertLines(p.param(0, 1))
	case 'M':
		t.deleteLines(p.param(0, 1))
	case 'P':
		t.deleteChars(p.param(0, 1))
	case 'S':
		t.scrollUp(t.top, t.bot, p.param(0, 1))
	case 'T':
		if p.np <= 1 {
			t.scrollDown(t.top, t.bot, p.param(0, 1))
		}
	case 'X':
		t.eraseChars(p.param(0, 1))
	case 'b': // REP: repeat the last character
		if t.lastRune != 0 {
			t.repeat(t.lastRune, min(p.param(0, 1), t.cols*t.rows))
		}
	case 'c':
		if p.paramZ(0, 0) == 0 { // DA1: VT220 with ANSI colors
			t.replyString("\x1b[?62;22c")
		}
	case 'd':
		row := p.param(0, 1) - 1
		if t.modes&ModeOrigin != 0 {
			t.moveTo(t.cur.x, row)
		} else {
			t.cur.y = min(max(row, 0), t.rows-1)
			t.cur.wrapNext = false
		}
	case 'g':
		switch p.paramZ(0, 0) {
		case 0:
			t.tabs[t.cur.x] = false
		case 3:
			clear(t.tabs)
		}
	case 'h', 'l':
		on := final == 'h'
		for i := 0; i < p.np; i++ {
			switch p.params[i] {
			case 4:
				t.setMode(ModeInsert, on)
			case 20:
				t.setMode(ModeLNM, on)
			}
		}
	case 'm':
		t.sgr()
	case 'n':
		switch p.paramZ(0, 0) {
		case 5:
			t.replyString("\x1b[0n")
		case 6:
			y := t.cur.y
			if t.modes&ModeOrigin != 0 {
				y -= t.top
			}
			t.replyString(fmt.Sprintf("\x1b[%d;%dR", y+1, t.cur.x+1))
		}
	case 'r':
		top := p.param(0, 1) - 1
		bot := p.param(1, t.rows) - 1
		bot = min(bot, t.rows-1)
		if top < bot {
			t.top, t.bot = top, bot
			t.moveTo(0, 0)
		}
	case 's':
		t.saveCursor()
	case 'u':
		t.restoreCursor()
	case 't':
		switch p.paramZ(0, 0) {
		case 14:
			t.replyString(fmt.Sprintf("\x1b[4;%d;%dt", t.pxH, t.pxW))
		case 16:
			if t.cols > 0 && t.rows > 0 {
				t.replyString(fmt.Sprintf("\x1b[6;%d;%dt", t.pxH/t.rows, t.pxW/t.cols))
			}
		case 18:
			t.replyString(fmt.Sprintf("\x1b[8;%d;%dt", t.rows, t.cols))
		}
	}
}

// cursorUp moves up n lines (CUU, CPL) without crossing the top of the
// scrolling region, unless the cursor is already above it.
func (t *Terminal) cursorUp(n int) {
	lim := 0
	if t.cur.y >= t.top {
		lim = t.top
	}
	t.cur.y = max(t.cur.y-n, lim)
	t.cur.wrapNext = false
}

// cursorDown moves down n lines (CUD, CNL) without crossing the bottom of
// the scrolling region, unless the cursor is already below it.
func (t *Terminal) cursorDown(n int) {
	lim := t.rows - 1
	if t.cur.y <= t.bot {
		lim = t.bot
	}
	t.cur.y = min(t.cur.y+n, lim)
	t.cur.wrapNext = false
}

func (t *Terminal) csiPrivate(final byte) {
	p := &t.p
	switch final {
	case 'h', 'l':
		on := final == 'h'
		for i := 0; i < p.np; i++ {
			t.decset(int(p.params[i]), on)
		}
	case 'J':
		t.eraseInDisplay(p.paramZ(0, 0))
	case 'K':
		t.eraseInLine(p.paramZ(0, 0))
	case 'n':
		if p.paramZ(0, 0) == 6 {
			t.replyString(fmt.Sprintf("\x1b[?%d;%dR", t.cur.y+1, t.cur.x+1))
		}
	case 'p':
		if p.inter == '$' { // DECRQM
			mode := p.paramZ(0, 0)
			state := 0
			if m, ok := decModes[mode]; ok {
				state = 2
				if t.modes&m != 0 {
					state = 1
				}
			}
			t.replyString(fmt.Sprintf("\x1b[?%d;%d$y", mode, state))
		}
	}
}

var decModes = map[int]Mode{
	1: ModeAppCursor, 6: ModeOrigin, 7: ModeAutoWrap, 9: ModeMouseX10,
	12: ModeCursorBlink, 25: ModeCursorVisible, 1000: ModeMouseNormal,
	1002: ModeMouseButton, 1003: ModeMouseAny, 1004: ModeFocus, 1006: ModeMouseSGR,
	1007: ModeAltScroll, 1049: ModeAltScreen, 2004: ModeBracketedPaste, 2026: ModeSync,
}

func (t *Terminal) decset(mode int, on bool) {
	switch mode {
	case 1:
		t.setMode(ModeAppCursor, on)
	case 6:
		t.setMode(ModeOrigin, on)
		t.cur.origin = on
		t.moveTo(0, 0)
	case 7:
		t.setMode(ModeAutoWrap, on)
	case 9, 1000, 1002, 1003:
		// Only one tracking mode at a time.
		t.setMode(ModeMouseMask, false)
		if on {
			t.setMode(decModes[mode], true)
		}
	case 12:
		t.setMode(ModeCursorBlink, on)
	case 25:
		t.setMode(ModeCursorVisible, on)
		t.markDirty(t.cur.y)
	case 47, 1047:
		if !on && t.g == t.alt && mode == 1047 {
			clearAll(t.alt, 0)
		}
		t.switchScreen(on)
	case 1048:
		if on {
			t.saveCursor()
		} else {
			t.restoreCursor()
		}
	case 1049:
		if on {
			t.saved1049 = t.cur
			t.switchScreen(true)
			clearAll(t.alt, t.cur.bg)
		} else {
			t.switchScreen(false)
			t.cur = t.saved1049
			t.cur.x = min(t.cur.x, t.cols-1)
			t.cur.y = min(t.cur.y, t.rows-1)
		}
	case 1004, 1006, 1007, 2004:
		t.setMode(decModes[mode], on)
	case 2026:
		t.setMode(ModeSync, on)
		if on {
			t.syncAt = time.Now()
		}
	}
}

func clearAll(g *grid, bg Color) {
	for y := range g.lines {
		clearCells(g.lines[y], bg)
		g.wrapped[y] = false
	}
}

func (t *Terminal) sgr() {
	p := &t.p
	c := &t.cur
	if p.np == 0 {
		c.attr, c.fg, c.bg = 0, 0, 0
		return
	}
	for i := 0; i < p.np; i++ {
		v := p.params[i]
		if v < 0 {
			v = 0
		}
		switch {
		case v == 0:
			c.attr, c.fg, c.bg = 0, 0, 0
		case v == 1:
			c.attr |= AttrBold
		case v == 2:
			c.attr |= AttrFaint
		case v == 3:
			c.attr |= AttrItalic
		case v == 4:
			if p.isSub(i + 1) { // 4:0 to 4:5, underline styles
				i++
				if p.params[i] == 0 {
					c.attr &^= AttrUnderline
				} else {
					c.attr |= AttrUnderline
				}
			} else {
				c.attr |= AttrUnderline
			}
		case v == 5 || v == 6:
			c.attr |= AttrBlink
		case v == 7:
			c.attr |= AttrInverse
		case v == 8:
			c.attr |= AttrHidden
		case v == 9:
			c.attr |= AttrStrike
		case v == 21:
			c.attr |= AttrUnderline
		case v == 22:
			c.attr &^= AttrBold | AttrFaint
		case v == 23:
			c.attr &^= AttrItalic
		case v == 24:
			c.attr &^= AttrUnderline
		case v == 25:
			c.attr &^= AttrBlink
		case v == 27:
			c.attr &^= AttrInverse
		case v == 28:
			c.attr &^= AttrHidden
		case v == 29:
			c.attr &^= AttrStrike
		case v >= 30 && v <= 37:
			c.fg = Indexed(uint8(v - 30))
		case v == 38 || v == 48 || v == 58:
			col, used := t.extColor(i)
			i += used
			if used > 0 {
				switch v {
				case 38:
					c.fg = col
				case 48:
					c.bg = col
				}
			}
		case v == 39:
			c.fg = ColorDefault
		case v >= 40 && v <= 47:
			c.bg = Indexed(uint8(v - 40))
		case v == 49:
			c.bg = ColorDefault
		case v >= 90 && v <= 97:
			c.fg = Indexed(uint8(v - 90 + 8))
		case v >= 100 && v <= 107:
			c.bg = Indexed(uint8(v - 100 + 8))
		}
	}
}

// extColor reads the forms 38;5;n, 38;2;r;g;b, 38:5:n, 38:2::r:g:b and 38:2:r:g:b.
// It returns the color and the number of parameters consumed after i.
func (t *Terminal) extColor(i int) (Color, int) {
	p := &t.p
	if i+1 >= p.np {
		return 0, 0
	}
	at := func(j int) uint8 {
		if j >= p.np || p.params[j] < 0 {
			return 0
		}
		return uint8(min(p.params[j], 255))
	}
	sub := p.isSub(i + 1)
	switch p.params[i+1] {
	case 5:
		return Indexed(at(i + 2)), 2
	case 2:
		if sub {
			n := 0
			for j := i + 2; j < p.np && p.isSub(j); j++ {
				n++
			}
			if n >= 4 {
				return RGB(at(i+3), at(i+4), at(i+5)), 1 + n
			}
			return RGB(at(i+2), at(i+3), at(i+4)), 1 + n
		}
		return RGB(at(i+2), at(i+3), at(i+4)), 4
	}
	return 0, 1
}

// maxTitle caps the title set by OSC 0 and 2, in bytes.
const maxTitle = 256

func (t *Terminal) oscDispatch(data []byte) {
	code, rest, _ := bytes.Cut(data, []byte{';'})
	switch string(code) {
	case "0", "2":
		if len(rest) > maxTitle {
			// Cut at the start of a character, not in the middle of one.
			n := maxTitle
			for n > maxTitle-utf8.UTFMax && !utf8.RuneStart(rest[n]) {
				n--
			}
			rest = rest[:n]
		}
		t.title = string(rest)
	case "7":
		if u, err := url.Parse(string(rest)); err == nil && u.Path != "" {
			t.cwd = u.Path
		}
	case "9":
		// OSC 9;4;... is the ConEmu progress bar, not a notification.
		if !bytes.HasPrefix(rest, []byte("4;")) {
			t.pushEvent(Event{Kind: EvNotify, Body: string(rest)})
		}
	case "777":
		parts := strings.SplitN(string(rest), ";", 3)
		if len(parts) >= 2 && parts[0] == "notify" {
			ev := Event{Kind: EvNotify, Title: parts[1]}
			if len(parts) == 3 {
				ev.Body = parts[2]
			}
			t.pushEvent(ev)
		}
	case "10", "11", "12":
		if string(rest) == "?" {
			c := t.reportFG
			switch string(code) {
			case "11":
				c = t.reportBG
			case "12":
				c = t.reportCursor
			}
			t.replyString(fmt.Sprintf("\x1b]%s;%s\x1b\\", code, xcolor(c)))
		}
	case "4":
		// OSC 4;index;?: query for a palette color.
		parts := strings.Split(string(rest), ";")
		for k := 0; k+1 < len(parts); k += 2 {
			if parts[k+1] != "?" || t.palette == nil {
				continue
			}
			if idx, err := strconv.Atoi(parts[k]); err == nil && idx >= 0 && idx < 256 {
				r, g, b := t.palette(idx)
				t.replyString(fmt.Sprintf("\x1b]4;%d;%s\x1b\\", idx, xcolor([3]uint8{r, g, b})))
			}
		}
	case "52":
		_, payload, ok := bytes.Cut(rest, []byte{';'})
		if ok && string(payload) != "?" {
			if dec, err := base64.StdEncoding.DecodeString(string(payload)); err == nil {
				t.pushEvent(Event{Kind: EvClipboard, Body: string(dec)})
			}
		}
	case "7777":
		t.pushEvent(Event{Kind: EvFX, Body: string(rest)})
	case "7501":
		t.progStatus(rest)
	case "133":
		// Start of a new prompt: the program that was working has given control back.
		if bytes.HasPrefix(rest, []byte("A")) {
			t.progDrop(PSWorking, PSBlocked, PSIdle)
		}
	}
}

func xcolor(c [3]uint8) string {
	return fmt.Sprintf("rgb:%02x%02x/%02x%02x/%02x%02x", c[0], c[0], c[1], c[1], c[2], c[2])
}
