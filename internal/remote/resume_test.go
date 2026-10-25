package remote

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"sync"
	"testing"
	"time"
)

func signResumeChallenge(t *testing.T, key *ecdsa.PrivateKey, challenge []byte) []byte {
	t.Helper()
	h := sha256.New()
	h.Write([]byte("nmx-resume-v1"))
	h.Write(challenge)
	digest := h.Sum(nil)

	r, s, err := ecdsa.Sign(rand.Reader, key, digest)
	if err != nil {
		t.Fatalf("failed to sign resume challenge: %v", err)
	}

	sig := make([]byte, 64)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)
	return sig
}

func TestResumeWindow(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate P-256 key: %v", err)
	}
	pubSPKI, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal SPKI: %v", err)
	}

	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate other key: %v", err)
	}

	resumes := &Resumes{}
	t0 := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	deviceID := "device-iphone-1"

	// Issue resume session
	id := resumes.Issue(deviceID, pubSPKI, t0)
	if id == "" {
		t.Fatal("expected non-empty resume session id")
	}
	resumes.Connected(deviceID)

	// Disconnected at t
	tDisconnect := t0.Add(10 * time.Minute)
	resumes.Disconnected(deviceID, tDisconnect)

	challenge := []byte("resume-challenge-1")
	sig := signResumeChallenge(t, key, challenge)

	// Verify at t+4m59s OK
	t4m59 := tDisconnect.Add(4*time.Minute + 59*time.Second)
	dev, err := resumes.Verify(id, challenge, sig, t4m59)
	if err != nil {
		t.Fatalf("expected verify at t+4m59s to succeed, got: %v", err)
	}
	if dev != deviceID {
		t.Fatalf("expected deviceID %q, got %q", deviceID, dev)
	}

	// Verify at t+5m01s error (window expired)
	t5m01 := tDisconnect.Add(5*time.Minute + time.Second)
	challenge2 := []byte("resume-challenge-2")
	sig2 := signResumeChallenge(t, key, challenge2)
	if _, err := resumes.Verify(id, challenge2, sig2, t5m01); err == nil {
		t.Fatal("expected error verifying at t+5m01s, got nil")
	}

	// Reconnected keeps it valid (even past tDisconnect + 5min)
	resumes.Connected(deviceID)
	tReconnectVerify := tDisconnect.Add(6 * time.Minute)
	challenge3 := []byte("resume-challenge-3")
	sig3 := signResumeChallenge(t, key, challenge3)
	dev, err = resumes.Verify(id, challenge3, sig3, tReconnectVerify)
	if err != nil {
		t.Fatalf("expected verify after reconnect to succeed, got: %v", err)
	}
	if dev != deviceID {
		t.Fatalf("expected deviceID %q, got %q", deviceID, dev)
	}

	// At issued+12h01m error even if connected
	t12h01 := t0.Add(12*time.Hour + time.Minute)
	challenge4 := []byte("resume-challenge-4")
	sig4 := signResumeChallenge(t, key, challenge4)
	if _, err := resumes.Verify(id, challenge4, sig4, t12h01); err == nil {
		t.Fatal("expected error verifying at issued+12h01m, got nil")
	}

	// Revoke makes it fail
	tRevokeVerify := t0.Add(time.Hour)
	challenge5 := []byte("resume-challenge-5")
	sig5 := signResumeChallenge(t, key, challenge5)
	resumes.Revoke(deviceID)
	if _, err := resumes.Verify(id, challenge5, sig5, tRevokeVerify); err == nil {
		t.Fatal("expected error verifying after revoke, got nil")
	}

	// Signature by another key fails
	// Re-issue a new session for testing signature rejection
	idNew := resumes.Issue(deviceID, pubSPKI, tRevokeVerify)
	resumes.Connected(deviceID)
	challenge6 := []byte("resume-challenge-6")
	sigBad := signResumeChallenge(t, otherKey, challenge6)
	if _, err := resumes.Verify(idNew, challenge6, sigBad, tRevokeVerify.Add(time.Minute)); err == nil {
		t.Fatal("expected error verifying signature by another key, got nil")
	}
}

func TestResumesEdgeCases(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	pubSPKI, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)

	resumes := &Resumes{}
	t0 := time.Now()

	// Empty device ID rejected
	if id := resumes.Issue("", pubSPKI, t0); id != "" {
		t.Fatalf("expected empty id for empty device ID, got %q", id)
	}

	// Malformed pub key rejected
	if id := resumes.Issue("dev", []byte("invalid-pub-key"), t0); id != "" {
		t.Fatalf("expected empty id for invalid pub key, got %q", id)
	}

	// Unknown session ID
	if _, err := resumes.Verify("unknown-id", []byte("ch"), make([]byte, 64), t0); err == nil {
		t.Fatal("expected error for unknown session id, got nil")
	}

	// Invalid signature length (truncated valid signature)
	id := resumes.Issue("dev", pubSPKI, t0)
	resumes.Connected("dev")
	ch := []byte("ch")
	validSig := signResumeChallenge(t, key, ch)
	if _, err := resumes.Verify(id, ch, validSig[:63], t0); err == nil {
		t.Fatal("expected error for 63-byte signature, got nil")
	}

	// Valid ASN.1 signature rejected (expects raw 64-byte r||s)
	h := sha256.New()
	h.Write([]byte("nmx-resume-v1"))
	h.Write(ch)
	digest := h.Sum(nil)
	asn1Sig, err := ecdsa.SignASN1(rand.Reader, key, digest)
	if err != nil {
		t.Fatalf("sign ASN.1: %v", err)
	}
	if _, err := resumes.Verify(id, ch, asn1Sig, t0); err == nil {
		t.Fatal("expected error for ASN.1 signature, got nil")
	}

	// Verify before Clear succeeds with valid signature
	if _, err := resumes.Verify(id, ch, validSig, t0); err != nil {
		t.Fatalf("verify before Clear failed: %v", err)
	}

	// Clear wipes all sessions (must fail even with a valid signature)
	resumes.Clear()
	if _, err := resumes.Verify(id, ch, validSig, t0); err == nil {
		t.Fatal("expected error after Clear with valid signature, got nil")
	}
}

func TestResumeConnectionCount(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	pubSPKI, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)

	resumes := &Resumes{}
	t0 := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	deviceID := "device-parallel-1"

	// Issue creates the resume key without incrementing count
	id := resumes.Issue(deviceID, pubSPKI, t0)
	if id == "" {
		t.Fatal("expected non-empty resume id")
	}

	// Two parallel connections open for the device (Connected alone increments)
	resumes.Connected(deviceID)
	resumes.Connected(deviceID)

	// First connection closes at t1 = t0 + 1m (count decrements to 1)
	t1 := t0.Add(time.Minute)
	resumes.Disconnected(deviceID, t1)

	challenge1 := []byte("challenge-1")
	sig1 := signResumeChallenge(t, key, challenge1)

	// Verify at +6 min after first disconnect succeeds because count > 0
	tVerify1 := t1.Add(6 * time.Minute)
	dev, err := resumes.Verify(id, challenge1, sig1, tVerify1)
	if err != nil {
		t.Fatalf("expected verify at +6 min with open connection to succeed, got: %v", err)
	}
	if dev != deviceID {
		t.Fatalf("expected device %q, got %q", deviceID, dev)
	}

	// Second connection closes at t2 = t1 + 7m (count decrements to 0, sets lastDisconnect)
	t2 := t1.Add(7 * time.Minute)
	resumes.Disconnected(deviceID, t2)

	// Verify at +5m01s after second disconnect fails (now > lastDisconnect + 5m)
	tVerify2 := t2.Add(5*time.Minute + time.Second)
	challenge2 := []byte("challenge-2")
	sig2 := signResumeChallenge(t, key, challenge2)
	if _, err := resumes.Verify(id, challenge2, sig2, tVerify2); err == nil {
		t.Fatal("expected verify at +5m01s after final disconnect to fail, got nil")
	}
}

func TestResumeIssueReplacesPreviousKey(t *testing.T) {
	key1, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub1, _ := x509.MarshalPKIXPublicKey(&key1.PublicKey)

	key2, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub2, _ := x509.MarshalPKIXPublicKey(&key2.PublicKey)

	resumes := &Resumes{}
	t0 := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	deviceID := "device-single-key"

	id1 := resumes.Issue(deviceID, pub1, t0)
	if id1 == "" {
		t.Fatal("expected non-empty id1")
	}
	resumes.Connected(deviceID)

	// Issue a second key for the same device
	t1 := t0.Add(time.Minute)
	id2 := resumes.Issue(deviceID, pub2, t1)
	if id2 == "" {
		t.Fatal("expected non-empty id2")
	}
	resumes.Connected(deviceID)
	if id1 == id2 {
		t.Fatal("expected distinct session ids")
	}

	challenge := []byte("replace-challenge")
	sig1 := signResumeChallenge(t, key1, challenge)
	sig2 := signResumeChallenge(t, key2, challenge)

	// First id must no longer verify
	if _, err := resumes.Verify(id1, challenge, sig1, t1.Add(time.Minute)); err == nil {
		t.Fatal("expected verify of replaced id1 to fail, got nil")
	}

	// Second id must verify
	if dev, err := resumes.Verify(id2, challenge, sig2, t1.Add(time.Minute)); err != nil || dev != deviceID {
		t.Fatalf("expected verify of id2 to succeed, got dev=%q err=%v", dev, err)
	}
}

func TestResumePurgesExpired(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)

	resumes := &Resumes{}
	t0 := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	id1 := resumes.Issue("dev1", pub, t0)
	resumes.Connected("dev1")

	// At t0 + 12h01m, id1 is expired (> 12h)
	t12h01 := t0.Add(12*time.Hour + time.Minute)
	challenge := []byte("purge-ch")
	sig := signResumeChallenge(t, key, challenge)

	// Verify at t12h01 will fail and purge id1
	if _, err := resumes.Verify(id1, challenge, sig, t12h01); err == nil {
		t.Fatal("expected verify to fail")
	}

	// Issue for dev2 at t12h01 also purges expired sessions
	id2 := resumes.Issue("dev2", pub, t12h01)
	if id2 == "" {
		t.Fatal("expected non-empty id2")
	}
	resumes.Connected("dev2")

	// Internal session map should only have id2 now
	resumes.mu.Lock()
	_, found1 := resumes.sessions[id1]
	_, found2 := resumes.sessions[id2]
	resumes.mu.Unlock()

	if found1 {
		t.Fatal("expected expired id1 to be purged")
	}
	if !found2 {
		t.Fatal("expected active id2 to be retained")
	}
}

func TestResumeConnSequence(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubSPKI, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)

	resumes := &Resumes{}
	t0 := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	deviceID := "device-conn-seq"

	// conn.go sequence: Issue, then Connected
	id := resumes.Issue(deviceID, pubSPKI, t0)
	resumes.Connected(deviceID)

	// Disconnected at t
	tDisconnect := t0.Add(10 * time.Minute)
	resumes.Disconnected(deviceID, tDisconnect)

	challenge := []byte("conn-seq-challenge")
	sig := signResumeChallenge(t, key, challenge)

	// Verify at t+4m59s must succeed
	t4m59 := tDisconnect.Add(4*time.Minute + 59*time.Second)
	dev, err := resumes.Verify(id, challenge, sig, t4m59)
	if err != nil {
		t.Fatalf("expected verify at t+4m59s to succeed, got: %v", err)
	}
	if dev != deviceID {
		t.Fatalf("expected device %q, got %q", deviceID, dev)
	}

	// Verify at t+5m01s must FAIL
	t5m01 := tDisconnect.Add(5*time.Minute + time.Second)
	challenge2 := []byte("conn-seq-challenge-2")
	sig2 := signResumeChallenge(t, key, challenge2)
	if _, err := resumes.Verify(id, challenge2, sig2, t5m01); err == nil {
		t.Fatal("expected verify at t+5m01s to fail, got nil")
	}
}

func TestResumesConcurrent(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	pubSPKI, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)

	resumes := &Resumes{}
	t0 := time.Now()
	id := resumes.Issue("dev-concurrent", pubSPKI, t0)
	resumes.Connected("dev-concurrent")
	challenge := []byte("concurrent-challenge")
	sig := signResumeChallenge(t, key, challenge)

	const n = 30
	var wg sync.WaitGroup
	wg.Add(n * 3)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = resumes.Verify(id, challenge, sig, t0.Add(time.Second))
		}()
		go func() {
			defer wg.Done()
			resumes.Connected("dev-concurrent")
		}()
		go func() {
			defer wg.Done()
			resumes.Disconnected("dev-concurrent", t0.Add(time.Second))
		}()
	}
	wg.Wait()
}
