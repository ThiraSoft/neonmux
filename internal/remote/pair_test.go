package remote

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPairingSingleUse(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	p := NewPairing(t0)

	tok := p.Token()
	if len(tok) != 16 {
		t.Fatalf("expected 16-byte token, got %d bytes", len(tok))
	}
	if exp := p.Expires(); !exp.Equal(t0.Add(5 * time.Minute)) {
		t.Fatalf("expected expiration at %v, got %v", t0.Add(5*time.Minute), exp)
	}

	// First use within valid window succeeds
	if err := p.Use(tok, t0.Add(time.Second)); err != nil {
		t.Fatalf("first Use failed: %v", err)
	}

	// Second use fails (single use)
	if err := p.Use(tok, t0.Add(2*time.Second)); err == nil {
		t.Fatal("expected error on second Use, got nil")
	}
}

func TestPairingExpires(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	p := NewPairing(t0)
	tok := p.Token()

	// 5 min + 1 s -> expired
	if err := p.Use(tok, t0.Add(5*time.Minute+time.Second)); err == nil {
		t.Fatal("expected error for expired token, got nil")
	}

	// Subsequent use attempt also fails (consumed on first attempt)
	if err := p.Use(tok, t0.Add(time.Second)); err == nil {
		t.Fatal("expected error for re-using consumed expired token, got nil")
	}
}

func TestPairingWrongToken(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	p := NewPairing(t0)
	tok := p.Token()

	wrongTok := bytes.Repeat([]byte{0xff}, 16)
	if bytes.Equal(wrongTok, tok) {
		wrongTok[0] ^= 0x01
	}

	// Use with wrong token fails
	if err := p.Use(wrongTok, t0.Add(time.Second)); err == nil {
		t.Fatal("expected error for wrong token, got nil")
	}

	// Token is consumed on first use attempt, so using correct token now also fails
	if err := p.Use(tok, t0.Add(2*time.Second)); err == nil {
		t.Fatal("expected error for re-using token consumed by failed attempt, got nil")
	}
}

func TestSASCodeVector(t *testing.T) {
	token := make([]byte, 16)
	for i := 0; i < 16; i++ {
		token[i] = byte(i) // 00..0f
	}
	var hash [32]byte
	for i := 0; i < 32; i++ {
		hash[i] = byte(0x20 + i) // 20..3f
	}
	pub := []byte("pub")

	code := SASCode(token, hash, pub)
	const expected = "483657"
	if code != expected {
		t.Fatalf("SASCode mismatch: got %q, want pinned %q", code, expected)
	}
}

func TestPairingConcurrent(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	p := NewPairing(t0)
	tok := p.Token()

	const n = 20
	var successCount atomic.Int32
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := p.Use(tok, t0.Add(time.Second)); err == nil {
				successCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if successCount.Load() != 1 {
		t.Fatalf("expected exactly 1 successful Use, got %d", successCount.Load())
	}
}

// A slow passkey creation (over a minute, seen on the iPhone) still finds
// the token valid.
func TestPairingValidFiveMinutes(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	p := NewPairing(t0)
	if err := p.Use(p.Token(), t0.Add(5*time.Minute-time.Second)); err != nil {
		t.Fatalf("token refused before 5 minutes: %v", err)
	}
}
