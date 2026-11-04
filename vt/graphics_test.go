package vt

import (
	"strings"
	"testing"
)

func TestParseGCmd(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, cmd gcmd)
	}{
		{
			name:  "standard transmission and display",
			input: "a=T,f=100,i=7;aGVsbG8=",
			check: func(t *testing.T, cmd gcmd) {
				if cmd.a != 'T' {
					t.Errorf("a: got %c, want 'T'", cmd.a)
				}
				if cmd.f != 100 {
					t.Errorf("f: got %d, want 100", cmd.f)
				}
				if cmd.i != 7 {
					t.Errorf("i: got %d, want 7", cmd.i)
				}
				if cmd.t != 'd' {
					t.Errorf("t (default): got %c, want 'd'", cmd.t)
				}
				if string(cmd.payload) != "hello" {
					t.Errorf("payload: got %q, want \"hello\"", string(cmd.payload))
				}
			},
		},
		{
			name:  "unknown key ignored",
			input: "foo=bar,a=p,Xunknown=99,i=42;aGVsbG8=",
			check: func(t *testing.T, cmd gcmd) {
				if cmd.a != 'p' {
					t.Errorf("a: got %c, want 'p'", cmd.a)
				}
				if cmd.i != 42 {
					t.Errorf("i: got %d, want 42", cmd.i)
				}
				if string(cmd.payload) != "hello" {
					t.Errorf("payload: got %q, want \"hello\"", string(cmd.payload))
				}
			},
		},
		{
			name:    "non-numeric value for i returns error",
			input:   "i=notanumber;aGVsbG8=",
			wantErr: true,
		},
		{
			name:    "non-numeric value for f returns error",
			input:   "f=invalid;aGVsbG8=",
			wantErr: true,
		},
		{
			name:    "non-numeric value for s returns error",
			input:   "s=bad;aGVsbG8=",
			wantErr: true,
		},
		{
			name:    "non-numeric value for z returns error",
			input:   "z=xyz;aGVsbG8=",
			wantErr: true,
		},
		{
			name:  "defaults for empty control",
			input: ";aGVsbG8=",
			check: func(t *testing.T, cmd gcmd) {
				if cmd.a != 't' || cmd.t != 'd' || cmd.f != 32 {
					t.Errorf("defaults mismatch: a=%c, t=%c, f=%d", cmd.a, cmd.t, cmd.f)
				}
				if string(cmd.payload) != "hello" {
					t.Errorf("payload: got %q, want \"hello\"", string(cmd.payload))
				}
			},
		},
		{
			name:  "raw base64 payload without padding",
			input: "a=t,i=1;aGVsbG8",
			check: func(t *testing.T, cmd gcmd) {
				if cmd.i != 1 {
					t.Errorf("i: got %d, want 1", cmd.i)
				}
				if string(cmd.payload) != "hello" {
					t.Errorf("payload: got %q, want \"hello\"", string(cmd.payload))
				}
			},
		},
		{
			name:  "negative z-index",
			input: "a=p,z=-3",
			check: func(t *testing.T, cmd gcmd) {
				if cmd.z != -3 {
					t.Errorf("z: got %d, want -3", cmd.z)
				}
			},
		},
		{
			name:  "all fields parsed",
			input: "a=p,t=f,f=24,o=z,d=a,i=10,I=20,p=30,s=100,v=200,S=300,O=400,x=1,y=2,w=50,h=60,X=3,Y=4,c=5,r=6,C=1,U=1,m=1,q=2,z=5",
			check: func(t *testing.T, cmd gcmd) {
				if cmd.a != 'p' || cmd.t != 'f' || cmd.f != 24 || cmd.o != 'z' || cmd.d != 'a' {
					t.Errorf("byte fields mismatch: a=%c t=%c f=%d o=%c d=%c", cmd.a, cmd.t, cmd.f, cmd.o, cmd.d)
				}
				if cmd.i != 10 || cmd.I != 20 || cmd.p != 30 {
					t.Errorf("uint32 fields mismatch: i=%d I=%d p=%d", cmd.i, cmd.I, cmd.p)
				}
				if cmd.s != 100 || cmd.v != 200 || cmd.S != 300 || cmd.O != 400 {
					t.Errorf("s,v,S,O mismatch: s=%d v=%d S=%d O=%d", cmd.s, cmd.v, cmd.S, cmd.O)
				}
				if cmd.x != 1 || cmd.y != 2 || cmd.w != 50 || cmd.h != 60 {
					t.Errorf("rect mismatch: x=%d y=%d w=%d h=%d", cmd.x, cmd.y, cmd.w, cmd.h)
				}
				if cmd.X != 3 || cmd.Y != 4 || cmd.c != 5 || cmd.r != 6 {
					t.Errorf("cell pos mismatch: X=%d Y=%d c=%d r=%d", cmd.X, cmd.Y, cmd.c, cmd.r)
				}
				if cmd.C != 1 || cmd.U != 1 || cmd.m != 1 || cmd.q != 2 || cmd.z != 5 {
					t.Errorf("flags mismatch: C=%d U=%d m=%d q=%d z=%d", cmd.C, cmd.U, cmd.m, cmd.q, cmd.z)
				}
			},
		},
		{
			name:    "corrupt base64 returns error",
			input:   "a=t;???invalid???",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := parseGCmd([]byte(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tc.input, err)
			}
			if tc.check != nil {
				tc.check(t, cmd)
			}
		})
	}
}

func TestAPCNonGIgnored(t *testing.T) {
	term := New(10, 2, 0)
	feed(term, "\x1b_Xabc\x1b\\ok")
	if got := lineText(term, 0); got != "ok" {
		t.Fatalf("expected %q, got %q", "ok", got)
	}
}

func TestAPCTooLong(t *testing.T) {
	term := New(10, 2, 0)
	chunk := strings.Repeat("A", 1<<20) // 1 MB
	feed(term, "\x1b_G")
	for i := 0; i < 5; i++ {
		feed(term, chunk)
	}
	feed(term, "\x1b\\ok")
	if got := lineText(term, 0); got != "ok" {
		t.Fatalf("expected %q, got %q", "ok", got)
	}
	if c := cap(term.p.apc); c > maxAPC {
		t.Fatalf("cap(term.p.apc) = %d > maxAPC (%d)", c, maxAPC)
	}
}

func TestAPCCancel(t *testing.T) {
	term := New(10, 2, 0)
	feed(term, "\x1b_Ga=T\x18ok")
	if got := lineText(term, 0); got != "ok" {
		t.Fatalf("CAN did not abort APC: got %q", got)
	}

	term = New(10, 2, 0)
	feed(term, "\x1b_Ga=T\x1aok")
	if got := lineText(term, 0); got != "ok" {
		t.Fatalf("SUB did not abort APC: got %q", got)
	}
}

func TestAPCBufferReleasedAfterST(t *testing.T) {
	term := New(10, 2, 0)
	payload := strings.Repeat("a", 64<<10)
	feed(term, "\x1b_G"+payload+"\x1b\\")
	if c := cap(term.p.apc); c > maxOSCKeep {
		t.Fatalf("APC buffer kept after ST: %d bytes (maxOSCKeep is %d)", c, maxOSCKeep)
	}
}
