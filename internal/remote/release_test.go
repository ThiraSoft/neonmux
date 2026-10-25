package remote

import (
	"context"
	"strings"
	"testing"
	"time"
)

func (l *limiter) counts() (authed, quic, pre int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.authed, l.quicAll, l.preTotal
}

// A phone that closes its session (home screen, then the machine again, in
// a loop) frees its slots at once, not at the QUIC idle timeout: opening
// the same machine ten times in a row is never refused.
func TestClosedConnectionsFreeSlots(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	d := addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		sess, _ := authOK(t, r, d)
		if a, _, _ := r.limit.counts(); a != 1 {
			t.Fatalf("round %d: %d authenticated connections, want 1", i, a)
		}
		// As the PWA's wt.close(): the session goes, the client keeps its
		// QUIC connection for the browser to reuse or time out.
		_ = sess.CloseWithError(0, "")
		waitFor(t, "slots freed", 2*time.Second, func() bool {
			a, q, p := r.limit.counts()
			return a == 0 && q == 0 && p == 0
		})
	}
	for _, l := range r.Log(20) {
		if !hasSuffix(l, " ok") {
			t.Fatalf("log line %q: every connection should be ok", l)
		}
	}
}

func hasSuffix(s, suf string) bool { return len(s) >= len(suf) && s[len(s)-len(suf):] == suf }

// Safari closes a WebTransport session late: the daemon log of the iPhone
// test shows its stream reset about a minute after the app went home. A
// phone opening the same machine again meanwhile is the same device,
// proven again: its older connection makes way, so it is not refused as
// busy after four tries (maxAuthed stays 4 for different phones).
func TestReopenedPhoneReplacesItsConnection(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	d := addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		sess, _ := authOK(t, r, d) // the previous one never closed
		t.Cleanup(func() { _ = sess.CloseWithError(0, "") })
		waitFor(t, "one connection left", 2*time.Second, func() bool {
			a, q, p := r.limit.counts()
			return a == 1 && q == 1 && p == 0
		})
		if i > 0 && len(r.State().Connected) != 1 {
			t.Fatalf("round %d: connected %v", i, r.State().Connected)
		}
	}
	for _, l := range r.Log(20) {
		if !hasSuffix(l, " ok") {
			t.Fatalf("log line %q: every connection should be ok", l)
		}
	}
}

// Different phones still share maxAuthed: the fifth is busy, and the log
// says which limit fired.
func TestBusyIsLoggedAsBusy(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	for i := range maxAuthed {
		d := addTestDevice(t, r, "d"+string(rune('a'+i)))
		sess, _ := authOK(t, r, d)
		t.Cleanup(func() { _ = sess.CloseWithError(0, "") })
	}
	d := addTestDevice(t, r, "dz")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, str, err := dialRemote(ctx, listenAddr(t, r), currentHash(r), testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.CloseWithError(0, "")
	ch := readChallenge(t, str)
	if err := writeFrame(str, wAuth, d.auth(t, ch.Nonce)); err != nil {
		t.Fatal(err)
	}
	typ, payload, _ := readFrame(str)
	if typ != wError || !strings.Contains(string(payload), "busy") {
		t.Fatalf("fifth phone: %d %s, want Error busy", typ, payload)
	}
	waitFor(t, "ratelimited busy line", 3*time.Second, func() bool {
		l := r.Log(1)
		return len(l) == 1 && strings.HasSuffix(l[0], " ratelimited busy")
	})
}
