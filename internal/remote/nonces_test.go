package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/netip"
	"testing"
	"time"
)

const nonceOrigin = "https://host.ts.net"

// dialPipe starts a connection and returns its client and Challenge nonce.
func dialPipe(t *testing.T, r *Remote) (*testClient, []byte) {
	t.Helper()
	sRw, cRw := net.Pipe()
	t.Cleanup(func() { cRw.Close() })
	go r.serveConn(context.Background(), sRw, nonceOrigin, netip.MustParseAddr("192.168.1.100"))
	client := &testClient{rw: cRw}
	typ, payload := client.readMsg(t)
	if typ != wChallenge {
		t.Fatalf("expected wChallenge, got %d", typ)
	}
	var ch ChallengeMsg
	if err := json.Unmarshal(payload, &ch); err != nil {
		t.Fatal(err)
	}
	return client, ch.Nonce
}

// abandoned is the nonce of a connection that died before it could answer
// (the phone was in the Face ID sheet).
func abandoned(t *testing.T, r *Remote) []byte {
	t.Helper()
	client, nonce := dialPipe(t, r)
	client.rw.Close()
	return nonce
}

type nonceDevice struct {
	key    *ecdsa.PrivateKey
	credID []byte
}

func addNonceDevice(t *testing.T, r *Remote) nonceDevice {
	t.Helper()
	key, pub := makeTestP256Key(t)
	d := nonceDevice{key: key, credID: []byte("cred-nonce")}
	if err := r.devices.Add(Device{ID: "dev-nonce", Name: "phone", CredID: d.credID, PubKey: pub, Origin: nonceOrigin, Added: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d nonceDevice) auth(t *testing.T, challenge []byte) AuthMsg {
	cd := makeTestClientDataJSON(t, "webauthn.get", nonceOrigin, challenge)
	ad := makeTestAuthData(t, nonceOrigin, 0x01|0x04, 1)
	_, sessPub := makeTestP256Key(t)
	return AuthMsg{
		Assertion:  AssertionWire{CredID: d.credID, AuthData: ad, ClientData: cd, Sig: signAssertion(t, d.key, ad, cd)},
		SessionPub: sessPub,
	}
}

func expect(t *testing.T, client *testClient, want uint8, what string) {
	t.Helper()
	if typ, payload := client.readMsg(t); typ != want {
		if len(payload) > 80 {
			payload = payload[:80]
		}
		t.Fatalf("%s: got message %d (%s), want %d", what, typ, payload, want)
	}
}

func TestAssertionFromClosedConnectionOnce(t *testing.T) {
	r, _ := setupTestRemote(t, newFakeHost())
	d := addNonceDevice(t, r)
	old := abandoned(t, r)
	msg := d.auth(t, old)

	client, _ := dialPipe(t, r)
	client.writeMsg(t, wAuth, msg)
	expect(t, client, wAuthed, "assertion over the nonce of a dead connection")

	client, _ = dialPipe(t, r)
	client.writeMsg(t, wAuth, msg)
	expect(t, client, wError, "the same assertion again")
}

func TestOwnNonceUsedOnceAcrossConnections(t *testing.T) {
	r, _ := setupTestRemote(t, newFakeHost())
	d := addNonceDevice(t, r)
	client, nonce := dialPipe(t, r)
	msg := d.auth(t, nonce)
	client.writeMsg(t, wAuth, msg)
	expect(t, client, wAuthed, "assertion over the connection's own nonce")

	other, _ := dialPipe(t, r)
	other.writeMsg(t, wAuth, msg)
	expect(t, other, wError, "replay of an assertion already used")
}

func TestExpiredNonceRefused(t *testing.T) {
	r, _ := setupTestRemote(t, newFakeHost())
	d := addNonceDevice(t, r)
	old := abandoned(t, r)
	r.nonces.mu.Lock()
	for i := range r.nonces.entries {
		r.nonces.entries[i].issued = time.Now().Add(-nonceTTL - time.Second)
	}
	r.nonces.mu.Unlock()
	client, _ := dialPipe(t, r)
	client.writeMsg(t, wAuth, d.auth(t, old))
	expect(t, client, wError, "assertion over a nonce older than 5 minutes")
}

func TestUnknownNonceRefused(t *testing.T) {
	r, _ := setupTestRemote(t, newFakeHost())
	d := addNonceDevice(t, r)
	unknown := make([]byte, 32)
	_, _ = rand.Read(unknown)
	client, _ := dialPipe(t, r)
	client.writeMsg(t, wAuth, d.auth(t, unknown))
	expect(t, client, wError, "assertion over a nonce the daemon never issued")
}

func TestNonceStoreBounded(t *testing.T) {
	var s nonceStore
	now := time.Now()
	for i := range maxNonce + 1 {
		s.add([]byte{byte(i)}, now)
	}
	if s.take([]byte{0}, now) {
		t.Fatal("oldest nonce still there past the bound")
	}
	for i := 1; i <= maxNonce; i++ {
		if !s.take([]byte{byte(i)}, now) {
			t.Fatalf("nonce %d evicted", i)
		}
	}
	if s.take([]byte{5}, now) {
		t.Fatal("a nonce taken twice")
	}
}

func TestNoncesClearedOnDisableAndRegenerate(t *testing.T) {
	r, _ := setupTestRemote(t, newFakeHost())
	now := time.Now()
	r.nonces.add([]byte("a"), now)
	r.Disable()
	if r.nonces.take([]byte("a"), now) {
		t.Fatal("nonce kept after Disable")
	}
	r.nonces.add([]byte("b"), now)
	r.Close()
	if r.nonces.take([]byte("b"), now) {
		t.Fatal("nonce kept after Close")
	}
	r.nonces.add([]byte("c"), now)
	if err := r.Regenerate(); err != nil {
		t.Fatal(err)
	}
	if r.nonces.take([]byte("c"), now) {
		t.Fatal("nonce kept after Regenerate")
	}
}

// The passkey created while iOS cut the connection is sent on a new one.
func TestPairWithNonceOfClosedConnection(t *testing.T) {
	r, _ := setupTestRemote(t, newFakeHost())
	tok := r.openPairing(time.Now()).Token()
	old := abandoned(t, r)

	_, pub := makeTestP256Key(t)
	credID := []byte("cred-pair-old")
	_, sessPub := makeTestP256Key(t)
	client, _ := dialPipe(t, r)
	client.writeMsg(t, wPair, PairMsg{
		Token: tok,
		Reg: RegistrationWire{
			CredID:     credID,
			PubKey:     pub,
			AuthData:   makeTestRegistrationAuthData(t, nonceOrigin, 0x01|0x04|0x40, 1, credID),
			ClientData: makeTestClientDataJSON(t, "webauthn.create", nonceOrigin, old),
			Name:       "phone",
		},
		SessionPub: sessPub,
	})
	expect(t, client, wPairCode, "Pair over the nonce of a dead connection")
	r.decidePairing(true)
	expect(t, client, wAuthed, "Pair confirmed")
}

func TestResumeWithNonceOfClosedConnection(t *testing.T) {
	r, _ := setupTestRemote(t, newFakeHost())
	key, pub := makeTestP256Key(t)
	if err := r.devices.Add(Device{ID: "dev-r", Name: "phone", CredID: []byte("c"), PubKey: pub, Origin: nonceOrigin, Added: time.Now()}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	id := r.resumes.Issue("dev-r", pub, t0)
	r.resumes.Disconnected("dev-r", t0)
	old := abandoned(t, r)
	msg := ResumeMsg{ID: id, Sig: signResumeChallenge(t, key, old), Nonce: old}

	client, _ := dialPipe(t, r)
	client.writeMsg(t, wResume, msg)
	expect(t, client, wAuthed, "Resume naming the nonce it signed")
	client.rw.Close()

	client, _ = dialPipe(t, r)
	client.writeMsg(t, wResume, msg)
	expect(t, client, wError, "the same Resume again")
}
