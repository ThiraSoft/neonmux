package remote

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"math/big"
	"sync"
	"time"
)

type resumeSession struct {
	deviceID string
	pubKey   *ecdsa.PublicKey
	issued   time.Time
}

type deviceState struct {
	count          int
	lastDisconnect time.Time
}

// Resumes manages short-lived in-memory resume sessions allowing a paired
// device to reconnect within 5 minutes of disconnection without Face ID.
type Resumes struct {
	mu       sync.Mutex
	sessions map[string]*resumeSession // id -> session
	devices  map[string]*deviceState   // deviceID -> state
}

func (r *Resumes) initLocked() {
	if r.sessions == nil {
		r.sessions = make(map[string]*resumeSession)
	}
	if r.devices == nil {
		r.devices = make(map[string]*deviceState)
	}
}

func (r *Resumes) purgeExpiredLocked(now time.Time) {
	for id, s := range r.sessions {
		if !now.Before(s.issued.Add(12 * time.Hour)) {
			delete(r.sessions, id)
		}
	}
}

// Issue generates a new resume session for deviceID with its ephemeral public key
// (in SPKI DER format). It replaces any previous resume key for deviceID.
// The session is valid while count > 0 or now < lastDisconnect+5min, and now < issued+12h.
func (r *Resumes) Issue(deviceID string, pub []byte, now time.Time) string {
	if deviceID == "" {
		return ""
	}
	pubKey, err := x509.ParsePKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	ecPub, ok := pubKey.(*ecdsa.PublicKey)
	if !ok || ecPub.Curve != elliptic.P256() {
		return ""
	}

	var rawID [16]byte
	if _, err := rand.Read(rawID[:]); err != nil {
		return ""
	}
	id := hex.EncodeToString(rawID[:])

	r.mu.Lock()
	defer r.mu.Unlock()
	r.initLocked()
	r.purgeExpiredLocked(now)

	// Replace any previous resume key of the same device
	for oldID, s := range r.sessions {
		if s.deviceID == deviceID {
			delete(r.sessions, oldID)
		}
	}

	dev, ok := r.devices[deviceID]
	if !ok {
		dev = &deviceState{}
		r.devices[deviceID] = dev
	}
	dev.count++

	r.sessions[id] = &resumeSession{
		deviceID: deviceID,
		pubKey:   ecPub,
		issued:   now,
	}
	return id
}

// Verify validates a resume attempt with the given session id, challenge,
// and raw 64-byte signature (r||s). If valid, it returns the deviceID.
func (r *Resumes) Verify(id string, challenge, sig []byte, now time.Time) (string, error) {
	if len(sig) != 64 {
		return "", errors.New("invalid signature length: expected 64 bytes")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.initLocked()
	r.purgeExpiredLocked(now)

	s, ok := r.sessions[id]
	if !ok {
		return "", errors.New("unknown or revoked resume session")
	}

	// Session lifetime: at most 12 hours from issue
	if !now.Before(s.issued.Add(12 * time.Hour)) {
		return "", errors.New("resume session expired (exceeded 12 hours)")
	}

	// Disconnection window: count > 0 or disconnected less than 5 minutes ago
	dev := r.devices[s.deviceID]
	if dev == nil {
		return "", errors.New("device state not found")
	}
	if dev.count == 0 && !now.Before(dev.lastDisconnect.Add(5*time.Minute)) {
		return "", errors.New("resume window expired (exceeded 5 minutes since disconnect)")
	}

	// Verify ECDSA-SHA256 signature over "nmx-resume-v1" || challenge in raw r||s format
	h := sha256.New()
	h.Write([]byte("nmx-resume-v1"))
	h.Write(challenge)
	digest := h.Sum(nil)

	rInt := new(big.Int).SetBytes(sig[:32])
	sInt := new(big.Int).SetBytes(sig[32:])
	if rInt.Sign() <= 0 || sInt.Sign() <= 0 {
		return "", errors.New("invalid signature component")
	}

	if !ecdsa.Verify(s.pubKey, digest, rInt, sInt) {
		return "", errors.New("invalid resume signature")
	}

	return s.deviceID, nil
}

// Disconnected records that a connection for deviceID closed at now.
// It decrements the device connection count (never below 0) and sets
// lastDisconnect only when the count reaches 0.
func (r *Resumes) Disconnected(deviceID string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initLocked()

	dev, ok := r.devices[deviceID]
	if !ok {
		dev = &deviceState{}
		r.devices[deviceID] = dev
	}
	if dev.count > 0 {
		dev.count--
	}
	if dev.count == 0 {
		dev.lastDisconnect = now
	}
}

// Connected records that an additional connection for deviceID opened.
func (r *Resumes) Connected(deviceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initLocked()

	dev, ok := r.devices[deviceID]
	if !ok {
		dev = &deviceState{}
		r.devices[deviceID] = dev
	}
	dev.count++
}

// Revoke invalidates all active resume sessions for deviceID.
func (r *Resumes) Revoke(deviceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initLocked()

	for id, s := range r.sessions {
		if s.deviceID == deviceID {
			delete(r.sessions, id)
		}
	}
	delete(r.devices, deviceID)
}

// Clear removes all resume sessions and device states from memory.
func (r *Resumes) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = make(map[string]*resumeSession)
	r.devices = make(map[string]*deviceState)
}
