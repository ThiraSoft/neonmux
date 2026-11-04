package vt

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
