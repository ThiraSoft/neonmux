package remote

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

type fakeHost struct {
	mu         sync.Mutex
	sessions   []SessionInfo
	terms      map[string]*vt.Terminal
	watchers   map[string]Watcher
	inputs     map[string][]byte
	inputCalls int
	sizes      map[string][2]int
	palette    Palette
	unwatched  map[string]bool
	calls      int
}

func newFakeHost() *fakeHost {
	h := &fakeHost{
		terms:     make(map[string]*vt.Terminal),
		watchers:  make(map[string]Watcher),
		inputs:    make(map[string][]byte),
		sizes:     make(map[string][2]int),
		unwatched: make(map[string]bool),
		palette:   testPalette(),
	}
	return h
}

func (h *fakeHost) Sessions() []SessionInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	out := make([]SessionInfo, len(h.sessions))
	copy(out, h.sessions)
	return out
}

func (h *fakeHost) Watch(key string, w Watcher) (unwatch func(), ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if _, exists := h.terms[key]; !exists {
		return nil, false
	}
	h.watchers[key] = w
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.watchers, key)
		h.unwatched[key] = true
	}, true
}

func (h *fakeHost) Terminal(key string) *vt.Terminal {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	return h.terms[key]
}

func (h *fakeHost) Input(key string, w Watcher, b []byte) {
	h.mu.Lock()
	h.calls++
	h.inputCalls++
	h.inputs[key] = append(h.inputs[key], b...)
	t := h.terms[key]
	h.mu.Unlock()
	if t != nil {
		_, _ = t.Write(b)
	}
}

func (h *fakeHost) SetSize(key string, w Watcher) {
	cols, rows := w.Size()
	h.mu.Lock()
	h.calls++
	h.sizes[key] = [2]int{cols, rows}
	t := h.terms[key]
	h.mu.Unlock()
	if t != nil {
		t.Resize(cols, rows)
	}
}

func (h *fakeHost) Palette() Palette {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.palette
}

func setupTestRemote(t *testing.T, h Host) (*Remote, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}
	now := time.Now()
	certs, err := LoadCerts(dir, now)
	if err != nil {
		t.Fatalf("LoadCerts failed: %v", err)
	}
	devices, err := LoadDevices(dir)
	if err != nil {
		t.Fatalf("LoadDevices failed: %v", err)
	}
	resumes := &Resumes{}
	r := &Remote{
		certs:   certs,
		devices: devices,
		resumes: resumes,
		name:    "test-mac",
		host:    h,
	}
	return r, dir
}

// clientConn provides test helper methods for wire frames.
type testClient struct {
	rw io.ReadWriteCloser
}

func (c *testClient) readMsg(t *testing.T) (uint8, []byte) {
	t.Helper()
	typ, payload, err := readFrame(c.rw)
	if err != nil {
		t.Fatalf("readFrame failed: %v", err)
	}
	return typ, payload
}

func (c *testClient) writeMsg(t *testing.T, typ uint8, payload any) {
	t.Helper()
	if err := writeFrame(c.rw, typ, payload); err != nil {
		t.Fatalf("writeFrame failed: %v", err)
	}
}

func assertFrameMatchesTerminal(t *testing.T, f FrameMsg, term *vt.Terminal, pal Palette) {
	t.Helper()
	expected := Capture(term, f.Offset, pal)
	if f.CX != expected.CX || f.CY != expected.CY || f.Cursor != expected.CursorVis {
		t.Fatalf("cursor mismatch: frame=(%d, %d, %v), term=(%d, %d, %v)",
			f.CX, f.CY, f.Cursor, expected.CX, expected.CY, expected.CursorVis)
	}
	if f.Cols != expected.Cols || f.Rows != expected.Rows {
		t.Fatalf("size mismatch: frame=%dx%d, term=%dx%d", f.Cols, f.Rows, expected.Cols, expected.Rows)
	}
	for _, fl := range f.Lines {
		if fl.Y < 0 || fl.Y >= len(expected.Lines) {
			t.Fatalf("frame line Y=%d out of bounds (rows=%d)", fl.Y, len(expected.Lines))
		}
		expectedLine := expected.Lines[fl.Y]
		var frameText, expText strings.Builder
		for _, r := range fl.Runs {
			frameText.WriteString(r.Text)
		}
		for _, r := range expectedLine {
			expText.WriteString(r.Text)
		}
		if frameText.String() != expText.String() {
			t.Fatalf("line %d text mismatch: frame=%q, term=%q", fl.Y, frameText.String(), expText.String())
		}
	}
}

func TestNothingBeforeAuth(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, "https://host.ts.net", netip.MustParseAddr("192.168.1.100"))

	// Read initial Challenge
	typ, _ := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}

	// Send List before Auth
	client.writeMsg(t, wList, struct{}{})

	// Connection should close (or send wError auth then close)
	_, _, err := readFrame(cRw)
	if err == nil {
		// If error frame was sent, next read must be EOF
		_, _, err = readFrame(cRw)
	}
	if err == nil {
		t.Fatal("expected connection to close after unauthed command")
	}

	// Host should have seen no calls
	if h.calls != 0 {
		t.Fatalf("expected 0 host calls before auth, got %d", h.calls)
	}
}

func TestAuthThenWatch(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	key, pubSPKI := makeTestP256Key(t)
	devID := "dev-1"
	credID := []byte("cred-1")
	if err := r.devices.Add(Device{
		ID:     devID,
		Name:   "phone",
		CredID: credID,
		PubKey: pubSPKI,
		Origin: origin,
	}); err != nil {
		t.Fatalf("add device failed: %v", err)
	}

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	// 1. Read Challenge
	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	if err := json.Unmarshal(payload, &ch); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}

	// 2. Send Auth
	cd := makeTestClientDataJSON(t, "webauthn.get", origin, ch.Nonce)
	ad := makeTestAuthData(t, origin, 0x01|0x04, 1)
	sig := signAssertion(t, key, ad, cd)

	sessionKey, sessionPubSPKI := makeTestP256Key(t)
	_ = sessionKey

	authMsg := AuthMsg{
		Assertion: AssertionWire{
			CredID:     credID,
			AuthData:   ad,
			ClientData: cd,
			Sig:        sig,
		},
		SessionPub: sessionPubSPKI,
	}
	client.writeMsg(t, wAuth, authMsg)

	// 3. Receive Authed
	typ, payload = client.readMsg(t)
	if typ != wAuthed {
		t.Fatalf("expected wAuthed, got %d (payload: %s)", typ, string(payload))
	}
	var authed AuthedMsg
	if err := json.Unmarshal(payload, &authed); err != nil {
		t.Fatalf("unmarshal authed: %v", err)
	}
	if len(authed.Hashes) == 0 {
		t.Fatal("expected non-empty cert hashes in Authed")
	}

	// 4. Send List
	client.writeMsg(t, wList, struct{}{})
	typ, payload = client.readMsg(t)
	if typ != wSessions {
		t.Fatalf("expected wSessions, got %d", typ)
	}
	var sess SessionsMsg
	if err := json.Unmarshal(payload, &sess); err != nil {
		t.Fatalf("unmarshal sessions: %v", err)
	}
	if len(sess.List) != 1 || sess.List[0].Key != "s1" {
		t.Fatalf("unexpected sessions list: %+v", sess.List)
	}

	// 5. Send Watch
	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})

	// Expect Theme then full Frame
	typ, payload = client.readMsg(t)
	if typ != wTheme {
		t.Fatalf("expected wTheme, got %d", typ)
	}

	typ, payload = client.readMsg(t)
	if typ != wFrame {
		t.Fatalf("expected wFrame, got %d", typ)
	}
	var f FrameMsg
	if err := json.Unmarshal(payload, &f); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if !f.Full {
		t.Fatal("expected full frame after watch")
	}

	// 6. Write "x" to terminal and call Changed
	_, _ = term.Write([]byte("x"))
	h.mu.Lock()
	watcher := h.watchers["s1"]
	h.mu.Unlock()
	if watcher == nil {
		t.Fatal("expected watcher to be registered")
	}
	watcher.Changed()

	// Expect a frame with only one changed line
	typ, payload = client.readMsg(t)
	if typ != wFrame {
		t.Fatalf("expected wFrame, got %d", typ)
	}
	var f2 FrameMsg
	if err := json.Unmarshal(payload, &f2); err != nil {
		t.Fatalf("unmarshal frame 2: %v", err)
	}
	if f2.Full {
		t.Fatal("expected differential frame, got full")
	}
	if len(f2.Lines) != 1 || f2.Lines[0].Y != 0 {
		t.Fatalf("expected exactly 1 line at Y=0, got %+v", f2.Lines)
	}
	if len(f2.Lines[0].Runs) == 0 || f2.Lines[0].Runs[0].Text != "x" {
		t.Fatalf("expected text 'x' in line 0 run, got %+v", f2.Lines[0].Runs)
	}
}

func authenticateClient(t *testing.T, client *testClient, r *Remote, origin string) {
	t.Helper()
	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	if err := json.Unmarshal(payload, &ch); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}

	key, pubSPKI := makeTestP256Key(t)
	credID := []byte("cred-test")
	_ = r.devices.Add(Device{
		ID:     "dev-test",
		Name:   "phone",
		CredID: credID,
		PubKey: pubSPKI,
		Origin: origin,
	})

	cd := makeTestClientDataJSON(t, "webauthn.get", origin, ch.Nonce)
	ad := makeTestAuthData(t, origin, 0x01|0x04, 1)
	sig := signAssertion(t, key, ad, cd)
	_, sessPub := makeTestP256Key(t)

	client.writeMsg(t, wAuth, AuthMsg{
		Assertion: AssertionWire{
			CredID:     credID,
			AuthData:   ad,
			ClientData: cd,
			Sig:        sig,
		},
		SessionPub: sessPub,
	})

	typ, _ = client.readMsg(t)
	if typ != wAuthed {
		t.Fatalf("expected wAuthed, got %d", typ)
	}
}

func TestFrameRateCapped(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	// Watch session
	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	typ, _ := client.readMsg(t) // Theme
	if typ != wTheme {
		t.Fatalf("expected wTheme, got %d", typ)
	}
	typ, _ = client.readMsg(t) // Initial full Frame
	if typ != wFrame {
		t.Fatalf("expected wFrame, got %d", typ)
	}

	h.mu.Lock()
	watcher := h.watchers["s1"]
	h.mu.Unlock()

	// 1000 writes with Changed in 100 ms
	start := time.Now()
	var frameCount int
	var lastFrame FrameMsg
	doneWriting := make(chan struct{})

	go func() {
		defer close(doneWriting)
		ticker := time.NewTicker(100 * time.Microsecond)
		defer ticker.Stop()
		for i := 0; i < 1000 && time.Since(start) < 100*time.Millisecond; i++ {
			<-ticker.C
			_, _ = term.Write([]byte(string(rune('A' + (i % 26)))))
			watcher.Changed()
		}
	}()

	// Read frames received during the burst
	timer := time.After(150 * time.Millisecond)
loop:
	for {
		select {
		case <-timer:
			break loop
		default:
			ch := make(chan struct {
				typ uint8
				p   []byte
				err error
			}, 1)
			go func() {
				tp, p, err := readFrame(cRw)
				ch <- struct {
					typ uint8
					p   []byte
					err error
				}{tp, p, err}
			}()

			select {
			case res := <-ch:
				if res.err != nil {
					t.Fatalf("read error: %v", res.err)
				}
				if res.typ == wFrame {
					frameCount++
					_ = json.Unmarshal(res.p, &lastFrame)
				}
			case <-time.After(50 * time.Millisecond):
				break loop
			}
		}
	}

	<-doneWriting

	// "1000 writes with Changed in 100 ms: at most 4 frames received, the last one matches the terminal."
	if frameCount > 4 {
		t.Fatalf("expected at most 4 frames received, got %d", frameCount)
	}
	assertFrameMatchesTerminal(t, lastFrame, term, h.palette)
}

func TestSlowClientOneFrameInFlight(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	// Watch session
	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	typ, _ := client.readMsg(t) // Theme
	if typ != wTheme {
		t.Fatalf("expected wTheme, got %d", typ)
	}
	typ, _ = client.readMsg(t) // Initial full Frame
	if typ != wFrame {
		t.Fatalf("expected wFrame, got %d", typ)
	}

	h.mu.Lock()
	watcher := h.watchers["s1"]
	h.mu.Unlock()

	// Reader stops reading now!
	// Write "hello" to terminal and call Changed once to trigger a frame that may block in pipe
	_, _ = term.Write([]byte("hello"))
	watcher.Changed()
	time.Sleep(10 * time.Millisecond)

	// Now 10 000 Changed calls return at once (watcher notification never blocks)
	start := time.Now()
	for i := 0; i < 10000; i++ {
		_, _ = term.Write([]byte("!"))
		watcher.Changed()
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("10 000 Changed calls took too long: %v (should be instantaneous)", elapsed)
	}

	// Drain the reader: at most 2 frames received, last one matches terminal
	var receivedFrames int
	var lastFrame FrameMsg

	// Read with deadline
	for {
		ch := make(chan struct {
			typ uint8
			p   []byte
			err error
		}, 1)
		go func() {
			tp, p, err := readFrame(cRw)
			ch <- struct {
				typ uint8
				p   []byte
				err error
			}{tp, p, err}
		}()

		select {
		case res := <-ch:
			if res.err != nil {
				t.Fatalf("readFrame error: %v", res.err)
			}
			if res.typ == wFrame {
				receivedFrames++
				_ = json.Unmarshal(res.p, &lastFrame)
			}
		case <-time.After(150 * time.Millisecond):
			goto drained
		}
	}
drained:
	if receivedFrames > 2 {
		t.Fatalf("expected at most 2 frames, got %d", receivedFrames)
	}
	assertFrameMatchesTerminal(t, lastFrame, term, h.palette)
}

func TestScrollSendsHistory(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	for i := 0; i < 30; i++ {
		_, _ = term.Write([]byte("line\r\n"))
	}
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// Send Scroll{5}
	client.writeMsg(t, wScroll, ScrollMsg{Offset: 5})

	typ, payload := client.readMsg(t)
	if typ != wFrame {
		t.Fatalf("expected wFrame, got %d", typ)
	}
	var f FrameMsg
	if err := json.Unmarshal(payload, &f); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if !f.Full {
		t.Fatal("expected full frame on scroll")
	}
	if f.Offset != 5 {
		t.Fatalf("expected offset 5, got %d", f.Offset)
	}
}

func TestAuthWrongOriginRejected(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)

	key, pubSPKI := makeTestP256Key(t)
	credID := []byte("cred-origin")
	_ = r.devices.Add(Device{
		ID:     "dev-origin",
		Name:   "phone",
		CredID: credID,
		PubKey: pubSPKI,
		Origin: "https://device-origin.ts.net", // Origin A
	})

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Connection origin is Origin B
	connOrigin := "https://attacker.ts.net"
	go r.serveConn(ctx, sRw, connOrigin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)

	cd := makeTestClientDataJSON(t, "webauthn.get", connOrigin, ch.Nonce)
	ad := makeTestAuthData(t, connOrigin, 0x01|0x04, 1)
	sig := signAssertion(t, key, ad, cd)
	_, sessPub := makeTestP256Key(t)

	client.writeMsg(t, wAuth, AuthMsg{
		Assertion: AssertionWire{
			CredID:     credID,
			AuthData:   ad,
			ClientData: cd,
			Sig:        sig,
		},
		SessionPub: sessPub,
	})

	typ, payload = client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError, got %d", typ)
	}
	var em ErrorMsg
	_ = json.Unmarshal(payload, &em)
	if em.Msg != "auth" {
		t.Fatalf("expected 'auth' error, got %q", em.Msg)
	}
}

func TestResumeOK(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sessionKey, sessionPubSPKI := makeTestP256Key(t)
	devID := "dev-resume"
	credID := []byte("cred-resume")
	_ = r.devices.Add(Device{
		ID:     devID,
		Name:   "phone",
		CredID: credID,
		PubKey: sessionPubSPKI,
		Origin: origin,
	})

	t0 := time.Now()
	resumeID := r.resumes.Issue(devID, sessionPubSPKI, t0)
	r.resumes.Disconnected(devID, t0)

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)

	sig := signResumeChallenge(t, sessionKey, ch.Nonce)
	client.writeMsg(t, wResume, ResumeMsg{ID: resumeID, Sig: sig})

	typ, payload = client.readMsg(t)
	if typ != wAuthed {
		t.Fatalf("expected wAuthed, got %d (payload: %s)", typ, string(payload))
	}
	var authed AuthedMsg
	_ = json.Unmarshal(payload, &authed)
	if authed.DeviceID != devID {
		t.Fatalf("expected device %q, got %q", devID, authed.DeviceID)
	}
}

func TestResumeExpired(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sessionKey, sessionPubSPKI := makeTestP256Key(t)
	devID := "dev-resume-exp"
	credID := []byte("cred-resume-exp")
	_ = r.devices.Add(Device{
		ID:     devID,
		Name:   "phone",
		CredID: credID,
		PubKey: sessionPubSPKI,
		Origin: origin,
	})

	t0 := time.Now().Add(-10 * time.Minute)
	resumeID := r.resumes.Issue(devID, sessionPubSPKI, t0)
	r.resumes.Disconnected(devID, t0) // Disconnected 10 mins ago (> 5m window)

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)

	sig := signResumeChallenge(t, sessionKey, ch.Nonce)
	client.writeMsg(t, wResume, ResumeMsg{ID: resumeID, Sig: sig})

	typ, _ = client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError, got %d", typ)
	}
}

func TestPairFlow(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"
	now := time.Now()

	pairing := r.openPairing(now)
	tok := pairing.Token()

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)

	_, pubSPKI := makeTestP256Key(t)
	credID := []byte("cred-pair-1")
	cd := makeTestClientDataJSON(t, "webauthn.create", origin, ch.Nonce)
	ad := makeTestRegistrationAuthData(t, origin, 0x01|0x04|0x40, 1, credID)
	_, sessPub := makeTestP256Key(t)

	cert := r.certs.Current(now)
	certHash := sha256.Sum256(cert.Certificate[0])
	expectedCode := SASCode(tok, certHash, pubSPKI)

	client.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     credID,
			PubKey:     pubSPKI,
			AuthData:   ad,
			ClientData: cd,
			Name:       "Ricardo's iPhone",
		},
		SessionPub: sessPub,
	})

	// Client receives PairCode
	typ, payload = client.readMsg(t)
	if typ != wPairCode {
		t.Fatalf("expected wPairCode, got %d (payload: %s)", typ, string(payload))
	}
	var pc PairCodeMsg
	_ = json.Unmarshal(payload, &pc)
	if pc.Code != expectedCode {
		t.Fatalf("expected code %q, got %q", expectedCode, pc.Code)
	}

	// Desktop confirms
	r.decidePairing(true)

	// Client receives Authed
	typ, _ = client.readMsg(t)
	if typ != wAuthed {
		t.Fatalf("expected wAuthed, got %d", typ)
	}

	// Verify device stored with connection origin
	devices := r.devices.List()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device stored, got %d", len(devices))
	}
	if devices[0].Origin != origin {
		t.Fatalf("expected origin %q, got %q", origin, devices[0].Origin)
	}
	if devices[0].Name != "Ricardo's iPhone" {
		t.Fatalf("expected name Ricardo's iPhone, got %q", devices[0].Name)
	}

	// Second attempt with same token fails
	sRw2, cRw2 := net.Pipe()
	client2 := &testClient{rw: cRw2}
	go r.serveConn(ctx, sRw2, origin, netip.MustParseAddr("192.168.1.100"))
	_, _ = client2.readMsg(t) // Challenge

	client2.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     []byte("cred-pair-2"),
			PubKey:     pubSPKI,
			AuthData:   ad,
			ClientData: cd,
			Name:       "Phone 2",
		},
		SessionPub: sessPub,
	})
	typ2, _ := client2.readMsg(t)
	if typ2 != wError {
		t.Fatalf("expected wError on reused token, got %d", typ2)
	}
}

func TestTextEncoding(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// 1. "ls" enter -> host got "ls\r"
	client.writeMsg(t, wText, TextMsg{Text: "ls", Enter: true})
	time.Sleep(20 * time.Millisecond)

	h.mu.Lock()
	in1 := string(h.inputs["s1"])
	h.inputs["s1"] = nil
	h.mu.Unlock()
	if in1 != "ls\r" {
		t.Fatalf("expected 'ls\\r', got %q", in1)
	}

	// 2. "a\nb" with bracketed paste
	// Enable bracketed paste on terminal: \x1b[?2004h
	_, _ = term.Write([]byte("\x1b[?2004h"))
	client.writeMsg(t, wText, TextMsg{Text: "a\nb", Enter: false})
	time.Sleep(20 * time.Millisecond)

	h.mu.Lock()
	in2 := string(h.inputs["s1"])
	h.inputs["s1"] = nil
	h.mu.Unlock()
	want2 := "\x1b[200~a\nb\x1b[201~"
	if in2 != want2 {
		t.Fatalf("expected bracketed paste %q, got %q", want2, in2)
	}

	// 3. "a\nb" without bracketed paste
	// Disable bracketed paste: \x1b[?2004l
	_, _ = term.Write([]byte("\x1b[?2004l"))
	client.writeMsg(t, wText, TextMsg{Text: "a\nb", Enter: false})
	time.Sleep(20 * time.Millisecond)

	h.mu.Lock()
	in3 := string(h.inputs["s1"])
	h.inputs["s1"] = nil
	h.mu.Unlock()
	want3 := "a\rb"
	if in3 != want3 {
		t.Fatalf("expected 'a\\rb', got %q", in3)
	}

	// 4. Emoji kept as UTF-8
	client.writeMsg(t, wText, TextMsg{Text: "🎉", Enter: true})
	time.Sleep(20 * time.Millisecond)

	h.mu.Lock()
	in4 := string(h.inputs["s1"])
	h.mu.Unlock()
	want4 := "🎉\r"
	if in4 != want4 {
		t.Fatalf("expected emoji %q, got %q", want4, in4)
	}
}

func TestKeysRejectsUnknown(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// Send Keys{["f13"]}
	client.writeMsg(t, wKeys, KeysMsg{Names: []string{"f13"}})

	typ, _ := client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError for unknown key f13, got %d", typ)
	}

	// Verify nothing was written to host terminal
	h.mu.Lock()
	inputs := h.inputs["s1"]
	h.mu.Unlock()
	if len(inputs) != 0 {
		t.Fatalf("expected 0 input written to host, got %q", string(inputs))
	}
}

func TestWatchedSessionExits(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	h.mu.Lock()
	watcher := h.watchers["s1"]
	h.mu.Unlock()
	if watcher == nil {
		t.Fatal("expected watcher to be registered")
	}

	// Host calls Exited()
	watcher.Exited()

	// Client gets Exited
	typ, _ := client.readMsg(t)
	if typ != wExited {
		t.Fatalf("expected wExited, got %d", typ)
	}

	// Host unwatch was called
	time.Sleep(20 * time.Millisecond)
	h.mu.Lock()
	unwatched := h.unwatched["s1"]
	h.mu.Unlock()
	if !unwatched {
		t.Fatal("expected host unwatch to have been called")
	}
}

func TestPairRejectFlow(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"
	now := time.Now()

	pairing := r.openPairing(now)
	tok := pairing.Token()

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)

	_, pubSPKI := makeTestP256Key(t)
	credID := []byte("cred-pair-reject")
	cd := makeTestClientDataJSON(t, "webauthn.create", origin, ch.Nonce)
	ad := makeTestRegistrationAuthData(t, origin, 0x01|0x04|0x40, 1, credID)
	_, sessPub := makeTestP256Key(t)

	client.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     credID,
			PubKey:     pubSPKI,
			AuthData:   ad,
			ClientData: cd,
			Name:       "Reject Phone",
		},
		SessionPub: sessPub,
	})

	typ, _ = client.readMsg(t)
	if typ != wPairCode {
		t.Fatalf("expected wPairCode, got %d", typ)
	}

	// Desktop rejects
	r.decidePairing(false)

	typ, _ = client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError on desktop reject, got %d", typ)
	}

	// Nothing stored
	if len(r.devices.List()) != 0 {
		t.Fatal("expected 0 devices stored after desktop reject")
	}
}

func TestPairNoPairingOpen(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)

	_, pubSPKI := makeTestP256Key(t)
	cd := makeTestClientDataJSON(t, "webauthn.create", origin, ch.Nonce)
	ad := makeTestAuthData(t, origin, 0x01|0x04, 1)
	_, sessPub := makeTestP256Key(t)

	// Send Pair with no pairing open
	client.writeMsg(t, wPair, PairMsg{
		Token: []byte("random-token-123"),
		Reg: RegistrationWire{
			CredID:     []byte("cred-pair-nopair"),
			PubKey:     pubSPKI,
			AuthData:   ad,
			ClientData: cd,
			Name:       "No Pairing Phone",
		},
		SessionPub: sessPub,
	})

	typ, _ = client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError when no pairing open, got %d", typ)
	}

	if len(r.devices.List()) != 0 {
		t.Fatal("expected 0 devices stored when no pairing open")
	}
}

func TestPairDesktopDecisionPendingOnly(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"
	now := time.Now()

	pairing := r.openPairing(now)
	tok := pairing.Token()

	// Decide before Pair arrives: must return false and be ignored!
	if ok := r.decidePairing(true); ok {
		t.Fatal("decidePairing should return false when no connection is pending")
	}

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)

	_, pubSPKI := makeTestP256Key(t)
	credID := []byte("cred-pair-pending")
	cd := makeTestClientDataJSON(t, "webauthn.create", origin, ch.Nonce)
	ad := makeTestRegistrationAuthData(t, origin, 0x01|0x04|0x40, 1, credID)
	_, sessPub := makeTestP256Key(t)

	client.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     credID,
			PubKey:     pubSPKI,
			AuthData:   ad,
			ClientData: cd,
			Name:       "Pending Phone",
		},
		SessionPub: sessPub,
	})

	// Client gets PairCode
	typ, _ = client.readMsg(t)
	if typ != wPairCode {
		t.Fatalf("expected wPairCode, got %d", typ)
	}

	// Should still be waiting (no Authed received yet)
	select {
	case <-time.After(50 * time.Millisecond):
		// Expected: still waiting
	}

	// Now desktop decides: must return true
	if ok := r.decidePairing(true); !ok {
		t.Fatal("decidePairing should return true when connection is waiting")
	}

	// Client now receives Authed
	typ, _ = client.readMsg(t)
	if typ != wAuthed {
		t.Fatalf("expected wAuthed after decision, got %d", typ)
	}
}

func TestPairClientDisconnectWhileWaiting(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"
	now := time.Now()

	pairing := r.openPairing(now)
	tok := pairing.Token()

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)

	_, pubSPKI := makeTestP256Key(t)
	credID := []byte("cred-pair-dc")
	cd := makeTestClientDataJSON(t, "webauthn.create", origin, ch.Nonce)
	ad := makeTestRegistrationAuthData(t, origin, 0x01|0x04|0x40, 1, credID)
	_, sessPub := makeTestP256Key(t)

	client.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     credID,
			PubKey:     pubSPKI,
			AuthData:   ad,
			ClientData: cd,
			Name:       "DC Phone",
		},
		SessionPub: sessPub,
	})

	typ, _ = client.readMsg(t)
	if typ != wPairCode {
		t.Fatalf("expected wPairCode, got %d", typ)
	}

	// Phone closes connection while waiting
	_ = cRw.Close()
	time.Sleep(30 * time.Millisecond)

	// Desktop confirms later
	_ = r.decidePairing(true)
	time.Sleep(100 * time.Millisecond)

	// Device must NOT be stored
	if len(r.devices.List()) != 0 {
		t.Fatal("device was stored despite phone disconnecting while waiting")
	}
}

func TestPairExpiryWhileWaiting(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"
	// Token expires in 100ms
	now := time.Now().Add(-1*time.Minute - 59*time.Second - 900*time.Millisecond)

	pairing := r.openPairing(now)
	tok := pairing.Token()

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)

	_, pubSPKI := makeTestP256Key(t)
	credID := []byte("cred-pair-expwait")
	cd := makeTestClientDataJSON(t, "webauthn.create", origin, ch.Nonce)
	ad := makeTestRegistrationAuthData(t, origin, 0x01|0x04|0x40, 1, credID)
	_, sessPub := makeTestP256Key(t)

	client.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     credID,
			PubKey:     pubSPKI,
			AuthData:   ad,
			ClientData: cd,
			Name:       "Exp Wait Phone",
		},
		SessionPub: sessPub,
	})

	typ, _ = client.readMsg(t)
	if typ != wPairCode {
		t.Fatalf("expected wPairCode, got %d", typ)
	}

	// Wait for pairing to expire
	time.Sleep(150 * time.Millisecond)

	// Desktop confirms after expiry
	_ = r.decidePairing(true)

	// Client should receive wError
	typ, _ = client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError on pairing expiry, got %d", typ)
	}

	if len(r.devices.List()) != 0 {
		t.Fatal("device was stored despite pairing expiring while waiting")
	}
}

func TestSizeClampedAndRefused(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// 1. Refuse cols <= 0 with Error
	client.writeMsg(t, wSize, SizeMsg{Cols: 0, Rows: 24})
	typ, _ := client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError for Cols=0, got %d", typ)
	}

	// 2. Refuse rows <= 0 with Error
	client.writeMsg(t, wSize, SizeMsg{Cols: 80, Rows: -5})
	typ, _ = client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError for Rows=-5, got %d", typ)
	}

	// 3. Clamp huge size to 500x300
	client.writeMsg(t, wSize, SizeMsg{Cols: 100000, Rows: 100000})
	time.Sleep(30 * time.Millisecond)

	h.mu.Lock()
	size := h.sizes["s1"]
	h.mu.Unlock()
	if size[0] != 500 || size[1] != 300 {
		t.Fatalf("expected clamped size [500, 300], got %v", size)
	}
}

func TestCancelContextClosesConnectionAndUnwatches(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithCancel(context.Background())

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	}()

	authenticateClient(t, client, r, origin)
	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// Trigger changed to send another frame, but client never reads!
	_, _ = term.Write([]byte("lots of output to fill frame"))
	h.mu.Lock()
	w := h.watchers["s1"]
	h.mu.Unlock()
	w.Changed()
	time.Sleep(50 * time.Millisecond) // Wait for frameTimer to trigger writeFrame which blocks on pipe

	// Cancel ctx while client never reads
	start := time.Now()
	cancel()

	select {
	case <-serveDone:
		if time.Since(start) > time.Second {
			t.Fatalf("serveConn took longer than 1s to return on cancel: %v", time.Since(start))
		}
	case <-time.After(time.Second):
		t.Fatal("serveConn did not return within 1s after context cancel")
	}

	h.mu.Lock()
	unwatched := h.unwatched["s1"]
	h.mu.Unlock()
	if !unwatched {
		t.Fatal("expected host session to be unwatched on cancel")
	}
}

func TestResumeRevokedDeviceRefused(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sessionKey, sessionPubSPKI := makeTestP256Key(t)
	devID := "dev-revoked"
	credID := []byte("cred-revoked")
	_ = r.devices.Add(Device{
		ID:     devID,
		Name:   "phone",
		CredID: credID,
		PubKey: sessionPubSPKI,
		Origin: origin,
	})

	t0 := time.Now()
	resumeID := r.resumes.Issue(devID, sessionPubSPKI, t0)
	r.resumes.Disconnected(devID, t0)

	// Revoke device from devices store
	if err := r.devices.Remove(devID); err != nil {
		t.Fatalf("failed to remove device: %v", err)
	}

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)

	sig := signResumeChallenge(t, sessionKey, ch.Nonce)
	client.writeMsg(t, wResume, ResumeMsg{ID: resumeID, Sig: sig})

	typ, _ = client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError for revoked device resume, got %d", typ)
	}
}

func TestBracketedPasteSanitizesEscape(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// Enable bracketed paste
	_, _ = term.Write([]byte("\x1b[?2004h"))

	// Send text containing injected ESC[201~
	client.writeMsg(t, wText, TextMsg{Text: "a\x1b[201~evil\nb", Enter: false})
	time.Sleep(20 * time.Millisecond)

	h.mu.Lock()
	in := string(h.inputs["s1"])
	h.mu.Unlock()

	want := "\x1b[200~aevil\nb\x1b[201~"
	if in != want {
		t.Fatalf("expected sanitized bracketed paste %q, got %q", want, in)
	}
}

func TestKeysRejectsUppercaseCtrl(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// Send ctrl+A (uppercase) -> must be rejected
	client.writeMsg(t, wKeys, KeysMsg{Names: []string{"ctrl+A"}})
	typ, _ := client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError for uppercase ctrl+A, got %d", typ)
	}
}

func TestKeysEmptyIgnored(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// Send empty keys list
	client.writeMsg(t, wKeys, KeysMsg{Names: []string{}})
	time.Sleep(30 * time.Millisecond)

	h.mu.Lock()
	calls := h.inputCalls
	h.mu.Unlock()
	if calls != 0 {
		t.Fatalf("expected 0 input calls on host for empty keys, got %d", calls)
	}
}

func TestWatchUnknownKeyClearsWatchedKey(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// Now watch unknown key
	client.writeMsg(t, wWatch, WatchMsg{Key: "unknown"})
	typ, _ := client.readMsg(t)
	if typ != wError {
		t.Fatalf("expected wError for unknown watch, got %d", typ)
	}

	// Host s1 must have been unwatched
	h.mu.Lock()
	unwatched := h.unwatched["s1"]
	h.mu.Unlock()
	if !unwatched {
		t.Fatal("expected s1 to be unwatched")
	}

	// Sending Text should NOT send input to old watchedKey s1
	client.writeMsg(t, wText, TextMsg{Text: "after error", Enter: false})
	time.Sleep(30 * time.Millisecond)

	h.mu.Lock()
	calls := h.inputCalls
	h.mu.Unlock()
	if calls != 0 {
		t.Fatalf("input was sent to old watched session after failed watch: %d calls", calls)
	}
}

func TestTextScrollResetTriggersFullFrame(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	for i := 0; i < 30; i++ {
		_, _ = term.Write([]byte("line\r\n"))
	}
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// Scroll to 5
	client.writeMsg(t, wScroll, ScrollMsg{Offset: 5})
	typ, _ := client.readMsg(t)
	if typ != wFrame {
		t.Fatalf("expected wFrame, got %d", typ)
	}

	// Send Text: must reset scroll to 0 AND trigger a full frame!
	client.writeMsg(t, wText, TextMsg{Text: "a", Enter: false})
	typ, payload := client.readMsg(t)
	if typ != wFrame {
		t.Fatalf("expected wFrame after text reset scroll, got %d", typ)
	}
	var f FrameMsg
	_ = json.Unmarshal(payload, &f)
	if !f.Full || f.Offset != 0 {
		t.Fatalf("expected full frame with offset 0, got full=%v offset=%d", f.Full, f.Offset)
	}
}

func TestScrollClamp(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	for i := 0; i < 30; i++ {
		_, _ = term.Write([]byte("line\r\n"))
	}
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	_, _ = client.readMsg(t) // Theme
	_, _ = client.readMsg(t) // Frame

	// Negative scroll clamped to 0
	client.writeMsg(t, wScroll, ScrollMsg{Offset: -10})
	typ, payload := client.readMsg(t)
	if typ != wFrame {
		t.Fatalf("expected wFrame, got %d", typ)
	}
	var f FrameMsg
	_ = json.Unmarshal(payload, &f)
	if f.Offset != 0 {
		t.Fatalf("expected offset 0, got %d", f.Offset)
	}

	// Huge scroll clamped to Hist
	client.writeMsg(t, wScroll, ScrollMsg{Offset: 9999})
	typ, payload = client.readMsg(t)
	if typ != wFrame {
		t.Fatalf("expected wFrame, got %d", typ)
	}
	_ = json.Unmarshal(payload, &f)
	if f.Offset != f.Hist {
		t.Fatalf("expected offset %d, got %d", f.Hist, f.Offset)
	}
}

