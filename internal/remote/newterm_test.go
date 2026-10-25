package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

// addTerm makes the fake host's NewTerm open sessions n1, n2…
func addTerm(h *fakeHost) func(cols, rows int) (string, error) {
	return func(cols, rows int) (string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		key := fmt.Sprintf("n%d", len(h.sessions))
		h.terms[key] = vt.New(cols, rows, 100)
		h.sessions = append(h.sessions, SessionInfo{Key: key, Name: "zsh"})
		return key, nil
	}
}

func startAuthed(t *testing.T, h *fakeHost) (*Remote, *testClient) {
	t.Helper()
	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"
	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)
	return r, client
}

func errMsgOf(t *testing.T, payload []byte) string {
	t.Helper()
	var em ErrorMsg
	if err := json.Unmarshal(payload, &em); err != nil {
		t.Fatal(err)
	}
	return em.Msg
}

// NewTerm opens a terminal at the phone's size; the phone gets the list
// with it, then its key.
func TestNewTermFromPhone(t *testing.T) {
	h := newFakeHost()
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}
	h.terms["s1"] = vt.New(80, 24, 100)
	h.newTerm = addTerm(h)
	_, client := startAuthed(t, h)

	client.writeMsg(t, wSize, SizeMsg{Cols: 100, Rows: 30})
	client.writeMsg(t, wNewTerm, nil)
	var list SessionsMsg
	if err := json.Unmarshal(readUntil(t, client, wSessions, wNewTermDone, wError), &list); err != nil {
		t.Fatal(err)
	}
	var done NewTermDoneMsg
	if err := json.Unmarshal(readUntil(t, client, wNewTermDone, wError), &done); err != nil {
		t.Fatal(err)
	}
	if done.Key != "n1" {
		t.Fatalf("NewTermDone key %q, want n1", done.Key)
	}
	found := false
	for _, s := range list.List {
		found = found || s.Key == done.Key
	}
	if !found {
		t.Fatalf("Sessions before NewTermDone lacks %s: %+v", done.Key, list.List)
	}
	h.mu.Lock()
	sizes := h.newTerms
	h.mu.Unlock()
	if len(sizes) != 1 || sizes[0] != [2]int{100, 30} {
		t.Fatalf("NewTerm sizes %v, want [[100 30]]", sizes)
	}

	// The phone watches it like any other.
	client.writeMsg(t, wWatch, WatchMsg{Key: done.Key})
	readUntil(t, client, wFrame, wError)

	// The host fails: an error, no key.
	h.mu.Lock()
	h.newTerm = nil
	h.mu.Unlock()
	client.writeMsg(t, wNewTerm, nil)
	if msg := errMsgOf(t, readUntil(t, client, wError, wNewTermDone)); msg != "new terminal: failed" {
		t.Fatalf("error %q", msg)
	}
}

// One creation at a time per phone, and at most newTermMax a minute: an
// authenticated phone cannot flood the machine with shells.
func TestNewTermLimits(t *testing.T) {
	h := newFakeHost()
	release := make(chan struct{})
	open := addTerm(h)
	h.newTerm = func(cols, rows int) (string, error) {
		<-release
		return open(cols, rows)
	}
	_, client := startAuthed(t, h)

	client.writeMsg(t, wNewTerm, nil)
	client.writeMsg(t, wNewTerm, nil)
	if msg := errMsgOf(t, readUntil(t, client, wError, wNewTermDone)); msg != "new terminal: busy" {
		t.Fatalf("second NewTerm while one is pending: %q", msg)
	}
	close(release)
	readUntil(t, client, wNewTermDone, wError)
	for i := 1; i < newTermMax; i++ {
		client.writeMsg(t, wNewTerm, nil)
		readUntil(t, client, wNewTermDone, wError)
	}
	client.writeMsg(t, wNewTerm, nil)
	if msg := errMsgOf(t, readUntil(t, client, wError, wNewTermDone)); msg != "new terminal: too many" {
		t.Fatalf("NewTerm past the cap: %q", msg)
	}
	h.mu.Lock()
	n := len(h.newTerms)
	h.mu.Unlock()
	if n != newTermMax {
		t.Fatalf("host asked for %d terminals, want %d", n, newTermMax)
	}
}

// The limits are per device, over a sliding minute, whatever connection
// asks.
func TestNewTermLimiter(t *testing.T) {
	var l newTerms
	t0 := time.Now()
	if err := l.start("a", t0); err != nil {
		t.Fatal(err)
	}
	if err := l.start("a", t0); err != errNewTermBusy {
		t.Fatalf("pending: %v", err)
	}
	if err := l.start("b", t0); err != nil {
		t.Fatalf("another device: %v", err)
	}
	l.end("a")
	for i := 1; i < newTermMax; i++ {
		if err := l.start("a", t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("creation %d: %v", i+1, err)
		}
		l.end("a")
	}
	if err := l.start("a", t0.Add(30*time.Second)); err != errNewTermTooMany {
		t.Fatalf("past the cap: %v", err)
	}
	if err := l.start("a", t0.Add(newTermWindow+time.Millisecond)); err != nil {
		t.Fatalf("a minute after the first: %v", err)
	}
}

// Wheel, SetTheme and NewTerm are refused before authentication like the
// rest: the connection closes and the host is never called.
func TestNewMessagesNeedAuth(t *testing.T) {
	for _, m := range []struct {
		typ     uint8
		payload any
	}{
		{wWheel, WheelMsg{Steps: 1}},
		{wSetTheme, SetThemeMsg{Name: "synthwave"}},
		{wNewTerm, nil},
	} {
		h := newFakeHost()
		h.newTerm = addTerm(h)
		r, _ := setupTestRemote(t, h)
		sRw, cRw := net.Pipe()
		client := &testClient{rw: cRw}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		go r.serveConn(ctx, sRw, "https://host.ts.net", netip.MustParseAddr("192.168.1.100"))
		if typ, _ := client.readMsg(t); typ != wChallenge {
			t.Fatalf("expected Challenge, got %d", typ)
		}
		client.writeMsg(t, m.typ, m.payload)
		_, _, err := readFrame(cRw)
		if err == nil {
			_, _, err = readFrame(cRw)
		}
		if err == nil {
			t.Fatalf("type %d before auth: connection still open", m.typ)
		}
		h.mu.Lock()
		calls := h.calls
		h.mu.Unlock()
		if calls != 0 {
			t.Fatalf("type %d before auth: %d host calls", m.typ, calls)
		}
		cancel()
	}
}
