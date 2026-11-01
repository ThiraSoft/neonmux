package remote

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Pairing manages the single-use pairing token generated for QR display.
type Pairing struct {
	mu      sync.Mutex
	token   []byte
	expires time.Time
	used    bool
}

// pairingTTL is how long a pairing token stays valid: 5 minutes, since
// creating the passkey on the phone (name, Face ID, the iOS save sheet) can
// take more than one.
const pairingTTL = 5 * time.Minute

// NewPairing generates a new pairing token with 16 cryptographically random
// bytes, valid for pairingTTL from now.
func NewPairing(now time.Time) *Pairing {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return &Pairing{
		token:   token,
		expires: now.Add(pairingTTL),
	}
}

// Token returns a copy of the 16-byte pairing token.
func (p *Pairing) Token() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	tok := make([]byte, len(p.token))
	copy(tok, p.token)
	return tok
}

// Expires returns the expiration timestamp of the pairing token.
func (p *Pairing) Expires() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.expires
}

// Used reports whether the token was already presented.
func (p *Pairing) Used() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.used
}

// Use consumes the pairing token. Comparison is performed in constant time.
// A wrong token fails without consuming it: anyone on the network can send
// one, which must not close the pairing of the phone in front of the machine.
// 16 random bytes are out of reach of guessing, and failed handshakes are
// rate limited.
func (p *Pairing) Use(token []byte, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.used {
		return errors.New("pairing token already used")
	}
	if !now.Before(p.expires) {
		p.used = true
		return errors.New("pairing token expired")
	}
	if subtle.ConstantTimeCompare(p.token, token) != 1 {
		return errors.New("invalid pairing token")
	}
	p.used = true
	return nil
}

// SASCode computes the 6-digit confirmation code shown on both the desktop
// and mobile screens during pairing.
// It computes sha256("nmx-pair-v1" || token || certHash || devicePub), takes
// the first 4 bytes as a big-endian uint32 modulo 1 000 000, and formats it
// as a zero-padded 6-digit string.
func SASCode(token []byte, certHash [32]byte, devicePub []byte) string {
	h := sha256.New()
	h.Write([]byte("nmx-pair-v1"))
	h.Write(token)
	h.Write(certHash[:])
	h.Write(devicePub)
	sum := h.Sum(nil)

	v := binary.BigEndian.Uint32(sum[0:4]) % 1000000
	return fmt.Sprintf("%06d", v)
}
