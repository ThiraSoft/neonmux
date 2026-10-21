package vt

import "unicode/utf8"

// State machine inspired by Paul Williams' DEC parser
// (https://vt100.net/emu/dec_ansi_parser), simplified for UTF-8.

const (
	stGround = iota
	stEscape
	stEscInter
	stCSI
	stCSIIgnore
	stOSC
	stOSCEsc
	stIgnoreST // DCS, SOS, PM, APC: consumed up to ST
	stIgnoreSTEsc
)

const (
	maxParams = 32
	maxOSC    = 1 << 20 // OSC 52 can carry a sizable clipboard
	// maxOSCKeep is the OSC buffer capacity kept from one sequence to the
	// next. Beyond that, the buffer is released: a 1 MB OSC 52 must not stay
	// allocated until the end of the session.
	maxOSCKeep = 4 << 10
)

type parser struct {
	state uint8

	params  [maxParams]int32 // -1 = missing parameter
	sub     uint32           // bit i: parameter i is a sub-parameter (introduced by ':')
	np      int
	cur     int32
	hasCur  bool
	inter   byte // first intermediate byte (' ', '$', '"', '!'...)
	private byte // '?', '>', '=' or '<'

	osc []byte

	uacc  rune
	uneed int
}

func (p *parser) clearCSI() {
	p.np, p.sub, p.cur, p.hasCur, p.inter, p.private = 0, 0, 0, false, 0, 0
}

func (p *parser) pushParam() {
	if p.np < maxParams {
		if p.hasCur {
			p.params[p.np] = p.cur
		} else {
			p.params[p.np] = -1
		}
		p.np++
	}
	p.cur, p.hasCur = 0, false
}

// param returns parameter i, or def if it is missing or zero.
func (p *parser) param(i, def int) int {
	if i >= p.np || p.params[i] <= 0 {
		return def
	}
	return int(p.params[i])
}

// paramZ returns parameter i, or def only if it is missing.
func (p *parser) paramZ(i, def int) int {
	if i >= p.np || p.params[i] < 0 {
		return def
	}
	return int(p.params[i])
}

func (p *parser) isSub(i int) bool { return i < 32 && p.sub&(1<<uint(i)) != 0 }

// endOSC empties the OSC buffer once the sequence is handled or dropped.
func (p *parser) endOSC() {
	if cap(p.osc) > maxOSCKeep {
		p.osc = nil
	} else {
		p.osc = p.osc[:0]
	}
}

// sanitize brings back within bounds the fields of a parser read from a
// snapshot. An unknown state, for example, would swallow all the output.
func (p *parser) sanitize() {
	if p.state > stIgnoreSTEsc {
		p.state = stGround
		p.clearCSI()
	}
	p.np = min(max(p.np, 0), maxParams)
	for i := range p.params[:p.np] {
		p.params[i] = min(max(p.params[i], -1), 1<<20)
	}
	p.cur = min(max(p.cur, 0), 1<<20)
	if len(p.osc) > maxOSC {
		p.osc = p.osc[:maxOSC]
	}
	// A UTF-8 sequence has at most 3 continuation bytes; uacc keeps the bits
	// already read, 21 at most once complete.
	if p.uneed < 1 || p.uneed > 3 {
		p.uacc, p.uneed = 0, 0
	} else {
		p.uacc &= 1<<(21-6*p.uneed) - 1
	}
}

func (t *Terminal) feed(data []byte) {
	p := &t.p
	for i := 0; i < len(data); i++ {
		b := data[i]
		if p.state != stGround {
			t.step(b)
			continue
		}
		if p.uneed > 0 {
			if b&0xc0 == 0x80 {
				p.uacc = p.uacc<<6 | rune(b&0x3f)
				p.uneed--
				if p.uneed == 0 {
					if !utf8.ValidRune(p.uacc) {
						p.uacc = utf8.RuneError // surrogate half or beyond U+10FFFF
					}
					t.print(p.uacc)
				}
				continue
			}
			p.uneed = 0
			t.print(0xfffd)
		}
		switch {
		case b >= 0x20 && b < 0x7f:
			// Fast path: a whole run of printable ASCII at once.
			j := i + 1
			for j < len(data) && data[j] >= 0x20 && data[j] < 0x7f {
				j++
			}
			t.printASCII(data[i:j])
			i = j - 1
		case b < 0x20 || b == 0x7f:
			t.control(b)
		case b&0xe0 == 0xc0:
			p.uacc, p.uneed = rune(b&0x1f), 1
		case b&0xf0 == 0xe0:
			p.uacc, p.uneed = rune(b&0x0f), 2
		case b&0xf8 == 0xf0:
			p.uacc, p.uneed = rune(b&0x07), 3
		default:
			t.print(0xfffd)
		}
	}
}

// control handles a C0 control character in any state.
func (t *Terminal) control(b byte) {
	p := &t.p
	switch b {
	case 0x1b:
		p.state = stEscape
		p.inter = 0
	case 0x18, 0x1a: // CAN, SUB: abort the sequence
		p.state = stGround
	case 0x7f:
	default:
		t.execute(b)
	}
}

func (t *Terminal) step(b byte) {
	p := &t.p
	switch p.state {
	case stEscape:
		switch {
		case b < 0x20:
			t.control(b)
		case b <= 0x2f:
			p.inter = b
			p.state = stEscInter
		case b == '[':
			p.clearCSI()
			p.state = stCSI
		case b == ']':
			p.osc = p.osc[:0]
			p.state = stOSC
		case b == 'P', b == 'X', b == '^', b == '_':
			p.state = stIgnoreST
		case b < 0x7f:
			p.state = stGround
			t.escDispatch(0, b)
		}
	case stEscInter:
		switch {
		case b < 0x20:
			t.control(b)
		case b <= 0x2f:
		case b < 0x7f:
			p.state = stGround
			t.escDispatch(p.inter, b)
		}
	case stCSI:
		switch {
		case b < 0x20:
			t.control(b)
		case b >= '0' && b <= '9':
			if p.cur < 65535 {
				p.cur = p.cur*10 + int32(b-'0')
			}
			p.hasCur = true
		case b == ';':
			p.pushParam()
		case b == ':':
			p.pushParam()
			if p.np < 32 {
				p.sub |= 1 << uint(p.np)
			}
		case b >= 0x3c && b <= 0x3f:
			if p.np == 0 && !p.hasCur && p.private == 0 && p.inter == 0 {
				p.private = b
			} else {
				p.state = stCSIIgnore
			}
		case b >= 0x20 && b <= 0x2f:
			p.inter = b
		case b >= 0x40 && b < 0x7f:
			if p.hasCur || p.np > 0 {
				p.pushParam()
			}
			p.state = stGround
			t.csiDispatch(b)
		}
	case stCSIIgnore:
		switch {
		case b < 0x20:
			t.control(b)
		case b >= 0x40 && b < 0x7f:
			p.state = stGround
		}
	case stOSC:
		switch {
		case b == 0x07:
			p.state = stGround
			t.oscDispatch(p.osc)
			p.endOSC()
		case b == 0x1b:
			p.state = stOSCEsc
		case b == 0x18 || b == 0x1a:
			p.state = stGround
			p.endOSC()
		case b < 0x20:
		default:
			if len(p.osc) < maxOSC {
				p.osc = append(p.osc, b)
			}
		}
	case stOSCEsc:
		p.state = stGround
		t.oscDispatch(p.osc)
		p.endOSC()
		if b != '\\' {
			p.state = stEscape
			t.step(b)
		}
	case stIgnoreST:
		switch b {
		case 0x1b:
			p.state = stIgnoreSTEsc
		case 0x18, 0x1a:
			p.state = stGround
		}
	case stIgnoreSTEsc:
		if b == '\\' {
			p.state = stGround
		} else {
			p.state = stEscape
			t.step(b)
		}
	}
}
