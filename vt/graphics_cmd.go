package vt

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strconv"
)

// gcmd represents a parsed kitty graphics protocol command.
type gcmd struct {
	a, t, f, o, d byte
	i, I, p       uint32
	s, v, S, O    int
	x, y, w, h    int
	X, Y, c, r    int
	C, U, m, q    int
	z             int32
	payload       []byte
	hasNonMQKey   bool
	hasM          bool
}

func parseByteNum(v []byte) (byte, error) {
	if len(v) == 0 {
		return 0, errors.New("empty value")
	}
	n, err := strconv.ParseUint(string(v), 10, 8)
	if err != nil {
		return 0, err
	}
	return byte(n), nil
}

func parseUint32(v []byte) (uint32, error) {
	if len(v) == 0 {
		return 0, errors.New("empty value")
	}
	n, err := strconv.ParseUint(string(v), 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(n), nil
}

func parseInt(v []byte) (int, error) {
	if len(v) == 0 {
		return 0, errors.New("empty value")
	}
	n, err := strconv.Atoi(string(v))
	if err != nil {
		return 0, err
	}
	return n, nil
}

func parseInt32(v []byte) (int32, error) {
	if len(v) == 0 {
		return 0, errors.New("empty value")
	}
	n, err := strconv.ParseInt(string(v), 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

// parseGCmd parses the content of a kitty graphics sequence after 'G'.
// Keys are comma-separated k=v pairs followed by an optional ';' and base64 payload.
// Protocol defaults are a='t', t='d', f=32.
func parseGCmd(b []byte) (gcmd, error) {
	cmd := gcmd{
		a: 't',
		t: 'd',
		f: 32,
	}

	var ctrl []byte
	var payloadPart []byte
	if idx := bytes.IndexByte(b, ';'); idx >= 0 {
		ctrl = b[:idx]
		payloadPart = b[idx+1:]
	} else {
		ctrl = b
	}

	var firstErr error
	for len(ctrl) > 0 {
		var pair []byte
		if idx := bytes.IndexByte(ctrl, ','); idx >= 0 {
			pair = ctrl[:idx]
			ctrl = ctrl[idx+1:]
		} else {
			pair = ctrl
			ctrl = nil
		}
		if len(pair) == 0 {
			continue
		}
		eq := bytes.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		k := pair[:eq]
		v := pair[eq+1:]
		if len(k) != 1 {
			// Unknown key: ignore
			cmd.hasNonMQKey = true
			continue
		}
		if k[0] != 'm' && k[0] != 'q' {
			cmd.hasNonMQKey = true
		}
		if k[0] == 'm' {
			cmd.hasM = true
		}

		var err error
		switch k[0] {
		case 'a':
			if len(v) == 1 {
				cmd.a = v[0]
			}
		case 't':
			if len(v) == 1 {
				cmd.t = v[0]
			}
		case 'f':
			cmd.f, err = parseByteNum(v)
		case 'o':
			if len(v) == 1 {
				cmd.o = v[0]
			}
		case 'd':
			if len(v) == 1 {
				cmd.d = v[0]
			}
		case 'i':
			cmd.i, err = parseUint32(v)
		case 'I':
			cmd.I, err = parseUint32(v)
		case 'p':
			cmd.p, err = parseUint32(v)
		case 's':
			cmd.s, err = parseInt(v)
		case 'v':
			cmd.v, err = parseInt(v)
		case 'S':
			cmd.S, err = parseInt(v)
		case 'O':
			cmd.O, err = parseInt(v)
		case 'x':
			cmd.x, err = parseInt(v)
		case 'y':
			cmd.y, err = parseInt(v)
		case 'w':
			cmd.w, err = parseInt(v)
		case 'h':
			cmd.h, err = parseInt(v)
		case 'X':
			cmd.X, err = parseInt(v)
		case 'Y':
			cmd.Y, err = parseInt(v)
		case 'c':
			cmd.c, err = parseInt(v)
		case 'r':
			cmd.r, err = parseInt(v)
		case 'C':
			cmd.C, err = parseInt(v)
		case 'U':
			cmd.U, err = parseInt(v)
		case 'm':
			cmd.m, err = parseInt(v)
		case 'q':
			cmd.q, err = parseInt(v)
		case 'z':
			cmd.z, err = parseInt32(v)
		default:
			// Unknown key: ignore
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if len(payloadPart) > 0 {
		clean := payloadPart
		if bytes.ContainsAny(clean, " \t\r\n") {
			buf := make([]byte, 0, len(clean))
			for _, c := range clean {
				if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
					buf = append(buf, c)
				}
			}
			clean = buf
		}
		if len(clean) > 0 {
			dst := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
			n, err := base64.StdEncoding.Decode(dst, clean)
			if err != nil {
				dst = make([]byte, base64.RawStdEncoding.DecodedLen(len(clean)))
				n, err = base64.RawStdEncoding.Decode(dst, clean)
			}
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
			} else {
				cmd.payload = dst[:n]
			}
		}
	}

	if firstErr != nil {
		return cmd, firstErr
	}

	return cmd, nil
}

// apcDispatch processes an APC sequence. It only acts if b starts with 'G'.
func (t *Terminal) apcDispatch(b []byte) {
	if len(b) == 0 || b[0] != 'G' {
		return
	}
	cmd, err := parseGCmd(b[1:])
	if err != nil {
		if cmd.i == 0 && cmd.I == 0 && t.gfx != nil && t.gfx.active != nil {
			cmd.i = t.gfx.active.cmd.i
			cmd.I = t.gfx.active.cmd.I
			cmd.q = t.gfx.active.cmd.q
			t.gfx.active = nil
		}
		if cmd.i != 0 || cmd.I != 0 {
			t.gReply(cmd, "EINVAL")
		}
		return
	}
	if t.gfx != nil && t.gfx.active != nil {
		if cmd.hasNonMQKey || !cmd.hasM || cmd.a == 'q' {
			t.gfx.active = nil
		}
	}
	if cmd.i != 0 && cmd.I != 0 {
		t.gReply(cmd, "EINVAL")
		return
	}
	if t.gfx == nil {
		t.gfx = newGraphics()
	}
	switch cmd.a {
	case 't', 'T':
		t.gfx.transmit(t, cmd)
	case 'q':
		t.gfx.query(t, cmd)
	case 'f', 'a', 'c':
		t.gReply(cmd, "EINVAL")
	default:
		t.gReply(cmd, "EINVAL")
	}
}
