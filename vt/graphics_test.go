package vt

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"slices"
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

func createTestPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 40), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func zlibCompress(b []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestTransmitPNG(t *testing.T) {
	term := New(10, 5, 0)
	var reply string
	term.SetReply(func(b []byte) { reply = string(b) })

	pngBytes := createTestPNG(3, 2)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	feed(term, "\x1b_Ga=t,f=100,i=1;"+b64+"\x1b\\")

	if want := "\x1b_Gi=1;OK\x1b\\"; reply != want {
		t.Fatalf("reply: got %q, want %q", reply, want)
	}

	term.Lock()
	img, ok := term.Image(1)
	term.Unlock()

	if !ok {
		t.Fatal("Image(1) not found")
	}
	if img.W != 3 || img.H != 2 {
		t.Fatalf("dimensions: got %dx%d, want 3x2", img.W, img.H)
	}
	if img.Format != 100 {
		t.Fatalf("format: got %d, want 100", img.Format)
	}
	if img.ID != 1 {
		t.Fatalf("ID: got %d, want 1", img.ID)
	}
	if !bytes.Equal(img.Data, pngBytes) {
		t.Fatal("image data does not match pngBytes")
	}
	if img.Gen == 0 {
		t.Fatal("Gen should be > 0")
	}
}

func TestTransmitChunked(t *testing.T) {
	term := New(10, 5, 0)
	var replies []string
	term.SetReply(func(b []byte) { replies = append(replies, string(b)) })

	pngBytes := createTestPNG(3, 2)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)

	// Split into 3 chunks
	n := len(b64)
	c1 := b64[:n/3]
	c2 := b64[n/3 : 2*n/3]
	c3 := b64[2*n/3:]

	// Chunk 1
	feed(term, "\x1b_Ga=t,f=100,i=1,m=1;"+c1+"\x1b\\")
	if len(replies) != 0 {
		t.Fatalf("unexpected reply after chunk 1: %v", replies)
	}
	term.Lock()
	if _, ok := term.Image(1); ok {
		term.Unlock()
		t.Fatal("image stored before all chunks received")
	}
	term.Unlock()

	// Chunk 2
	feed(term, "\x1b_Gm=1;"+c2+"\x1b\\")
	if len(replies) != 0 {
		t.Fatalf("unexpected reply after chunk 2: %v", replies)
	}

	// Chunk 3
	feed(term, "\x1b_Gm=0;"+c3+"\x1b\\")
	if len(replies) != 1 || replies[0] != "\x1b_Gi=1;OK\x1b\\" {
		t.Fatalf("reply: got %v, want [%q]", replies, "\x1b_Gi=1;OK\x1b\\")
	}

	term.Lock()
	img, ok := term.Image(1)
	term.Unlock()

	if !ok {
		t.Fatal("Image(1) not found")
	}
	if img.W != 3 || img.H != 2 {
		t.Fatalf("dimensions: got %dx%d, want 3x2", img.W, img.H)
	}
}

func TestTransmitZlibRGBA(t *testing.T) {
	term := New(10, 5, 0)
	var reply string
	term.SetReply(func(b []byte) { reply = string(b) })

	// 2x2 RGBA = 16 bytes
	raw := make([]byte, 16)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	compressed := zlibCompress(raw)
	b64 := base64.StdEncoding.EncodeToString(compressed)

	feed(term, "\x1b_Ga=t,f=32,s=2,v=2,o=z,i=2;"+b64+"\x1b\\")
	if want := "\x1b_Gi=2;OK\x1b\\"; reply != want {
		t.Fatalf("reply: got %q, want %q", reply, want)
	}

	term.Lock()
	img, ok := term.Image(2)
	term.Unlock()

	if !ok {
		t.Fatal("Image(2) not found")
	}
	if len(img.Data) != 16 {
		t.Fatalf("len(img.Data): got %d, want 16", len(img.Data))
	}
	if !bytes.Equal(img.Data, raw) {
		t.Fatalf("decompressed data mismatch")
	}
	if img.W != 2 || img.H != 2 {
		t.Fatalf("dimensions: got %dx%d, want 2x2", img.W, img.H)
	}
	if img.Format != 32 {
		t.Fatalf("format: got %d, want 32", img.Format)
	}
}

func TestQuery(t *testing.T) {
	term := New(10, 5, 0)
	var reply string
	term.SetReply(func(b []byte) { reply = string(b) })

	feed(term, "\x1b_Ga=q,i=31,s=1,v=1,f=24;AAAA\x1b\\")
	if want := "\x1b_Gi=31;OK\x1b\\"; reply != want {
		t.Fatalf("reply: got %q, want %q", reply, want)
	}

	term.Lock()
	_, ok := term.Image(31)
	term.Unlock()
	if ok {
		t.Fatal("Image(31) should not be stored after query")
	}
}

func TestQuiet(t *testing.T) {
	pngBytes := createTestPNG(2, 2)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)

	// q=1 suppresses OK but keeps errors
	{
		term := New(10, 5, 0)
		var replies []string
		term.SetReply(func(b []byte) { replies = append(replies, string(b)) })

		feed(term, "\x1b_Ga=t,f=100,i=1,q=1;"+b64+"\x1b\\")
		if len(replies) != 0 {
			t.Fatalf("q=1 should suppress OK, got replies: %v", replies)
		}
		term.Lock()
		if _, ok := term.Image(1); !ok {
			term.Unlock()
			t.Fatal("Image(1) should be stored even with q=1")
		}
		term.Unlock()

		// Error should still be sent with q=1
		feed(term, "\x1b_Ga=t,f=100,i=2,q=1;notvalidbase64???\x1b\\")
		if len(replies) != 1 || !strings.Contains(replies[0], "EINVAL") {
			t.Fatalf("q=1 should report error, got: %v", replies)
		}
	}

	// q=2 suppresses everything (OK and errors)
	{
		term := New(10, 5, 0)
		var replies []string
		term.SetReply(func(b []byte) { replies = append(replies, string(b)) })

		feed(term, "\x1b_Ga=t,f=100,i=1,q=2;"+b64+"\x1b\\")
		if len(replies) != 0 {
			t.Fatalf("q=2 should suppress OK, got: %v", replies)
		}
		term.Lock()
		if _, ok := term.Image(1); !ok {
			term.Unlock()
			t.Fatal("Image(1) should be stored with q=2")
		}
		term.Unlock()

		feed(term, "\x1b_Ga=t,f=100,i=2,q=2;notvalidbase64???\x1b\\")
		if len(replies) != 0 {
			t.Fatalf("q=2 should suppress errors, got: %v", replies)
		}
	}
}

func TestBadData(t *testing.T) {
	// Corrupt PNG
	{
		term := New(10, 5, 0)
		var reply string
		term.SetReply(func(b []byte) { reply = string(b) })

		b64 := base64.StdEncoding.EncodeToString([]byte("not a valid png file"))
		feed(term, "\x1b_Ga=t,f=100,i=1;"+b64+"\x1b\\")

		if !strings.HasPrefix(reply, "\x1b_Gi=1;EBADPNG") {
			t.Fatalf("expected reply starting with \\x1b_Gi=1;EBADPNG, got %q", reply)
		}
		term.Lock()
		if _, ok := term.Image(1); ok {
			term.Unlock()
			t.Fatal("corrupt PNG should not be stored")
		}
		term.Unlock()
	}

	// RGB size mismatch
	{
		term := New(10, 5, 0)
		var reply string
		term.SetReply(func(b []byte) { reply = string(b) })

		// 2x2 RGB requires 2*2*3 = 12 bytes; AAAA decodes to 3 bytes
		feed(term, "\x1b_Ga=t,f=24,s=2,v=2,i=2;AAAA\x1b\\")

		if !strings.HasPrefix(reply, "\x1b_Gi=2;EINVAL") {
			t.Fatalf("expected reply starting with \\x1b_Gi=2;EINVAL, got %q", reply)
		}
		term.Lock()
		if _, ok := term.Image(2); ok {
			term.Unlock()
			t.Fatal("mismatched RGB should not be stored")
		}
		term.Unlock()
	}

	// Dimensions > maxImageDim
	{
		term := New(10, 5, 0)
		var reply string
		term.SetReply(func(b []byte) { reply = string(b) })

		feed(term, "\x1b_Ga=t,f=24,s=10001,v=1,i=3;AAAA\x1b\\")
		if !strings.HasPrefix(reply, "\x1b_Gi=3;EINVAL") {
			t.Fatalf("expected reply starting with \\x1b_Gi=3;EINVAL, got %q", reply)
		}
		term.Lock()
		if _, ok := term.Image(3); ok {
			term.Unlock()
			t.Fatal("oversized image should not be stored")
		}
		term.Unlock()
	}
}

func TestQuota(t *testing.T) {
	// Exceeding maxImages
	{
		term := New(10, 5, 0)
		// 1x1 RGBA is 4 bytes
		data := base64.StdEncoding.EncodeToString([]byte{255, 0, 0, 255})
		for id := 1; id <= maxImages; id++ {
			feed(term, fmt.Sprintf("\x1b_Ga=t,f=32,s=1,v=1,i=%d,q=1;%s\x1b\\", id, data))
		}
		term.Lock()
		if _, ok := term.Image(1); !ok {
			term.Unlock()
			t.Fatal("Image(1) should still exist at maxImages")
		}
		term.Unlock()

		// Storing one more image exceeds maxImages -> evicts oldest (ID 1)
		feed(term, fmt.Sprintf("\x1b_Ga=t,f=32,s=1,v=1,i=%d,q=1;%s\x1b\\", maxImages+1, data))
		term.Lock()
		if _, ok := term.Image(1); ok {
			term.Unlock()
			t.Fatal("Image(1) should have been evicted when exceeding maxImages")
		}
		if _, ok := term.Image(maxImages + 1); !ok {
			term.Unlock()
			t.Fatal("new image should be stored")
		}
		term.Unlock()
	}

	// Exceeding maxImageBytes
	{
		term := New(10, 5, 0)
		// Send two 17 MB images (each 2048 x 2176 x 4 = 17,825,792 bytes).
		// Compressed with zlib so each sequence fits under maxAPC (4 MB).
		// Total stored uncompressed: ~35.6 MB > 32 MB maxImageBytes.
		w, h := 2048, 2176
		raw := make([]byte, w*h*4)
		compressed := zlibCompress(raw)
		b64 := base64.StdEncoding.EncodeToString(compressed)

		feed(term, fmt.Sprintf("\x1b_Ga=t,f=32,s=%d,v=%d,o=z,i=1,q=1;%s\x1b\\", w, h, b64))
		term.Lock()
		if _, ok := term.Image(1); !ok {
			term.Unlock()
			t.Fatal("Image 1 should be stored")
		}
		term.Unlock()

		feed(term, fmt.Sprintf("\x1b_Ga=t,f=32,s=%d,v=%d,o=z,i=2,q=1;%s\x1b\\", w, h, b64))
		term.Lock()
		if _, ok := term.Image(1); ok {
			term.Unlock()
			t.Fatal("Image 1 should have been evicted when exceeding maxImageBytes")
		}
		if _, ok := term.Image(2); !ok {
			term.Unlock()
			t.Fatal("Image 2 should be stored")
		}
		term.Unlock()
	}
}

func TestImageNumber(t *testing.T) {
	term := New(10, 5, 0)
	var replies []string
	term.SetReply(func(b []byte) { replies = append(replies, string(b)) })

	pngBytes := createTestPNG(2, 2)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)

	// I=5 without i assigns a free id and replies i=<id>,I=5
	feed(term, "\x1b_Ga=t,f=100,I=5;"+b64+"\x1b\\")
	if len(replies) != 1 || replies[0] != "\x1b_Gi=1,I=5;OK\x1b\\" {
		t.Fatalf("reply: got %v, want [%q]", replies, "\x1b_Gi=1,I=5;OK\x1b\\")
	}

	term.Lock()
	img, ok := term.Image(1)
	term.Unlock()
	if !ok {
		t.Fatal("Image(1) not found")
	}
	if img.Number != 5 {
		t.Fatalf("img.Number: got %d, want 5", img.Number)
	}

	// Another image with I=9 assigns next free id (2)
	feed(term, "\x1b_Ga=t,f=100,I=9;"+b64+"\x1b\\")
	if len(replies) != 2 || replies[1] != "\x1b_Gi=2,I=9;OK\x1b\\" {
		t.Fatalf("reply: got %v, want [%q]", replies, "\x1b_Gi=2,I=9;OK\x1b\\")
	}

	// Specifying both i and I is an error (EINVAL)
	feed(term, "\x1b_Ga=t,f=100,i=10,I=10;"+b64+"\x1b\\")
	if len(replies) != 3 || !strings.Contains(replies[2], "EINVAL") {
		t.Fatalf("expected EINVAL reply for both i and I, got %v", replies)
	}
}

func TestFileTransmissionRejectedInVT(t *testing.T) {
	term := New(10, 5, 0)
	var reply string
	term.SetReply(func(b []byte) { reply = string(b) })

	b64 := base64.StdEncoding.EncodeToString([]byte("/tmp/test.png"))
	feed(term, "\x1b_Ga=t,t=f,f=100,i=1;"+b64+"\x1b\\")

	if want := "\x1b_Gi=1;EBADF\x1b\\"; reply != want {
		t.Fatalf("reply: got %q, want %q", reply, want)
	}

	term.Lock()
	if _, ok := term.Image(1); ok {
		term.Unlock()
		t.Fatal("file transmission should not store image in vt")
	}
	term.Unlock()
}

func TestTransmitReplace(t *testing.T) {
	term := New(10, 5, 0)
	var replies []string
	term.SetReply(func(b []byte) { replies = append(replies, string(b)) })

	png1 := createTestPNG(2, 2)
	b64_1 := base64.StdEncoding.EncodeToString(png1)
	feed(term, "\x1b_Ga=t,f=100,i=42;"+b64_1+"\x1b\\")

	term.Lock()
	img1, ok := term.Image(42)
	term.Unlock()
	if !ok {
		t.Fatal("Image(42) not found")
	}

	seq1 := term.GraphicsSeq()

	// Replace with new image on same ID
	png2 := createTestPNG(4, 5)
	b64_2 := base64.StdEncoding.EncodeToString(png2)
	feed(term, "\x1b_Ga=t,f=100,i=42;"+b64_2+"\x1b\\")

	term.Lock()
	img2, ok := term.Image(42)
	term.Unlock()
	if !ok {
		t.Fatal("Image(42) not found after replace")
	}
	if img2.W != 4 || img2.H != 5 {
		t.Fatalf("new dimensions: got %dx%d, want 4x5", img2.W, img2.H)
	}
	if img2.Gen <= img1.Gen {
		t.Fatalf("Gen should increase: got %d <= %d", img2.Gen, img1.Gen)
	}
	if term.GraphicsSeq() <= seq1 {
		t.Fatalf("GraphicsSeq should increase: got %d <= %d", term.GraphicsSeq(), seq1)
	}
}

func TestResetClearsGraphics(t *testing.T) {
	term := New(10, 5, 0)
	png1 := createTestPNG(2, 2)
	b64 := base64.StdEncoding.EncodeToString(png1)
	feed(term, "\x1b_Ga=t,f=100,i=1;"+b64+"\x1b\\")

	term.Lock()
	if _, ok := term.Image(1); !ok {
		term.Unlock()
		t.Fatal("Image(1) not found")
	}
	seq1 := term.GraphicsSeq()
	term.reset()
	if _, ok := term.Image(1); ok {
		term.Unlock()
		t.Fatal("Image(1) should be gone after reset")
	}
	if term.GraphicsSeq() <= seq1 {
		term.Unlock()
		t.Fatalf("GraphicsSeq should increase after reset: %d <= %d", term.GraphicsSeq(), seq1)
	}
	term.Unlock()
}

func TestZlibBomb(t *testing.T) {
	term := New(10, 5, 0)
	var replies []string
	term.SetReply(func(b []byte) { replies = append(replies, string(b)) })

	// Compress 64 MiB of zeros.
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	zeroBlock := make([]byte, 64*1024)
	for i := 0; i < 1024; i++ {
		if _, err := zw.Write(zeroBlock); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	b64 := base64.StdEncoding.EncodeToString(compressed.Bytes())

	// Send as 64 MiB RGBA image (4096*4096*4 = 67,108,864 bytes).
	feed(term, "\x1b_Ga=t,f=32,s=4096,v=4096,o=z,i=1;"+b64+"\x1b\\")

	if len(replies) != 1 || replies[0] != "\x1b_Gi=1;EINVAL\x1b\\" {
		t.Fatalf("expected EINVAL reply, got %v", replies)
	}
	term.Lock()
	if _, ok := term.Image(1); ok {
		term.Unlock()
		t.Fatal("expected no image stored for zlib bomb")
	}
	term.Unlock()
}

func TestChunkedPastCap(t *testing.T) {
	term := New(10, 5, 0)
	var replies []string
	term.SetReply(func(b []byte) { replies = append(replies, string(b)) })

	// Each chunk is 2 MiB decoded (2,097,152 bytes).
	// Base64 is ~2.8 MB < maxAPC (4 MiB).
	chunkData := make([]byte, 2<<20)
	chunkB64 := base64.StdEncoding.EncodeToString(chunkData)

	// Send chunk 1 with m=1, i=1, f=100
	feed(term, "\x1b_Ga=t,f=100,i=1,m=1;"+chunkB64+"\x1b\\")

	// Chunks 2..16: 15 * 2 MiB = 30 MiB (accumulated 32 MiB == maxImageBytes)
	for i := 2; i <= 16; i++ {
		feed(term, "\x1b_Gm=1;"+chunkB64+"\x1b\\")
	}
	if len(replies) != 0 {
		t.Fatalf("expected no replies yet, got %v", replies)
	}

	// Chunk 17: brings total to 34 MiB > maxImageBytes
	feed(term, "\x1b_Gm=1;"+chunkB64+"\x1b\\")

	if len(replies) != 1 || replies[0] != "\x1b_Gi=1;EINVAL\x1b\\" {
		t.Fatalf("expected EINVAL reply on exceeding cap, got %v", replies)
	}

	term.Lock()
	if _, ok := term.Image(1); ok {
		term.Unlock()
		t.Fatal("expected no image stored when chunks exceed cap")
	}
	term.Unlock()
}

func TestNonContinuationDiscardsActiveChunk(t *testing.T) {
	term := New(10, 5, 0)
	var replies []string
	term.SetReply(func(b []byte) { replies = append(replies, string(b)) })

	// Start a chunked transfer for image 1
	dummyChunk := base64.StdEncoding.EncodeToString([]byte("incomplete-chunk-data"))
	feed(term, "\x1b_Ga=t,f=100,i=1,m=1;"+dummyChunk+"\x1b\\")

	// Interrupt with a query (a=q, which has keys 'a', 'i', 's', 'v', 'f')
	feed(term, "\x1b_Ga=q,i=99,s=1,v=1,f=24;AAAA\x1b\\")

	if len(replies) != 1 || replies[0] != "\x1b_Gi=99;OK\x1b\\" {
		t.Fatalf("expected query OK reply, got %v", replies)
	}

	// Now send a fresh, valid image 2 with m=0
	pngBytes := createTestPNG(2, 2)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	feed(term, "\x1b_Ga=t,f=100,i=2;"+b64+"\x1b\\")

	if len(replies) != 2 || replies[1] != "\x1b_Gi=2;OK\x1b\\" {
		t.Fatalf("expected image 2 OK reply, got %v", replies)
	}

	term.Lock()
	defer term.Unlock()

	// Image 1 should NOT exist (transfer was aborted)
	if _, ok := term.Image(1); ok {
		t.Fatal("Image(1) should not exist")
	}

	// Image 2 should exist cleanly
	img2, ok := term.Image(2)
	if !ok {
		t.Fatal("Image(2) not found")
	}
	if img2.W != 2 || img2.H != 2 {
		t.Fatalf("Image(2) dimensions: got %dx%d, want 2x2", img2.W, img2.H)
	}
	if !bytes.Equal(img2.Data, pngBytes) {
		t.Fatal("Image(2) data corrupted by stale chunk data")
	}
}

func TestNonContinuationInterruptByNewTransmit(t *testing.T) {
	term := New(10, 5, 0)
	var replies []string
	term.SetReply(func(b []byte) { replies = append(replies, string(b)) })

	// Start a chunked transfer for image 1
	dummyChunk := base64.StdEncoding.EncodeToString([]byte("stale-chunk-data"))
	feed(term, "\x1b_Ga=t,f=100,i=1,m=1;"+dummyChunk+"\x1b\\")

	// Directly send a new image 2 (non-continuation because it has keys a, f, i)
	pngBytes := createTestPNG(3, 3)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	feed(term, "\x1b_Ga=t,f=100,i=2;"+b64+"\x1b\\")

	if len(replies) != 1 || replies[0] != "\x1b_Gi=2;OK\x1b\\" {
		t.Fatalf("expected image 2 OK reply, got %v", replies)
	}

	term.Lock()
	defer term.Unlock()

	if _, ok := term.Image(1); ok {
		t.Fatal("Image(1) should not exist")
	}
	img2, ok := term.Image(2)
	if !ok {
		t.Fatal("Image(2) not found")
	}
	if img2.W != 3 || img2.H != 3 {
		t.Fatalf("Image(2) dimensions: got %dx%d, want 3x3", img2.W, img2.H)
	}
}

// rgbaCmd builds a kitty graphics command carrying a blank w×h RGBA image.
func rgbaCmd(keys string, w, h int) string {
	data := base64.StdEncoding.EncodeToString(make([]byte, w*h*4))
	return fmt.Sprintf("\x1b_G%s,f=32,s=%d,v=%d;%s\x1b\\", keys, w, h, data)
}

func visiblePlacements(term *Terminal, offset int) []Placement {
	term.Lock()
	defer term.Unlock()
	return term.VisiblePlacements(offset, nil)
}

func hasImage(term *Terminal, id uint32) bool {
	term.Lock()
	defer term.Unlock()
	_, ok := term.Image(id)
	return ok
}

func cursorPos(term *Terminal) (int, int) {
	term.Lock()
	defer term.Unlock()
	x, y, _, _ := term.Cursor()
	return x, y
}

func onePlacement(t *testing.T, term *Terminal, offset int) Placement {
	t.Helper()
	ps := visiblePlacements(term, offset)
	if len(ps) != 1 {
		t.Fatalf("got %d placements, want 1: %+v", len(ps), ps)
	}
	return ps[0]
}

func TestPlaceAtCursor(t *testing.T) {
	term := New(20, 10, 100)
	term.SetPixelSize(200, 200) // 10×20 cells
	feed(term, "\x1b[4;3H"+rgbaCmd("a=T,i=1,q=2", 25, 50))
	p := onePlacement(t, term, 0)
	want := Placement{ImageID: 1, Row: 3, Col: 2, Src: image.Rect(0, 0, 25, 50), Cols: 3, Rows: 3}
	if p != want {
		t.Fatalf("placement: got %+v, want %+v", p, want)
	}
	if x, y := cursorPos(term); x != 5 || y != 5 {
		t.Fatalf("cursor: got (%d,%d), want (5,5)", x, y)
	}

	term = New(20, 10, 100)
	term.SetPixelSize(200, 200)
	feed(term, "\x1b[4;3H"+rgbaCmd("a=T,i=1,q=2,C=1", 25, 50))
	onePlacement(t, term, 0)
	if x, y := cursorPos(term); x != 2 || y != 3 {
		t.Fatalf("cursor with C=1: got (%d,%d), want (2,3)", x, y)
	}
}

func TestPlaceSizeInCells(t *testing.T) {
	term := New(20, 10, 100)
	term.SetPixelSize(200, 200)
	feed(term, rgbaCmd("a=T,i=1,q=2,c=4,r=2", 25, 50))
	p := onePlacement(t, term, 0)
	if p.Cols != 4 || p.Rows != 2 {
		t.Fatalf("size: got %d×%d, want 4×2", p.Cols, p.Rows)
	}
}

func TestPlaceCommand(t *testing.T) {
	term := New(20, 10, 100)
	var replies []string
	term.SetReply(func(b []byte) { replies = append(replies, string(b)) })
	// Unknown pixel size: 8×16 cells.
	feed(term, rgbaCmd("a=t,i=1,q=1", 40, 40))
	feed(term, "\x1b[2;2H\x1b_Ga=p,i=1,p=7,x=8,y=4,w=16,h=20,X=3,Y=5,z=-2\x1b\\")
	p := onePlacement(t, term, 0)
	want := Placement{ImageID: 1, ID: 7, Row: 1, Col: 1, OffX: 3, OffY: 5,
		Src: image.Rect(8, 4, 24, 24), Cols: 3, Rows: 2, Z: -2}
	if p != want {
		t.Fatalf("placement: got %+v, want %+v", p, want)
	}
	if len(replies) != 1 || replies[0] != "\x1b_Gi=1,p=7;OK\x1b\\" {
		t.Fatalf("replies: got %q", replies)
	}
	// Same placement id: moved, not duplicated.
	feed(term, "\x1b[6;6H\x1b_Ga=p,i=1,p=7,C=1,q=2\x1b\\")
	p = onePlacement(t, term, 0)
	if p.Row != 5 || p.Col != 5 || p.Src != image.Rect(0, 0, 40, 40) {
		t.Fatalf("replaced placement: got %+v", p)
	}
	// Missing image.
	replies = nil
	feed(term, "\x1b_Ga=p,i=9\x1b\\")
	if len(replies) != 1 || !strings.HasPrefix(replies[0], "\x1b_Gi=9;ENOENT") {
		t.Fatalf("missing image reply: got %q", replies)
	}
	// By image number.
	replies = nil
	feed(term, rgbaCmd("a=t,I=5,q=2", 8, 16))
	feed(term, "\x1b_Ga=p,I=5,C=1\x1b\\")
	if len(visiblePlacements(term, 0)) != 2 {
		t.Fatalf("placement by number missing")
	}
	if len(replies) != 1 || !strings.HasSuffix(replies[0], ",I=5;OK\x1b\\") {
		t.Fatalf("number reply: got %q", replies)
	}
}

func TestPlacementsSortedByZ(t *testing.T) {
	term := New(20, 10, 100)
	feed(term, rgbaCmd("a=t,i=1,q=2", 8, 16))
	feed(term, "\x1b_Ga=p,i=1,p=1,z=5,C=1\x1b\\")
	feed(term, "\x1b_Ga=p,i=1,p=2,z=-1,C=1\x1b\\")
	feed(term, "\x1b_Ga=p,i=1,p=3,z=5,C=1\x1b\\")
	feed(term, "\x1b_Ga=p,i=1,p=4,C=1\x1b\\")
	var got []uint32
	for _, p := range visiblePlacements(term, 0) {
		got = append(got, p.ID)
	}
	if fmt.Sprint(got) != "[2 4 1 3]" {
		t.Fatalf("order: got %v, want [2 4 1 3]", got)
	}
}

func TestPlacementScrollsWithText(t *testing.T) {
	term := New(20, 10, 100)
	feed(term, "\x1b[10;1H"+rgbaCmd("a=T,i=1,q=2,C=1", 8, 16))
	if p := onePlacement(t, term, 0); p.Row != 9 {
		t.Fatalf("row: got %d, want 9", p.Row)
	}
	feed(term, "\n\n\n\n")
	if p := onePlacement(t, term, 0); p.Row != 5 {
		t.Fatalf("row after scroll: got %d, want 5", p.Row)
	}
	if p := onePlacement(t, term, 4); p.Row != 9 {
		t.Fatalf("row with offset 4: got %d, want 9", p.Row)
	}
}

func TestPlacementScrollRegions(t *testing.T) {
	term := New(20, 10, 100)
	feed(term, rgbaCmd("a=t,i=1,q=2", 8, 16))
	feed(term, "\x1b[3;1H\x1b_Ga=p,i=1,p=1,C=1,q=2\x1b\\")
	feed(term, "\x1b[8;1H\x1b_Ga=p,i=1,p=2,C=1,q=2\x1b\\")
	rows := func() string {
		var s []string
		for _, p := range visiblePlacements(term, 0) {
			s = append(s, fmt.Sprintf("%d:%d", p.ID, p.Row))
		}
		return strings.Join(s, " ")
	}
	// A partial region from the top: its lines go to the history but the
	// placements stay put.
	feed(term, "\x1b[1;5r\x1b[5;1H\n\n")
	if got := rows(); got != "1:2 2:7" {
		t.Fatalf("partial scroll up: got %q", got)
	}
	feed(term, "\x1b[2;5r\x1b[2;1H\x1bM")
	if got := rows(); got != "1:2 2:7" {
		t.Fatalf("partial scroll down: got %q", got)
	}
	// Full screen reverse scroll moves them down.
	feed(term, "\x1b[r\x1b[1;1H\x1bM\x1bM")
	if got := rows(); got != "1:4 2:9" {
		t.Fatalf("full scroll down: got %q", got)
	}
	feed(term, "\x1bM")
	if got := rows(); got != "1:5" {
		t.Fatalf("pushed off the bottom: got %q", got)
	}
}

func TestPlacementEvictedWithHistory(t *testing.T) {
	term := New(20, 5, 5)
	feed(term, rgbaCmd("a=T,i=1,q=2,C=1", 8, 16))
	feed(term, rgbaCmd("a=t,i=2,q=2", 8, 16))
	feed(term, "\x1b_Ga=p,i=2,C=1,q=2\x1b\\")
	feed(term, "\x1b[5;1H\n\n\n\n\n")
	if ps := visiblePlacements(term, 5); len(ps) != 2 || ps[0].Row != 0 {
		t.Fatalf("still in history: got %+v", ps)
	}
	feed(term, "\n")
	if ps := visiblePlacements(term, 5); len(ps) != 0 {
		t.Fatalf("anchor evicted: got %+v", ps)
	}
	if hasImage(term, 1) {
		t.Fatal("image sent with a=T should be freed with its last placement")
	}
	if !hasImage(term, 2) {
		t.Fatal("image sent with a=t should be kept")
	}
}

func TestAltScreenHasOwnPlacements(t *testing.T) {
	term := New(20, 10, 100)
	feed(term, rgbaCmd("a=T,i=1,q=2,C=1", 8, 16))
	feed(term, "\x1b[?1049h"+rgbaCmd("a=T,i=2,q=2,C=1", 8, 16))
	if p := onePlacement(t, term, 0); p.ImageID != 2 {
		t.Fatalf("alt screen: got image %d, want 2", p.ImageID)
	}
	feed(term, "\x1b[?1049l")
	if p := onePlacement(t, term, 0); p.ImageID != 1 {
		t.Fatalf("main screen: got image %d, want 1", p.ImageID)
	}
}

func TestAltResizeDropsPlacements(t *testing.T) {
	term := New(20, 5, 100)
	feed(term, "\x1b[?1049h"+rgbaCmd("a=t,i=1,q=2", 8, 16))
	feed(term, "\x1b_Ga=p,i=1,p=1,C=1,q=2\x1b\\")
	feed(term, "\x1b[5;1Hx\x1b_Ga=p,i=1,p=2,C=1,q=2\x1b\\")
	term.Resize(20, 3)
	p := onePlacement(t, term, 0)
	if p.ID != 2 || p.Row != 2 {
		t.Fatalf("after resize: got %+v, want p=2 on row 2", p)
	}
}

func TestReplaceDeletesPlacements(t *testing.T) {
	term := New(20, 10, 100)
	feed(term, rgbaCmd("a=T,i=1,q=2,C=1", 8, 16))
	feed(term, rgbaCmd("a=t,i=1,q=2", 8, 16))
	if ps := visiblePlacements(term, 0); len(ps) != 0 {
		t.Fatalf("replacement kept placements: %+v", ps)
	}
	feed(term, rgbaCmd("a=T,i=1,q=2,C=1", 8, 16))
	onePlacement(t, term, 0)
}

func TestMaxPlacements(t *testing.T) {
	term := New(20, 10, 100)
	feed(term, rgbaCmd("a=t,i=1,q=2", 8, 16))
	cmd := strings.Repeat("\x1b_Ga=p,i=1,C=1,q=2\x1b\\", maxPlacements+1)
	feed(term, cmd)
	if n := len(visiblePlacements(term, 0)); n != maxPlacements {
		t.Fatalf("got %d placements, want %d", n, maxPlacements)
	}
}

func TestDelete(t *testing.T) {
	setup := func() *Terminal {
		term := New(20, 10, 100)
		term.SetPixelSize(160, 160) // 8×16 cells
		feed(term, rgbaCmd("a=t,i=1,q=2", 8, 16))
		feed(term, rgbaCmd("a=t,i=2,q=2", 8, 16))
		feed(term, "\x1b[2;2H\x1b_Ga=p,i=1,p=1,c=2,r=2,C=1,q=2\x1b\\")
		feed(term, "\x1b[6;11H\x1b_Ga=p,i=1,p=2,c=2,r=2,z=5,C=1,q=2\x1b\\")
		feed(term, "\x1b[9;1H\x1b_Ga=p,i=2,z=-1,C=1,q=2\x1b\\")
		return term
	}
	tests := []struct {
		cmd    string
		left   string // remaining placements, image/placement
		images string // images still stored
	}{
		{"\x1b_Ga=d\x1b\\", "", "1 2"},
		{"\x1b_Ga=d,d=a\x1b\\", "", "1 2"},
		{"\x1b_Ga=d,d=A\x1b\\", "", ""},
		{"\x1b_Ga=d,d=i,i=1\x1b\\", "2/0", "1 2"},
		{"\x1b_Ga=d,d=I,i=1\x1b\\", "2/0", "2"},
		{"\x1b_Ga=d,d=i,i=1,p=2\x1b\\", "1/1 2/0", "1 2"},
		{"\x1b_Ga=d,d=I,i=1,p=2\x1b\\", "1/1 2/0", "1 2"},
		{"\x1b_Ga=d,d=p,x=3,y=3\x1b\\", "1/2 2/0", "1 2"},
		{"\x1b_Ga=d,d=P,x=1,y=9\x1b\\", "1/1 1/2", "1"},
		{"\x1b[7;12H\x1b_Ga=d,d=c\x1b\\", "1/1 2/0", "1 2"},
		{"\x1b[9;1H\x1b_Ga=d,d=C\x1b\\", "1/1 1/2", "1"},
		{"\x1b_Ga=d,d=x,x=11\x1b\\", "1/1 2/0", "1 2"},
		{"\x1b_Ga=d,d=y,y=2\x1b\\", "1/2 2/0", "1 2"},
		{"\x1b_Ga=d,d=z,z=5\x1b\\", "1/1 2/0", "1 2"},
		{"\x1b_Ga=d,d=Z,z=-1\x1b\\", "1/1 1/2", "1"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			term := setup()
			feed(term, tt.cmd)
			var left, images []string
			for _, p := range visiblePlacements(term, 0) {
				left = append(left, fmt.Sprintf("%d/%d", p.ImageID, p.ID))
			}
			slices.Sort(left)
			for _, id := range []uint32{1, 2} {
				if hasImage(term, id) {
					images = append(images, fmt.Sprint(id))
				}
			}
			if got := strings.Join(left, " "); got != tt.left {
				t.Errorf("placements: got %q, want %q", got, tt.left)
			}
			if got := strings.Join(images, " "); got != tt.images {
				t.Errorf("images: got %q, want %q", got, tt.images)
			}
		})
	}
}

func TestEraseDeletes(t *testing.T) {
	term := New(20, 5, 100)
	feed(term, rgbaCmd("a=T,i=1,q=2,C=1", 8, 16))
	feed(term, "\x1b[5;1H\n\n\n\n\n")
	feed(term, "\x1b[3;1H"+rgbaCmd("a=T,i=2,q=2,C=1", 8, 16))
	if p := onePlacement(t, term, 0); p.ImageID != 2 {
		t.Fatalf("setup: got image %d on screen, want 2", p.ImageID)
	}
	feed(term, "\x1b[2J")
	if n := len(visiblePlacements(term, 0)); n != 0 {
		t.Fatalf("after ED 2: got %d placements on screen", n)
	}
	if p := onePlacement(t, term, 5); p.ImageID != 1 {
		t.Fatalf("after ED 2: got image %d, want 1 (in history)", p.ImageID)
	}
	feed(term, "\x1b[3J")
	if n := len(visiblePlacements(term, 5)); n != 0 {
		t.Fatalf("after ED 3: got %d placements", n)
	}
	feed(term, rgbaCmd("a=T,i=3,q=2,C=1", 8, 16))
	feed(term, "\x1bc")
	if n := len(visiblePlacements(term, 0)); n != 0 || hasImage(term, 3) {
		t.Fatalf("after RIS: %d placements, image kept %v", n, hasImage(term, 3))
	}
}

func TestReflowKeepsAnchor(t *testing.T) {
	term := New(10, 5, 100)
	feed(term, "abcdefghijKLMNOPQRST\x1b[2;4H"+rgbaCmd("a=T,i=1,q=2,C=1", 8, 16)+"\x1b[4;1H")
	check := func(cols int) {
		t.Helper()
		term.Resize(cols, 5)
		term.Lock()
		defer term.Unlock()
		// The anchor may be in the history: find the offset that shows it.
		for off := 0; off <= term.HistoryLen(); off++ {
			ps := term.VisiblePlacements(off, nil)
			if len(ps) == 0 {
				continue
			}
			p := ps[0]
			if p.Row < 0 {
				continue
			}
			if r := term.ViewLine(p.Row, off, nil)[p.Col].Rune(); r != 'N' {
				t.Fatalf("width %d: image on %q at (%d,%d), want 'N'", cols, r, p.Col, p.Row)
			}
			return
		}
		t.Fatalf("width %d: placement lost", cols)
	}
	check(10)
	check(20)
	check(10)
	check(5)
	check(10)
	check(2) // the anchor goes to the history
	check(10)
}

func TestReflowThenHistoryEviction(t *testing.T) {
	term := New(10, 5, 5)
	feed(term, "abcdefghijKLMNOPQRST\x1b[2;4H"+rgbaCmd("a=T,i=1,q=2,C=1", 8, 16)+"\x1b[4;1H")
	term.Resize(20, 5)             // the anchor moves up one line, the cursor to row 2
	feed(term, "\n\n\n\n\n\n\n\n") // fills the 5 history lines, then one more
	term.Lock()
	n := len(term.gfx.placements)
	term.Unlock()
	if n != 0 {
		t.Fatalf("placement outlived its line: %d stored", n)
	}
	if hasImage(term, 1) {
		t.Fatal("image sent with a=T should be freed")
	}
}

func TestMaxPlacementsFreesTransient(t *testing.T) {
	term := New(20, 10, 100)
	feed(term, rgbaCmd("a=T,i=1,q=2,C=1", 8, 16))
	feed(term, rgbaCmd("a=t,i=2,q=2", 8, 16))
	feed(term, strings.Repeat("\x1b_Ga=p,i=2,C=1,q=2\x1b\\", maxPlacements))
	if hasImage(term, 1) {
		t.Fatal("a=T image dropped from the table should be freed")
	}
	if n := len(visiblePlacements(term, 0)); n != maxPlacements {
		t.Fatalf("got %d placements", n)
	}
}

func TestFailedPlaceFreesTransient(t *testing.T) {
	term := New(20, 10, 100)
	var replies []string
	term.SetReply(func(b []byte) { replies = append(replies, string(b)) })
	feed(term, rgbaCmd("a=T,i=1,x=50", 8, 16))
	if hasImage(term, 1) {
		t.Fatal("a=T image whose placement failed should not be stored")
	}
	if len(replies) != 1 || !strings.Contains(replies[0], "EINVAL") {
		t.Fatalf("replies: got %q", replies)
	}
}
