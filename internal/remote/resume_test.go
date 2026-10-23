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

	// Invalid signature length
	id := resumes.Issue("dev", pubSPKI, t0)
	if _, err := resumes.Verify(id, []byte("ch"), make([]byte, 63), t0); err == nil {
		t.Fatal("expected error for 63-byte signature, got nil")
	}

	// Clear wipes all sessions
	resumes.Clear()
	if _, err := resumes.Verify(id, []byte("ch"), make([]byte, 64), t0); err == nil {
		t.Fatal("expected error after Clear, got nil")
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
