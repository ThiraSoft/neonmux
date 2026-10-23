package remote

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	certValidityDays = 13
	certSpacingDays  = 7
	certCoverDays    = 365
	certExtendDays   = 182
)

// Cert represents one short-lived self-signed WebTransport certificate.
type Cert struct {
	NotBefore time.Time
	NotAfter  time.Time
	DER       []byte
	Hash      [32]byte
	leaf      *x509.Certificate
}

// CertHash is the fingerprint of a certificate pinned by the mobile client.
type CertHash struct {
	NotBefore time.Time
	NotAfter  time.Time
	Hash      [32]byte
}

type certHashJSON struct {
	NotBefore int64  `json:"nb"`
	NotAfter  int64  `json:"na"`
	Hash      string `json:"h"`
}

func (ch CertHash) MarshalJSON() ([]byte, error) {
	return json.Marshal(certHashJSON{
		NotBefore: ch.NotBefore.Unix(),
		NotAfter:  ch.NotAfter.Unix(),
		Hash:      base64.RawURLEncoding.EncodeToString(ch.Hash[:]),
	})
}

func (ch *CertHash) UnmarshalJSON(data []byte) error {
	var j certHashJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(j.Hash, "="))
	if err != nil {
		return err
	}
	if len(raw) != 32 {
		return errors.New("invalid cert hash length")
	}
	ch.NotBefore = time.Unix(j.NotBefore, 0).UTC()
	ch.NotAfter = time.Unix(j.NotAfter, 0).UTC()
	copy(ch.Hash[:], raw)
	return nil
}

type certRecord struct {
	NotBefore int64  `json:"nb"`
	NotAfter  int64  `json:"na"`
	DER       string `json:"der"`
	Hash      string `json:"h"`
}

// Certs manages the daemon's WebTransport certificate series on disk.
type Certs struct {
	mu    sync.RWMutex
	dir   string
	key   *ecdsa.PrivateKey
	certs []Cert
}

// LoadCerts loads the certificate series and private key from dir. If absent,
// it generates a new private key and a one-year series. If less than 182 days
// remain, it extends the series to cover a full year from now.
func LoadCerts(dir string, now time.Time) (*Certs, error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}

	keyPath := filepath.Join(dir, "certkey.pem")
	certsPath := filepath.Join(dir, "certs.json")

	key, keyErr := loadKey(keyPath)
	certs, certsErr := loadCertRecords(certsPath)

	if keyErr != nil || certsErr != nil || len(certs) == 0 {
		newKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		newCerts, err := generateSeries(newKey, now)
		if err != nil {
			return nil, err
		}
		if err := saveKey(keyPath, newKey); err != nil {
			return nil, err
		}
		if err := saveCertRecords(certsPath, newCerts); err != nil {
			return nil, err
		}
		return &Certs{
			dir:   dir,
			key:   newKey,
			certs: newCerts,
		}, nil
	}

	// Extend if under 182 days remain
	last := certs[len(certs)-1]
	if last.NotAfter.Sub(now) < certExtendDays*24*time.Hour {
		extended, err := extendSeries(key, certs, now)
		if err != nil {
			return nil, err
		}
		certs = extended
		if err := saveCertRecords(certsPath, certs); err != nil {
			return nil, err
		}
	}

	return &Certs{
		dir:   dir,
		key:   key,
		certs: certs,
	}, nil
}

// Current returns the tls.Certificate for now: the valid certificate that
// started last (latest NotBefore).
func (c *Certs) Current(now time.Time) tls.Certificate {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var best *Cert
	for i := range c.certs {
		cert := &c.certs[i]
		if !cert.NotBefore.After(now) && now.Before(cert.NotAfter) {
			if best == nil || cert.NotBefore.After(best.NotBefore) {
				best = cert
			}
		}
	}
	if best == nil {
		return tls.Certificate{}
	}

	leaf := best.leaf
	if leaf == nil {
		var err error
		leaf, err = x509.ParseCertificate(best.DER)
		if err != nil {
			return tls.Certificate{}
		}
	}

	return tls.Certificate{
		Certificate: [][]byte{best.DER},
		PrivateKey:  c.key,
		Leaf:        leaf,
	}
}

// Hashes returns every certificate fingerprint that has not yet expired at now.
func (c *Certs) Hashes(now time.Time) []CertHash {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var hashes []CertHash
	for _, cert := range c.certs {
		if cert.NotAfter.After(now) {
			hashes = append(hashes, CertHash{
				NotBefore: cert.NotBefore,
				NotAfter:  cert.NotAfter,
				Hash:      cert.Hash,
			})
		}
	}
	return hashes
}

// Regenerate creates a new private key and a fresh certificate series,
// replacing existing on-disk files.
func (c *Certs) Regenerate(now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	newKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	newCerts, err := generateSeries(newKey, now)
	if err != nil {
		return err
	}

	keyPath := filepath.Join(c.dir, "certkey.pem")
	certsPath := filepath.Join(c.dir, "certs.json")

	if err := saveKey(keyPath, newKey); err != nil {
		return err
	}
	if err := saveCertRecords(certsPath, newCerts); err != nil {
		return err
	}

	c.key = newKey
	c.certs = newCerts
	return nil
}

func generateSingleCert(key *ecdsa.PrivateKey, nb, na time.Time) (Cert, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return Cert{}, err
	}

	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "neonmux",
		},
		NotBefore:             nb,
		NotAfter:              na,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return Cert{}, err
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return Cert{}, err
	}

	return Cert{
		NotBefore: leaf.NotBefore,
		NotAfter:  leaf.NotAfter,
		DER:       der,
		Hash:      sha256.Sum256(der),
		leaf:      leaf,
	}, nil
}

func generateSeries(key *ecdsa.PrivateKey, now time.Time) ([]Cert, error) {
	var certs []Cert
	target := now.Add(certCoverDays * 24 * time.Hour)
	curNb := now.UTC().Truncate(time.Second)

	for {
		curNa := curNb.Add(certValidityDays * 24 * time.Hour)
		cert, err := generateSingleCert(key, curNb, curNa)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
		if !curNa.Before(target) {
			break
		}
		curNb = curNb.Add(certSpacingDays * 24 * time.Hour)
	}
	return certs, nil
}

func extendSeries(key *ecdsa.PrivateKey, existing []Cert, now time.Time) ([]Cert, error) {
	if len(existing) == 0 {
		return generateSeries(key, now)
	}

	certs := make([]Cert, len(existing))
	copy(certs, existing)

	last := certs[len(certs)-1]
	target := now.Add(certCoverDays * 24 * time.Hour)
	curNb := last.NotBefore.Add(certSpacingDays * 24 * time.Hour)

	for {
		curNa := curNb.Add(certValidityDays * 24 * time.Hour)
		cert, err := generateSingleCert(key, curNb, curNa)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
		if !curNa.Before(target) {
			break
		}
		curNb = curNb.Add(certSpacingDays * 24 * time.Hour)
	}
	return certs, nil
}

func loadKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("failed to decode PEM block from certkey.pem")
	}

	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	pk8, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ecKey, ok := pk8.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("key in certkey.pem is not ECDSA")
	}
	return ecKey, nil
}

func saveKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	block := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: der,
	})
	return atomicWriteFile(path, block, 0o600)
}

func loadCertRecords(path string) ([]Cert, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var records []certRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}

	certs := make([]Cert, len(records))
	for i, r := range records {
		der, err := base64.StdEncoding.DecodeString(r.DER)
		if err != nil {
			return nil, fmt.Errorf("invalid cert DER: %w", err)
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("invalid x509 cert: %w", err)
		}
		certs[i] = Cert{
			NotBefore: leaf.NotBefore,
			NotAfter:  leaf.NotAfter,
			DER:       der,
			Hash:      sha256.Sum256(der),
			leaf:      leaf,
		}
	}
	return certs, nil
}

func saveCertRecords(path string, certs []Cert) error {
	records := make([]certRecord, len(certs))
	for i, c := range certs {
		records[i] = certRecord{
			NotBefore: c.NotBefore.Unix(),
			NotAfter:  c.NotAfter.Unix(),
			DER:       base64.StdEncoding.EncodeToString(c.DER),
			Hash:      base64.RawURLEncoding.EncodeToString(c.Hash[:]),
		}
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o600)
}
