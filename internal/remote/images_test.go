package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"weak"

	"github.com/ThiraSoft/neonmux/vt"
)

func colorPNG(w, h int, c color.RGBA) string {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, m)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// imgConn is a phone connected to a fake host, authenticated.
type imgConn struct {
	h      *fakeHost
	client *testClient
	raw    net.Conn
}

func newImgConn(t *testing.T, h *fakeHost) *imgConn {
	t.Helper()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"
	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)
	return &imgConn{h: h, client: client, raw: cRw}
}

// next reads the next message within d; ok is false on timeout.
func (c *imgConn) next(t *testing.T, d time.Duration) (typ uint8, payload []byte, ok bool) {
	t.Helper()
	_ = c.raw.SetReadDeadline(time.Now().Add(d))
	defer func() { _ = c.raw.SetReadDeadline(time.Time{}) }()
	typ, payload, err := readFrame(c.raw)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, os.ErrDeadlineExceeded) {
			return 0, nil, false
		}
		t.Fatalf("readFrame: %v", err)
	}
	return typ, payload, true
}

// images collects the ImageMsg read until nothing comes for d.
func (c *imgConn) images(t *testing.T, d time.Duration) []ImageMsg {
	t.Helper()
	var out []ImageMsg
	for {
		typ, payload, ok := c.next(t, d)
		if !ok {
			return out
		}
		if typ == wImage {
			var m ImageMsg
			if err := json.Unmarshal(payload, &m); err != nil {
				t.Fatalf("unmarshal ImageMsg: %v", err)
			}
			out = append(out, m)
		}
	}
}

func (c *imgConn) changed(key string) {
	c.h.mu.Lock()
	w := c.h.watchers[key]
	c.h.mu.Unlock()
	if w != nil {
		w.Changed()
	}
}

func centerColor(t *testing.T, m ImageMsg) color.RGBA {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatalf("decode sent image: %v", err)
	}
	b := img.Bounds()
	r, g, bl, a := img.At(b.Dx()/2, b.Dy()/2).RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), uint8(a >> 8)}
}

func near(a, b color.RGBA) bool {
	d := func(x, y uint8) int { return max(int(x)-int(y), int(y)-int(x)) }
	return d(a.R, b.R) < 24 && d(a.G, b.G) < 24 && d(a.B, b.B) < 24
}

// Two sessions with an image of the same id and gen: each phone view gets
// its own pixels, and going back to the first session sends its image again
// (the phone dropped it when it left).
func TestImageCrossSessionAndBack(t *testing.T) {
	h := newFakeHost()
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	for key, c := range map[string]color.RGBA{"s1": red, "s2": blue} {
		term := vt.New(80, 24, 100)
		term.SetPixelSize(800, 480)
		_, _ = term.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100,i=1,c=6,r=3;%s\x1b\\", colorPNG(60, 60, c))))
		h.terms[key] = term
	}
	h.sessions = []SessionInfo{{Key: "s1"}, {Key: "s2"}}
	c := newImgConn(t, h)

	want := []struct {
		key string
		c   color.RGBA
	}{{"s1", red}, {"s2", blue}, {"s1", red}}
	for i, w := range want {
		c.client.writeMsg(t, wWatch, WatchMsg{Key: w.key})
		got := c.images(t, 500*time.Millisecond)
		if len(got) != 1 {
			t.Fatalf("step %d (%s): %d images, want 1", i, w.key, len(got))
		}
		if got[0].Key != w.key {
			t.Fatalf("step %d: image for %q, want %q", i, got[0].Key, w.key)
		}
		if cc := centerColor(t, got[0]); !near(cc, w.c) {
			t.Fatalf("step %d (%s): image color %v, want %v", i, w.key, cc, w.c)
		}
	}
}

// An image that leaves the screen is dropped by the phone: it is sent again
// when it shows up again, even with the same generation.
func TestImageResentAfterHidden(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	term.SetPixelSize(800, 480)
	_, _ = term.Write([]byte(fmt.Sprintf("\x1b_Ga=t,f=100,i=1;%s\x1b\\", colorPNG(60, 60, color.RGBA{0, 255, 0, 255}))))
	_, _ = term.Write([]byte("\x1b[1;1H\x1b_Ga=p,i=1,p=1,c=6,r=3\x1b\\"))
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1"}}
	c := newImgConn(t, h)

	c.client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	if got := c.images(t, 500*time.Millisecond); len(got) != 1 {
		t.Fatalf("first view: %d images, want 1", len(got))
	}
	_, _ = term.Write([]byte("\x1b_Ga=d,d=i,i=1\x1b\\")) // placement gone, image kept
	c.changed("s1")
	if got := c.images(t, 300*time.Millisecond); len(got) != 0 {
		t.Fatalf("hidden: %d images, want 0", len(got))
	}
	_, _ = term.Write([]byte("\x1b[1;1H\x1b_Ga=p,i=1,p=1,c=6,r=3\x1b\\"))
	c.changed("s1")
	if got := c.images(t, 500*time.Millisecond); len(got) != 1 {
		t.Fatalf("shown again: %d images, want 1", len(got))
	}
}

// The whole ImageMsg, base64 and JSON included, fits one wire frame.
func TestImageMsgFitsPayload(t *testing.T) {
	m := ImageMsg{Key: strings.Repeat("k", 64), ID: 1<<32 - 1, Gen: 1<<53 - 1, W: 1280, H: 1280,
		Data: make([]byte, maxImagePayload)}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxPayloadSize {
		t.Fatalf("largest ImageMsg is %d bytes, over the %d bytes frame cap", len(b), maxPayloadSize)
	}
}

// RGBA and RGB data shorter than the declared size are refused, not
// silently half drawn.
func TestPrepareImageShortData(t *testing.T) {
	for _, f := range []uint8{24, 32} {
		img := vt.Image{ID: 1, Format: f, W: 10, H: 10, Data: make([]byte, 10), Gen: 1}
		if _, _, _, err := prepareImage(img); err == nil {
			t.Errorf("format %d with short data: no error", f)
		}
	}
}

// Placeholder cells of one placement on one row make a single rect.
func TestCaptureMergesPlaceholders(t *testing.T) {
	term := vt.New(80, 24, 0)
	term.SetPixelSize(800, 480) // 10x20 cells
	_, _ = term.Write([]byte(fmt.Sprintf("\x1b_Ga=t,f=100,i=2;%s\x1b\\", colorPNG(800, 80, color.RGBA{255, 0, 0, 255}))))
	_, _ = term.Write([]byte("\x1b_Ga=p,i=2,U=1,c=80,r=4\x1b\\"))
	ph := "\U0010EEEE"
	rowMarks := []string{"̅", "̍", "̎", "̐"}
	for y, m := range rowMarks {
		// The first cell carries the row; the others follow it.
		_, _ = term.Write([]byte(fmt.Sprintf("\x1b[%d;1H\x1b[38;2;0;0;2m%s%s%s", y+1, ph, m, strings.Repeat(ph, 79))))
	}
	s := Capture(term, 0, testPalette(), 0, 0)
	if len(s.Imgs) != 4 {
		t.Fatalf("%d rects, want 4 (one per row)", len(s.Imgs))
	}
	for y, r := range s.Imgs {
		if r.ID != 2 || r.X != 0 || r.Y != float64(y) || r.W != 80 || r.H != 1 {
			t.Errorf("row %d rect %+v, want x=0 y=%d w=80 h=1", y, r, y)
		}
		if r.SX != 0 || r.SY != y*20 || r.SW != 800 || r.SH != 20 {
			t.Errorf("row %d src %d,%d %dx%d, want 0,%d 800x20", y, r.SX, r.SY, r.SW, r.SH, y*20)
		}
	}
}

// However many placements, a frame carries a bounded number of rects.
func TestCaptureImgRectsBounded(t *testing.T) {
	term := vt.New(80, 24, 0)
	term.SetPixelSize(800, 480)
	_, _ = term.Write([]byte(fmt.Sprintf("\x1b_Ga=t,f=100,i=1;%s\x1b\\", colorPNG(4, 4, color.RGBA{255, 0, 0, 255}))))
	for i := 0; i < maxImgRects+50; i++ {
		_, _ = term.Write([]byte(fmt.Sprintf("\x1b[%d;%dH\x1b_Ga=p,i=1,p=%d,c=1,r=1,C=1\x1b\\", i%24+1, i%80+1, i+1)))
	}
	s := Capture(term, 0, testPalette(), 0, 0)
	if len(s.Imgs) != maxImgRects {
		t.Fatalf("%d rects, want the cap %d", len(s.Imgs), maxImgRects)
	}
}

// The phone's cell size is kept within sane bounds.
func TestPhoneCellPxClamped(t *testing.T) {
	h := newFakeHost()
	h.terms["s1"] = vt.New(80, 24, 100)
	h.sessions = []SessionInfo{{Key: "s1"}}
	c := newImgConn(t, h)
	c.client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	c.images(t, 200*time.Millisecond)
	for _, tc := range []struct{ cw, ch, wantW, wantH int }{
		{100000, 1 << 40, maxCellPx, maxCellPx},
		{-5, 36, 0, 36},
		{18, 36, 18, 36},
	} {
		c.client.writeMsg(t, wSize, SizeMsg{Cols: 40, Rows: 10, CW: tc.cw, CH: tc.ch})
		c.images(t, 200*time.Millisecond)
		c.h.mu.Lock()
		w := c.h.owners["s1"]
		c.h.mu.Unlock()
		if w == nil {
			t.Fatal("no owner")
		}
		if gw, gh := w.CellPx(); gw != tc.wantW || gh != tc.wantH {
			t.Errorf("cw=%d ch=%d: CellPx %d,%d, want %d,%d", tc.cw, tc.ch, gw, gh, tc.wantW, tc.wantH)
		}
	}
}

// withPrepare replaces prepareImage and starts from an empty cache.
func withPrepare(t *testing.T, f func(vt.Image) ([]byte, int, int, error)) {
	t.Helper()
	oldF, oldC := prepareImageFunc, phoneImages
	prepareImageFunc, phoneImages = f, newImageCache(imageCacheBytes, imageCacheEntries)
	t.Cleanup(func() { prepareImageFunc, phoneImages = oldF, oldC })
}

// Capture only reads sizes: it never prepares an image.
func TestCaptureDoesNotPrepare(t *testing.T) {
	calls := 0
	withPrepare(t, func(img vt.Image) ([]byte, int, int, error) {
		calls++
		return prepareImage(img)
	})
	term := vt.New(80, 24, 0)
	term.SetPixelSize(800, 480)
	_, _ = term.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100,i=1,c=6,r=3;%s\x1b\\", redPNG(60, 60))))
	if s := Capture(term, 0, testPalette(), 0, 0); len(s.Imgs) != 1 {
		t.Fatalf("%d rects, want 1", len(s.Imgs))
	}
	if calls != 0 {
		t.Fatalf("Capture prepared %d images, want 0", calls)
	}
}

// A slow preparation holds neither the frames nor the terminal: frames keep
// coming, and the image follows once it is ready.
func TestImageOffFramePath(t *testing.T) {
	release := make(chan struct{})
	withPrepare(t, func(img vt.Image) ([]byte, int, int, error) {
		<-release
		return prepareImage(img)
	})
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	term.SetPixelSize(800, 480)
	_, _ = term.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100,i=1,c=6,r=3;%s\x1b\\", redPNG(60, 60))))
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1"}}
	c := newImgConn(t, h)

	c.client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	frames := 0
	for i := 0; i < 3; i++ {
		done := make(chan struct{})
		go func() { _, _ = term.Write([]byte("x")); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("terminal blocked while an image is prepared")
		}
		c.changed("s1")
		for {
			typ, _, ok := c.next(t, time.Second)
			if !ok {
				t.Fatalf("no frame %d while the image is prepared", frames+1)
			}
			if typ == wImage {
				t.Fatal("image before its preparation ended")
			}
			if typ == wFrame {
				frames++
				break
			}
		}
	}
	close(release)
	if got := c.images(t, 500*time.Millisecond); len(got) != 1 {
		t.Fatalf("%d images after the preparation, want 1", len(got))
	}
}

// An image that cannot be prepared is tried once, not on every frame.
func TestImageFailureNotRetried(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	withPrepare(t, func(vt.Image) ([]byte, int, int, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil, 0, 0, errImageData
	})
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	_, _ = term.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100,i=1,c=6,r=3;%s\x1b\\", redPNG(60, 60))))
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1"}}
	c := newImgConn(t, h)
	c.client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	for i := 0; i < 4; i++ {
		_, _ = term.Write([]byte("x"))
		c.changed("s1")
		if got := c.images(t, 150*time.Millisecond); len(got) != 0 {
			t.Fatalf("%d images, want 0", len(got))
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("prepared %d times, want 1", calls)
	}
}

// The prepared images stay within their byte bound; a new generation
// drops the old one, and a closed terminal's images go.
func TestImageCacheBounded(t *testing.T) {
	withPrepare(t, func(img vt.Image) ([]byte, int, int, error) {
		return make([]byte, 1000), 1, 1, nil
	})
	c := newImageCache(4*1064, 100)
	term := vt.New(10, 10, 0)
	for id := uint32(1); id <= 10; id++ {
		if _, err := c.get(term, vt.Image{ID: id, Gen: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if c.bytes > c.maxBytes || len(c.m) != 4 {
		t.Fatalf("%d bytes in %d entries, want <= %d in 4", c.bytes, len(c.m), c.maxBytes)
	}
	_, _ = c.get(term, vt.Image{ID: 10, Gen: 2})
	for k := range c.m {
		if k.id == 10 && k.gen == 1 {
			t.Fatal("generation 1 of image 10 kept after generation 2")
		}
	}

	func() {
		gone := vt.New(10, 10, 0)
		_, _ = c.get(gone, vt.Image{ID: 1, Gen: 1})
	}()
	runtime.GC()
	runtime.GC()
	_, _ = c.get(term, vt.Image{ID: 20, Gen: 1})
	for k := range c.m {
		if k.term != weak.Make(term) {
			t.Fatalf("entry of a closed terminal kept: %+v", k)
		}
	}
}

// Two terminals' image 1 gen 1 are two entries.
func TestImageCacheKeyedByTerminal(t *testing.T) {
	withPrepare(t, func(img vt.Image) ([]byte, int, int, error) { return img.Data, 1, 1, nil })
	c := newImageCache(imageCacheBytes, imageCacheEntries)
	a, b := vt.New(10, 10, 0), vt.New(10, 10, 0)
	da, _ := c.get(a, vt.Image{ID: 1, Gen: 1, Data: []byte("a")})
	db, _ := c.get(b, vt.Image{ID: 1, Gen: 1, Data: []byte("b")})
	if string(da) != "a" || string(db) != "b" {
		t.Fatalf("got %q and %q, want a and b", da, db)
	}
}
