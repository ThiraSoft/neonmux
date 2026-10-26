package remote

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"sync"
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

// With no pairing open, a connection that never authenticates is still
// closed at the deadline.
func TestAuthTimeoutStillCloses(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	r.authTimeout = 200 * time.Millisecond

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

// runConn serves one connection on a pipe and returns the client side and
// a channel with the result.
func runConn(t *testing.T, r *Remote, origin string) (*testClient, chan connResult) {
	t.Helper()
	sRw, cRw := net.Pipe()
	t.Cleanup(func() { cRw.Close() })
	res := make(chan connResult, 1)
	go func() { res <- r.serveConn(context.Background(), sRw, origin, netip.MustParseAddr("192.168.1.100")) }()
	client := &testClient{rw: cRw}
	if typ, _ := client.readMsg(t); typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	// Read what the server writes (an Error) so it never blocks on the pipe.
	go func() { _, _ = io.Copy(io.Discard, cRw) }()
	return client, res
}

func waitResult(t *testing.T, res chan connResult, within time.Duration) (connResult, time.Duration) {
	t.Helper()
	t0 := time.Now()
	select {
	case r := <-res:
		return r, time.Since(t0)
	case <-time.After(within):
		t.Fatalf("connection still open after %v", within)
	}
	return connResult{}, 0
}

// An open pairing does not lengthen the handshake deadline: only a
// connection whose Pair token was accepted waits past it.
func TestOpenPairingKeepsAuthTimeout(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	r.authTimeout = 200 * time.Millisecond
	r.openPairing(time.Now())
	if d := r.preAuthDeadline(); d != 200*time.Millisecond {
		t.Fatalf("deadline %v under an open pairing, want the plain 200ms", d)
	}
	_, res := runConn(t, r, "https://host.ts.net")
	got, took := waitResult(t, res, 2*time.Second)
	if took > time.Second || got.reason != "timeout" {
		t.Fatalf("closed after %v with reason %q", took, got.reason)
	}
}

// The phone connects at the QR scan and may spend more than the deadline
// typing a name and creating the passkey: its connection is cut, the PWA
// opens a new one and sends Pair there, signed over the first connection's
// nonce. The pairing succeeds, for a new phone and for a machine paired
// again from an origin that already has a device.
func TestPairOnNewConnAfterAuthTimeout(t *testing.T) {
	for _, repair := range []bool{false, true} {
		name := "new phone"
		if repair {
			name = "paired again"
		}
		t.Run(name, func(t *testing.T) {
			h := newFakeHost()
			r, _ := setupTestRemote(t, h)
			r.authTimeout = 200 * time.Millisecond
			origin := "https://host.ts.net"
			if repair {
				_, oldPub := makeTestP256Key(t)
				if err := r.devices.Add(Device{ID: "d1", Name: "phone", CredID: []byte("c1"), PubKey: oldPub, Origin: origin, Added: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			tok := r.openPairing(time.Now()).Token()

			// The connection opened at the scan: Challenge, Hello, then silence.
			sRw, cRw := net.Pipe()
			defer cRw.Close()
			first := &testClient{rw: cRw}
			res := make(chan connResult, 1)
			go func() { res <- r.serveConn(context.Background(), sRw, origin, netip.MustParseAddr("192.168.1.100")) }()
			typ, payload := first.readMsg(t)
			if typ != wChallenge {
				t.Fatalf("expected wChallenge, got %d", typ)
			}
			var ch ChallengeMsg
			_ = json.Unmarshal(payload, &ch)
			first.writeMsg(t, wHello, HelloMsg{Proto: 1})
			go func() { _, _ = io.Copy(io.Discard, cRw) }()
			if got, took := waitResult(t, res, 2*time.Second); took > time.Second || got.reason != "timeout" {
				t.Fatalf("scan connection closed after %v with reason %q, want the deadline", took, got.reason)
			}

			// The passkey, made over the dead connection's nonce, goes on a new one.
			sRw2, cRw2 := net.Pipe()
			defer cRw2.Close()
			client := &testClient{rw: cRw2}
			go r.serveConn(context.Background(), sRw2, origin, netip.MustParseAddr("192.168.1.100"))
			if typ, _ := client.readMsg(t); typ != wChallenge {
				t.Fatalf("expected wChallenge, got %d", typ)
			}
			client.writeMsg(t, wHello, HelloMsg{Proto: 1})
			_, pubSPKI := makeTestP256Key(t)
			credID := []byte("cred-slow")
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
			if typ, payload = client.readMsg(t); typ != wPairCode {
				t.Fatalf("expected wPairCode, got %d (%s)", typ, payload)
			}
			// The desktop takes longer than the deadline: the accepted token holds the wait.
			time.Sleep(3 * r.authTimeout)
			if !r.decidePairing(true) {
				t.Fatal("pairing no longer waiting: the connection was closed")
			}
			if typ, payload = client.readMsg(t); typ != wAuthed {
				t.Fatalf("expected wAuthed, got %d (%s)", typ, payload)
			}
		})
	}
}

// With no pairing open, a paired device's connection has the plain
// deadline.
func TestNoPairingKeepsAuthTimeout(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	r.authTimeout = 200 * time.Millisecond
	if d := r.preAuthDeadline(); d != 200*time.Millisecond {
		t.Fatalf("deadline %v, want the plain 200ms", d)
	}
	_, res := runConn(t, r, "https://host.ts.net")
	if got, took := waitResult(t, res, 2*time.Second); took > time.Second || got.reason != "timeout" {
		t.Fatalf("closed after %v with reason %q", took, got.reason)
	}
}

// Every refused handshake says why, for the log.
func TestRefusedReasons(t *testing.T) {
	h := newFakeHost()
	r, _ := setupTestRemote(t, h)
	r.authTimeout = 200 * time.Millisecond
	origin := "https://host.ts.net"

	client, res := runConn(t, r, origin)
	client.rw.(io.Closer).Close()
	if got, _ := waitResult(t, res, 2*time.Second); got.outcome != outcomeRefused || got.reason != "closed" {
		t.Fatalf("client gone: %+v", got)
	}

	client, res = runConn(t, r, origin)
	client.writeMsg(t, wAuth, AuthMsg{Assertion: AssertionWire{CredID: []byte("nobody")}})
	if got, _ := waitResult(t, res, 2*time.Second); got.reason != "auth" {
		t.Fatalf("unknown credential: %+v", got)
	}

	client, res = runConn(t, r, origin)
	client.writeMsg(t, wHello, HelloMsg{Proto: 99})
	if got, _ := waitResult(t, res, 2*time.Second); got.reason != "proto" {
		t.Fatalf("other proto: %+v", got)
	}

	r.openPairing(time.Now())
	client, res = runConn(t, r, origin)
	client.writeMsg(t, wPair, PairMsg{Token: make([]byte, 16)})
	if got, _ := waitResult(t, res, 2*time.Second); got.reason != "pair" {
		t.Fatalf("wrong token: %+v", got)
	}

	r.closePairing()
	_, res = runConn(t, r, origin)
	if got, _ := waitResult(t, res, 2*time.Second); got.reason != "timeout" {
		t.Fatalf("silent client: %+v", got)
	}
}

// Between the desktop decision and the new device, State still shows the
// pairing with its code, so the window never sees a pairing that vanished
// without a device.
func TestConfirmedPairingStaysInStateUntilDeviceAdded(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	ps, err := r.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	r.pairMu.Lock()
	tok := r.pairing.Token()
	r.pairMu.Unlock()

	client, nonce := dialPipe(t, r)
	_, pub := makeTestP256Key(t)
	credID := []byte("cred-decided")
	_, sessPub := makeTestP256Key(t)
	client.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     credID,
			PubKey:     pub,
			AuthData:   makeTestRegistrationAuthData(t, nonceOrigin, 0x01|0x04|0x40, 1, credID),
			ClientData: makeTestClientDataJSON(t, "webauthn.create", nonceOrigin, nonce),
			Name:       "phone",
		},
		SessionPub: sessPub,
	})
	expect(t, client, wPairCode, "Pair")
	code := r.State().Pairing.Code

	var during State
	var second bool
	hooked := make(chan struct{})
	r.pairDecided = func() {
		during = r.State()
		second = r.decidePairing(false)
		close(hooked)
	}
	if !r.decidePairing(true) {
		t.Fatal("decision refused")
	}
	<-hooked
	if second {
		t.Error("a second decision was accepted")
	}
	if during.Pairing == nil || during.Pairing.Code != code || during.Pairing.Payload != ps.Payload {
		t.Errorf("State between the decision and the device: Pairing = %+v, want the code %s", during.Pairing, code)
	}
	if len(during.Devices) != 0 {
		t.Errorf("device added before the hook: %+v", during.Devices)
	}
	expect(t, client, wAuthed, "Pair confirmed")
	after := r.State()
	if after.Pairing != nil || len(after.Devices) != 1 {
		t.Errorf("after pairing: Pairing = %+v, devices = %d", after.Pairing, len(after.Devices))
	}
}

// A refused decision also keeps the code shown until serveConn ends it.
func TestRefusedPairingEndsInState(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	if _, err := r.StartPairing(); err != nil {
		t.Fatal(err)
	}
	r.pairMu.Lock()
	tok := r.pairing.Token()
	r.pairMu.Unlock()
	client, nonce := dialPipe(t, r)
	_, pub := makeTestP256Key(t)
	credID := []byte("cred-refused")
	_, sessPub := makeTestP256Key(t)
	client.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     credID,
			PubKey:     pub,
			AuthData:   makeTestRegistrationAuthData(t, nonceOrigin, 0x01|0x04|0x40, 1, credID),
			ClientData: makeTestClientDataJSON(t, "webauthn.create", nonceOrigin, nonce),
			Name:       "phone",
		},
		SessionPub: sessPub,
	})
	expect(t, client, wPairCode, "Pair")
	if !r.decidePairing(false) {
		t.Fatal("decision refused")
	}
	expect(t, client, wError, "Pair refused")
	waitFor(t, "pairing gone from State", 2*time.Second, func() bool { return r.State().Pairing == nil })
	if n := len(r.State().Devices); n != 0 {
		t.Fatalf("%d devices after a refusal", n)
	}
}

// A confirmed pairing is announced as ended while the phone stays
// connected: the window closes its pairing screen on that push.
func TestConfirmedPairingNotifiesEnd(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	if _, err := r.StartPairing(); err != nil {
		t.Fatal(err)
	}
	r.pairMu.Lock()
	tok := r.pairing.Token()
	r.pairMu.Unlock()

	client, nonce := dialPipe(t, r)
	_, pub := makeTestP256Key(t)
	credID := []byte("cred-notify")
	_, sessPub := makeTestP256Key(t)
	client.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     credID,
			PubKey:     pub,
			AuthData:   makeTestRegistrationAuthData(t, nonceOrigin, 0x01|0x04|0x40, 1, credID),
			ClientData: makeTestClientDataJSON(t, "webauthn.create", nonceOrigin, nonce),
			Name:       "phone",
		},
		SessionPub: sessPub,
	})
	expect(t, client, wPairCode, "Pair")

	ended := make(chan struct{})
	var once sync.Once
	r.OnChange(func() {
		if st := r.State(); st.Pairing == nil && len(st.Devices) == 1 {
			once.Do(func() { close(ended) })
		}
	})
	r.ConfirmPairing(true)
	expect(t, client, wAuthed, "Pair confirmed")
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("no change announced the end of the pairing while the phone is connected")
	}
}
