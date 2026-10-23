package remote

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// Assertion contains the authentication assertion returned by the phone
// via navigator.credentials.get().
type Assertion struct {
	CredID     []byte
	AuthData   []byte
	ClientData []byte
	Sig        []byte
}

// Registration contains the credential registration data returned by the
// phone via navigator.credentials.create().
type Registration struct {
	CredID     []byte
	PubKey     []byte // SPKI DER from getPublicKey()
	AuthData   []byte // getAuthenticatorData()
	ClientData []byte
	Name       string
}

type clientDataJSON struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin *bool  `json:"crossOrigin,omitempty"`
	TopOrigin   string `json:"topOrigin,omitempty"`
}

const (
	flagUP = 0x01
	flagUV = 0x04
	flagAT = 0x40
)

func parseP256SPKI(pubBytes []byte) (*ecdsa.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(pubBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid public key SPKI: %w", err)
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok || ecPub.Curve != elliptic.P256() {
		return nil, errors.New("public key is not ECDSA P-256")
	}
	return ecPub, nil
}

func verifyClientData(clientDataBytes []byte, expectedType, origin string, challenge []byte) error {
	var cd clientDataJSON
	if err := json.Unmarshal(clientDataBytes, &cd); err != nil {
		return fmt.Errorf("invalid clientDataJSON: %w", err)
	}
	if cd.Type != expectedType {
		return fmt.Errorf("unexpected clientData type: got %q, want %q", cd.Type, expectedType)
	}
	expectedChallenge := base64.RawURLEncoding.EncodeToString(challenge)
	if cd.Challenge != expectedChallenge {
		return errors.New("challenge mismatch")
	}
	if cd.Origin != origin {
		return fmt.Errorf("origin mismatch: got %q, want %q", cd.Origin, origin)
	}
	if cd.CrossOrigin != nil && *cd.CrossOrigin {
		return errors.New("cross-origin requests not allowed")
	}
	if cd.TopOrigin != "" {
		return errors.New("topOrigin must be empty")
	}
	return nil
}

func verifyAuthData(authData []byte, origin string) error {
	if len(authData) < 37 {
		return errors.New("authData too short")
	}
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("invalid origin URL: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("missing hostname in origin")
	}
	expectedRPHash := sha256.Sum256([]byte(host))
	if !bytes.Equal(authData[0:32], expectedRPHash[:]) {
		return errors.New("rpIdHash mismatch")
	}
	flags := authData[32]
	if flags&flagUP == 0 {
		return errors.New("user present (UP) flag not set")
	}
	if flags&flagUV == 0 {
		return errors.New("user verified (UV) flag not set")
	}
	return nil
}

// VerifyAssertion verifies an authentication assertion from the phone against
// the device's registered public key (in SPKI DER form), the expected origin,
// and the server challenge.
func VerifyAssertion(a Assertion, pub []byte, origin string, challenge []byte) error {
	ecPub, err := parseP256SPKI(pub)
	if err != nil {
		return err
	}
	if err := verifyClientData(a.ClientData, "webauthn.get", origin, challenge); err != nil {
		return err
	}
	if err := verifyAuthData(a.AuthData, origin); err != nil {
		return err
	}

	cdHash := sha256.Sum256(a.ClientData)
	signedData := make([]byte, 0, len(a.AuthData)+len(cdHash))
	signedData = append(signedData, a.AuthData...)
	signedData = append(signedData, cdHash[:]...)
	digest := sha256.Sum256(signedData)

	if !ecdsa.VerifyASN1(ecPub, digest[:], a.Sig) {
		return errors.New("invalid assertion signature")
	}
	return nil
}

// VerifyRegistration verifies the phone's passkey registration response
// against the expected origin and server challenge, and ensures the public
// key parses as an ECDSA P-256 SPKI key.
// Registration checks only detect a broken client: the real gate is the
// pairing token, SAS code, and desktop confirmation.
func VerifyRegistration(r Registration, origin string, challenge []byte) error {
	if _, err := parseP256SPKI(r.PubKey); err != nil {
		return err
	}
	if len(r.CredID) == 0 {
		return errors.New("empty credential id")
	}
	if err := verifyClientData(r.ClientData, "webauthn.create", origin, challenge); err != nil {
		return err
	}
	if err := verifyAuthData(r.AuthData, origin); err != nil {
		return err
	}

	// Attested credential data (AT flag) is mandatory for registration.
	if r.AuthData[32]&flagAT == 0 {
		return errors.New("attested credential data flag (AT) not set")
	}
	if len(r.AuthData) < 55 {
		return errors.New("authData too short for attested credential data")
	}
	n := int(binary.BigEndian.Uint16(r.AuthData[53:55]))
	if len(r.AuthData) < 55+n {
		return errors.New("authData truncated: insufficient bytes for credential id")
	}
	if !bytes.Equal(r.AuthData[55:55+n], r.CredID) {
		return errors.New("credential id mismatch in authData")
	}
	return nil
}
