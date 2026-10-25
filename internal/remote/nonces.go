package remote

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

const (
	nonceTTL = 5 * time.Minute
	maxNonce = 64
)

// nonceStore keeps the Challenge nonces the daemon issued, so a signature
// obtained while its connection died (iOS cuts WebTransport while the Face
// ID sheet is up) can be sent on a new connection. Each nonce is good for
// one signature, for nonceTTL; the oldest goes when the store is full.
type nonceStore struct {
	mu      sync.Mutex
	entries []nonceEntry // oldest first
}

type nonceEntry struct {
	nonce  []byte
	issued time.Time
}

func (s *nonceStore) add(nonce []byte, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if len(s.entries) >= maxNonce {
		s.entries = s.entries[len(s.entries)-maxNonce+1:]
	}
	s.entries = append(s.entries, nonceEntry{nonce: bytes.Clone(nonce), issued: now})
}

// take removes nonce and reports whether it was there and unexpired.
func (s *nonceStore) take(nonce []byte, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	for i, e := range s.entries {
		if bytes.Equal(e.nonce, nonce) {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return true
		}
	}
	return false
}

func (s *nonceStore) clear() {
	s.mu.Lock()
	s.entries = nil
	s.mu.Unlock()
}

func (s *nonceStore) pruneLocked(now time.Time) {
	keep := s.entries[:0]
	for _, e := range s.entries {
		if now.Sub(e.issued) < nonceTTL {
			keep = append(keep, e)
		}
	}
	s.entries = keep
}

// challengeOf returns the challenge a WebAuthn clientDataJSON was signed
// over (nil if unreadable). Every other check stays in VerifyAssertion and
// VerifyRegistration, which compare it again.
func challengeOf(clientData []byte) []byte {
	var cd struct {
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(clientData, &cd) != nil || cd.Challenge == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(cd.Challenge, "="))
	if err != nil {
		return nil
	}
	return b
}

// claimNonce picks the challenge a signature may be checked against: the
// connection's own nonce, or an unused nonce of a connection that ended.
// Either way it is consumed now, before the check, so a signature works
// once.
func (r *Remote) claimNonce(signed, own []byte, now time.Time) ([]byte, bool) {
	if len(signed) == 0 {
		return nil, false
	}
	if bytes.Equal(signed, own) {
		r.nonces.take(own, now)
		return own, true
	}
	if r.nonces.take(signed, now) {
		return signed, true
	}
	return nil, false
}
