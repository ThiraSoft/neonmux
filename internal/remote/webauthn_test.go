package remote

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/url"
	"testing"
)

type testClientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin *bool  `json:"crossOrigin,omitempty"`
	TopOrigin   string `json:"topOrigin,omitempty"`
}

func makeTestP256Key(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate P-256 key: %v", err)
	}
	pubSPKI, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal SPKI: %v", err)
	}
	return key, pubSPKI
}

func makeTestClientDataJSON(t *testing.T, typ, origin string, challenge []byte) []byte {
	t.Helper()
	cd := testClientData{
		Type:      typ,
		Challenge: base64.RawURLEncoding.EncodeToString(challenge),
		Origin:    origin,
	}
	data, err := json.Marshal(cd)
	if err != nil {
		t.Fatalf("failed to marshal clientDataJSON: %v", err)
	}
	return data
}

func makeTestAuthData(t *testing.T, origin string, flags byte, signCount uint32) []byte {
	t.Helper()
	u, err := url.Parse(origin)
	if err != nil {
		t.Fatalf("invalid origin: %v", err)
	}
	host := u.Hostname()
	rpHash := sha256.Sum256([]byte(host))

	// By default include attested credential data for "cred-pair-1" with AT flag
	// so callers in conn_test.go (e.g. TestPairFlow) and assertion callers succeed.
	credID := []byte("cred-pair-1")
	authData := make([]byte, 55+len(credID))
	copy(authData[0:32], rpHash[:])
	authData[32] = flags | flagAT
	authData[33] = byte(signCount >> 24)
	authData[34] = byte(signCount >> 16)
	authData[35] = byte(signCount >> 8)
	authData[36] = byte(signCount)
	binary.BigEndian.PutUint16(authData[53:55], uint16(len(credID)))
	copy(authData[55:], credID)
	return authData
}

func makeTestRegistrationAuthData(t *testing.T, origin string, flags byte, signCount uint32, credID []byte) []byte {
	t.Helper()
	u, err := url.Parse(origin)
	if err != nil {
		t.Fatalf("invalid origin: %v", err)
	}
	host := u.Hostname()
	rpHash := sha256.Sum256([]byte(host))

	authData := make([]byte, 55+len(credID))
	copy(authData[0:32], rpHash[:])
	authData[32] = flags
	authData[33] = byte(signCount >> 24)
	authData[34] = byte(signCount >> 16)
	authData[35] = byte(signCount >> 8)
	authData[36] = byte(signCount)
	// bytes 37..52: aaguid (16 bytes)
	binary.BigEndian.PutUint16(authData[53:55], uint16(len(credID)))
	copy(authData[55:], credID)
	return authData
}

func signAssertion(t *testing.T, key *ecdsa.PrivateKey, authData, clientData []byte) []byte {
	t.Helper()
	cdHash := sha256.Sum256(clientData)
	signedData := append(append([]byte(nil), authData...), cdHash[:]...)
	digest := sha256.Sum256(signedData)
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("failed to sign assertion: %v", err)
	}
	return sig
}

func TestAssertionOK(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("random-challenge-bytes-12345")
	clientData := makeTestClientDataJSON(t, "webauthn.get", origin, challenge)
	authData := makeTestAuthData(t, origin, 0x01|0x04, 1) // UP | UV
	sig := signAssertion(t, key, authData, clientData)

	a := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}

	if err := VerifyAssertion(a, pubSPKI, origin, challenge); err != nil {
		t.Fatalf("VerifyAssertion failed: %v", err)
	}
}

func TestAssertionWrongType(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("random-challenge-bytes-12345")
	clientData := makeTestClientDataJSON(t, "webauthn.create", origin, challenge)
	authData := makeTestAuthData(t, origin, 0x01|0x04, 1)
	sig := signAssertion(t, key, authData, clientData)

	a := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}

	if err := VerifyAssertion(a, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error for wrong clientData type, got nil")
	}
}

func TestAssertionWrongChallenge(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("random-challenge-bytes-12345")
	wrongChallenge := []byte("wrong-challenge-bytes-99999")
	clientData := makeTestClientDataJSON(t, "webauthn.get", origin, wrongChallenge)
	authData := makeTestAuthData(t, origin, 0x01|0x04, 1)
	sig := signAssertion(t, key, authData, clientData)

	a := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}

	if err := VerifyAssertion(a, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error for wrong challenge, got nil")
	}
}

func TestAssertionWrongOrigin(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	wrongOrigin := "https://attacker.evil.com"
	challenge := []byte("random-challenge-bytes-12345")
	clientData := makeTestClientDataJSON(t, "webauthn.get", wrongOrigin, challenge)
	authData := makeTestAuthData(t, origin, 0x01|0x04, 1)
	sig := signAssertion(t, key, authData, clientData)

	a := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}

	if err := VerifyAssertion(a, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error for wrong origin, got nil")
	}
}

func TestAssertionWrongRPIDHash(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("random-challenge-bytes-12345")
	clientData := makeTestClientDataJSON(t, "webauthn.get", origin, challenge)
	// Build authData with rpIdHash of a different host
	authData := makeTestAuthData(t, "https://different.ts.net", 0x01|0x04, 1)
	sig := signAssertion(t, key, authData, clientData)

	a := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}

	if err := VerifyAssertion(a, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error for wrong rpIdHash, got nil")
	}
}

func TestAssertionUVMissing(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("random-challenge-bytes-12345")
	clientData := makeTestClientDataJSON(t, "webauthn.get", origin, challenge)
	authData := makeTestAuthData(t, origin, 0x01, 1) // UP only, UV missing
	sig := signAssertion(t, key, authData, clientData)

	a := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}

	if err := VerifyAssertion(a, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error when UV flag is missing, got nil")
	}
}

func TestAssertionUPMissing(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("random-challenge-bytes-12345")
	clientData := makeTestClientDataJSON(t, "webauthn.get", origin, challenge)
	authData := makeTestAuthData(t, origin, 0x04, 1) // UV only, UP missing
	sig := signAssertion(t, key, authData, clientData)

	a := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}

	if err := VerifyAssertion(a, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error when UP flag is missing, got nil")
	}
}

func TestAssertionSignatureByAnotherKey(t *testing.T) {
	_, pubSPKI := makeTestP256Key(t)
	otherKey, _ := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("random-challenge-bytes-12345")
	clientData := makeTestClientDataJSON(t, "webauthn.get", origin, challenge)
	authData := makeTestAuthData(t, origin, 0x01|0x04, 1)
	sig := signAssertion(t, otherKey, authData, clientData) // signed by other key

	a := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}

	if err := VerifyAssertion(a, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error when signature is by another key, got nil")
	}
}

func TestAssertionTruncatedAuthData(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("random-challenge-bytes-12345")
	clientData := makeTestClientDataJSON(t, "webauthn.get", origin, challenge)

	lengths := []int{0, 1, 10, 31, 32, 33, 36}
	for _, l := range lengths {
		authData := make([]byte, l)
		sig := signAssertion(t, key, authData, clientData)

		a := Assertion{
			CredID:     []byte("cred-123"),
			AuthData:   authData,
			ClientData: clientData,
			Sig:        sig,
		}

		if err := VerifyAssertion(a, pubSPKI, origin, challenge); err == nil {
			t.Fatalf("expected error for truncated authData of length %d, got nil", l)
		}
	}
}

func TestRegistrationOK(t *testing.T) {
	_, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("reg-challenge-bytes-54321")
	clientData := makeTestClientDataJSON(t, "webauthn.create", origin, challenge)
	credID := []byte("new-cred-456")
	authData := makeTestRegistrationAuthData(t, origin, 0x01|0x04|0x40, 0, credID) // UP | UV | AT

	r := Registration{
		CredID:     credID,
		PubKey:     pubSPKI,
		AuthData:   authData,
		ClientData: clientData,
		Name:       "Ricardo's iPhone",
	}

	if err := VerifyRegistration(r, origin, challenge); err != nil {
		t.Fatalf("VerifyRegistration failed: %v", err)
	}
}

func TestRegistrationRejectsNonP256(t *testing.T) {
	origin := "https://host.ts.net"
	challenge := []byte("reg-challenge-bytes-54321")
	clientData := makeTestClientDataJSON(t, "webauthn.create", origin, challenge)
	credID := []byte("new-cred-456")
	authData := makeTestRegistrationAuthData(t, origin, 0x01|0x04|0x40, 0, credID)

	// RSA key
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	rsaSPKI, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal RSA SPKI: %v", err)
	}

	rRSA := Registration{
		CredID:     credID,
		PubKey:     rsaSPKI,
		AuthData:   authData,
		ClientData: clientData,
		Name:       "Ricardo's iPhone",
	}
	if err := VerifyRegistration(rRSA, origin, challenge); err == nil {
		t.Fatal("expected error for RSA public key in registration, got nil")
	}

	// P-384 key
	p384Key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate P-384 key: %v", err)
	}
	p384SPKI, err := x509.MarshalPKIXPublicKey(&p384Key.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal P-384 SPKI: %v", err)
	}

	rP384 := Registration{
		CredID:     credID,
		PubKey:     p384SPKI,
		AuthData:   authData,
		ClientData: clientData,
		Name:       "Ricardo's iPhone",
	}
	if err := VerifyRegistration(rP384, origin, challenge); err == nil {
		t.Fatal("expected error for P-384 public key in registration, got nil")
	}

	// Malformed key bytes
	rMalformed := Registration{
		CredID:     credID,
		PubKey:     []byte("not-a-valid-spki-key"),
		AuthData:   authData,
		ClientData: clientData,
		Name:       "Ricardo's iPhone",
	}
	if err := VerifyRegistration(rMalformed, origin, challenge); err == nil {
		t.Fatal("expected error for malformed public key in registration, got nil")
	}
}

func TestAssertionMalformedInputs(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("random-challenge-bytes-12345")
	authData := makeTestAuthData(t, origin, 0x01|0x04, 1)

	// Malformed clientDataJSON
	aBadJSON := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: []byte("not valid json"),
		Sig:        []byte("sig"),
	}
	if err := VerifyAssertion(aBadJSON, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error for malformed clientData JSON, got nil")
	}

	// Malformed ASN.1 signature
	clientData := makeTestClientDataJSON(t, "webauthn.get", origin, challenge)
	aBadSig := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        []byte("not a valid asn1 sequence"),
	}
	if err := VerifyAssertion(aBadSig, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error for invalid ASN.1 signature, got nil")
	}

	// Malformed public key SPKI
	sig := signAssertion(t, key, authData, clientData)
	aGood := Assertion{
		CredID:     []byte("cred-123"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}
	if err := VerifyAssertion(aGood, []byte("bad-spki"), origin, challenge); err == nil {
		t.Fatal("expected error for invalid SPKI public key, got nil")
	}
}

func TestRegistrationEdgeCases(t *testing.T) {
	_, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("reg-challenge-bytes-54321")
	clientData := makeTestClientDataJSON(t, "webauthn.create", origin, challenge)
	authData := makeTestAuthData(t, origin, 0x01|0x04, 0)

	// Empty CredID
	rEmptyCred := Registration{
		CredID:     nil,
		PubKey:     pubSPKI,
		AuthData:   authData,
		ClientData: clientData,
	}
	if err := VerifyRegistration(rEmptyCred, origin, challenge); err == nil {
		t.Fatal("expected error for empty CredID in registration, got nil")
	}

	// Wrong clientData type (e.g. webauthn.get instead of webauthn.create)
	wrongCD := makeTestClientDataJSON(t, "webauthn.get", origin, challenge)
	rWrongType := Registration{
		CredID:     []byte("cred-1"),
		PubKey:     pubSPKI,
		AuthData:   authData,
		ClientData: wrongCD,
	}
	if err := VerifyRegistration(rWrongType, origin, challenge); err == nil {
		t.Fatal("expected error for wrong clientData type in registration, got nil")
	}
}

func TestAssertionCrossOriginTrue(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("challenge-cross-origin")
	crossOriginTrue := true
	cd := testClientData{
		Type:        "webauthn.get",
		Challenge:   base64.RawURLEncoding.EncodeToString(challenge),
		Origin:      origin,
		CrossOrigin: &crossOriginTrue,
	}
	clientData, err := json.Marshal(cd)
	if err != nil {
		t.Fatal(err)
	}
	authData := makeTestAuthData(t, origin, 0x01|0x04, 1)
	sig := signAssertion(t, key, authData, clientData)

	a := Assertion{
		CredID:     []byte("cred-1"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}
	if err := VerifyAssertion(a, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error for clientDataJSON with crossOrigin: true, got nil")
	}
}

func TestAssertionTopOrigin(t *testing.T) {
	key, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("challenge-top-origin")
	cd := testClientData{
		Type:      "webauthn.get",
		Challenge: base64.RawURLEncoding.EncodeToString(challenge),
		Origin:    origin,
		TopOrigin: "https://evil.example.com",
	}
	clientData, err := json.Marshal(cd)
	if err != nil {
		t.Fatal(err)
	}
	authData := makeTestAuthData(t, origin, 0x01|0x04, 1)
	sig := signAssertion(t, key, authData, clientData)

	a := Assertion{
		CredID:     []byte("cred-1"),
		AuthData:   authData,
		ClientData: clientData,
		Sig:        sig,
	}
	if err := VerifyAssertion(a, pubSPKI, origin, challenge); err == nil {
		t.Fatal("expected error for clientDataJSON with non-empty topOrigin, got nil")
	}
}

func TestRegistrationMismatchedCredID(t *testing.T) {
	_, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("reg-challenge")
	clientData := makeTestClientDataJSON(t, "webauthn.create", origin, challenge)

	// authData contains "cred-real", but Registration has "cred-fake"
	authData := makeTestRegistrationAuthData(t, origin, 0x01|0x04|0x40, 0, []byte("cred-real"))
	r := Registration{
		CredID:     []byte("cred-fake"),
		PubKey:     pubSPKI,
		AuthData:   authData,
		ClientData: clientData,
		Name:       "Phone",
	}
	if err := VerifyRegistration(r, origin, challenge); err == nil {
		t.Fatal("expected error for mismatched CredID, got nil")
	}
}

func TestRegistrationTruncatedAttestedData(t *testing.T) {
	_, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("reg-challenge")
	clientData := makeTestClientDataJSON(t, "webauthn.create", origin, challenge)
	credID := []byte("cred-12345")
	fullAuthData := makeTestRegistrationAuthData(t, origin, 0x01|0x04|0x40, 0, credID)

	// Test truncating at various offsets: 37, 52, 53, 54, and inside credID
	truncatedLengths := []int{37, 52, 53, 54, 55 + len(credID) - 1}
	for _, l := range truncatedLengths {
		r := Registration{
			CredID:     credID,
			PubKey:     pubSPKI,
			AuthData:   fullAuthData[:l],
			ClientData: clientData,
			Name:       "Phone",
		}
		if err := VerifyRegistration(r, origin, challenge); err == nil {
			t.Fatalf("expected error for truncated attested data of length %d, got nil", l)
		}
	}
}

func TestRegistrationMissingATFlag(t *testing.T) {
	_, pubSPKI := makeTestP256Key(t)
	origin := "https://host.ts.net"
	challenge := []byte("reg-challenge")
	clientData := makeTestClientDataJSON(t, "webauthn.create", origin, challenge)
	credID := []byte("cred-12345")
	// flags without 0x40 (AT)
	authData := makeTestRegistrationAuthData(t, origin, 0x01|0x04, 0, credID)

	r := Registration{
		CredID:     credID,
		PubKey:     pubSPKI,
		AuthData:   authData,
		ClientData: clientData,
		Name:       "Phone",
	}
	if err := VerifyRegistration(r, origin, challenge); err == nil {
		t.Fatal("expected error when AT flag is missing, got nil")
	}
}
