package remote

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net"
	"net/netip"
	"testing"
	"time"
)

// A pairing waiting for the desktop outlives the handshake deadline: only
// the pairing expiry bounds it.
func TestPairWaitOutlivesAuthTimeout(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	r.authTimeout = 200 * time.Millisecond
	origin := "https://host.ts.net"
	now := time.Now()
	tok := r.openPairing(now).Token()

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))

	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	_ = json.Unmarshal(payload, &ch)
	_, pubSPKI := makeTestP256Key(t)
	credID := []byte("cred-wait")
	_, sessPub := makeTestP256Key(t)
	client.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     credID,
			PubKey:     pubSPKI,
			AuthData:   makeTestRegistrationAuthData(t, origin, 0x01|0x04|0x40, 1, credID),
			ClientData: makeTestClientDataJSON(t, "webauthn.create", origin, ch.Nonce),
			Name:       "phone",
		},
		SessionPub: sessPub,
	})
	typ, payload = client.readMsg(t)
	if typ != wPairCode {
		t.Fatalf("expected wPairCode, got %d (%s)", typ, payload)
	}
	cert := r.certs.Current(now)
	var pc PairCodeMsg
	_ = json.Unmarshal(payload, &pc)
	if pc.Code != SASCode(tok, sha256.Sum256(cert.Certificate[0]), pubSPKI) {
		t.Fatalf("unexpected code %q", pc.Code)
	}

	// The desktop answers after three handshake deadlines.
	time.Sleep(3 * r.authTimeout)
	if !r.decidePairing(true) {
		t.Fatal("pairing no longer waiting: the connection was closed")
	}
	typ, payload = client.readMsg(t)
	if typ != wAuthed {
		t.Fatalf("expected wAuthed, got %d (%s)", typ, payload)
	}
}

// A connection that never authenticates is still closed at the deadline.
func TestAuthTimeoutStillCloses(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	r.authTimeout = 200 * time.Millisecond
	r.openPairing(time.Now())

	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}
	done := make(chan struct{})
	go func() {
		r.serveConn(context.Background(), sRw, "https://host.ts.net", netip.MustParseAddr("192.168.1.100"))
		close(done)
	}()
	if typ, _ := client.readMsg(t); typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connection without Pair not closed at the auth deadline")
	}
}
