package remote

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCertSeriesCoversAYear(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	certs, err := LoadCerts(tmp, t0)
	if err != nil {
		t.Fatalf("LoadCerts failed: %v", err)
	}

	hashes := certs.Hashes(t0)
	if len(hashes) == 0 {
		t.Fatalf("expected at least one certificate, got none")
	}

	first := hashes[0]
	if first.NotBefore.After(t0) {
		t.Errorf("first cert NotBefore %v is after t0 %v", first.NotBefore, t0)
	}

	last := hashes[len(hashes)-1]
	yearEnd := t0.Add(365 * 24 * time.Hour)
	if last.NotAfter.Before(yearEnd) {
		t.Errorf("last cert NotAfter %v is before t0+365d %v", last.NotAfter, yearEnd)
	}

	for i, h := range hashes {
		duration := h.NotAfter.Sub(h.NotBefore)
		expectedDuration := 13 * 24 * time.Hour
		if duration != expectedDuration {
			t.Errorf("cert %d validity duration %v != expected %v", i, duration, expectedDuration)
		}
		if i > 0 {
			spacing := h.NotBefore.Sub(hashes[i-1].NotBefore)
			expectedSpacing := 7 * 24 * time.Hour
			if spacing != expectedSpacing {
				t.Errorf("spacing between cert %d and %d is %v != expected %v", i-1, i, spacing, expectedSpacing)
			}
		}
	}

	// Verify ECDSA P-256 key on leaf
	tlsCert := certs.Current(t0)
	if tlsCert.Leaf == nil {
		t.Fatalf("Current(t0).Leaf is nil")
	}
	pub, ok := tlsCert.Leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Errorf("expected ECDSA P-256 public key, got %T (%v)", tlsCert.Leaf.PublicKey, pub)
	}
}

func TestCertSeriesIsStableAcrossLoads(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	c1, err := LoadCerts(tmp, t0)
	if err != nil {
		t.Fatalf("first LoadCerts failed: %v", err)
	}
	c2, err := LoadCerts(tmp, t0)
	if err != nil {
		t.Fatalf("second LoadCerts failed: %v", err)
	}

	h1 := c1.Hashes(t0)
	h2 := c2.Hashes(t0)
	if !reflect.DeepEqual(h1, h2) {
		t.Errorf("hashes differ across loads:\nh1: %+v\nh2: %+v", h1, h2)
	}
}

func TestCertSeriesExtends(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	c1, err := LoadCerts(tmp, t0)
	if err != nil {
		t.Fatalf("initial LoadCerts failed: %v", err)
	}
	h1 := c1.Hashes(t0)

	t1 := t0.Add(200 * 24 * time.Hour)
	c2, err := LoadCerts(tmp, t1)
	if err != nil {
		t.Fatalf("extended LoadCerts failed: %v", err)
	}
	h2 := c2.Hashes(t1)

	yearEndAfterExtend := t1.Add(365 * 24 * time.Hour)
	last := h2[len(h2)-1]
	if last.NotAfter.Before(yearEndAfterExtend) {
		t.Errorf("last cert NotAfter %v is before t1+365d %v", last.NotAfter, yearEndAfterExtend)
	}

	// Older hashes from c1 that have not expired at t1 must still be present in c2
	unexpiredFound := 0
	for _, oldHash := range h1 {
		if oldHash.NotAfter.After(t1) {
			unexpiredFound++
			found := false
			for _, newHash := range h2 {
				if newHash.Hash == oldHash.Hash {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("old unexpired hash %x (expires %v) missing after extend at %v",
					oldHash.Hash[:8], oldHash.NotAfter, t1)
			}
		}
	}
	if unexpiredFound == 0 {
		t.Fatalf("expected unexpired hashes from initial series at t0+200d, found none")
	}
}

func TestCurrentIsValidNow(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	certs, err := LoadCerts(tmp, t0)
	if err != nil {
		t.Fatalf("LoadCerts failed: %v", err)
	}

	// Test 30 instants spread over the year (every 12 days)
	for i := 0; i < 30; i++ {
		now := t0.Add(time.Duration(i*12) * 24 * time.Hour)
		tlsCert := certs.Current(now)
		if tlsCert.Leaf == nil {
			t.Fatalf("instant %d (%v): Leaf is nil", i, now)
		}
		if tlsCert.Leaf.NotBefore.After(now) {
			t.Errorf("instant %d (%v): cert NotBefore %v is in the future", i, now, tlsCert.Leaf.NotBefore)
		}
		if !now.Before(tlsCert.Leaf.NotAfter) {
			t.Errorf("instant %d (%v): cert NotAfter %v is expired", i, now, tlsCert.Leaf.NotAfter)
		}

		// Hash of leaf must be in Hashes(now)
		leafHash := sha256.Sum256(tlsCert.Certificate[0])
		currentHashes := certs.Hashes(now)
		found := false
		for _, ch := range currentHashes {
			if ch.Hash == leafHash {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("instant %d (%v): current cert hash %x not in Hashes(now)", i, now, leafHash[:8])
		}
	}
}

func TestRegenerateChangesEverything(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	certs, err := LoadCerts(tmp, t0)
	if err != nil {
		t.Fatalf("LoadCerts failed: %v", err)
	}
	h1 := certs.Hashes(t0)

	if err := certs.Regenerate(t0); err != nil {
		t.Fatalf("Regenerate failed: %v", err)
	}
	h2 := certs.Hashes(t0)

	oldMap := make(map[[32]byte]bool)
	for _, h := range h1 {
		oldMap[h.Hash] = true
	}

	for _, h := range h2 {
		if oldMap[h.Hash] {
			t.Errorf("hash %x preserved after Regenerate", h.Hash[:8])
		}
	}
}

func TestCertHashJSONFormat(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(13 * 24 * time.Hour)
	var hash [32]byte
	for i := range hash {
		hash[i] = byte(i + 1)
	}

	ch := CertHash{
		NotBefore: t0,
		NotAfter:  t1,
		Hash:      hash,
	}

	data, err := json.Marshal(ch)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw failed: %v", err)
	}

	if nb, ok := raw["nb"].(float64); !ok || int64(nb) != t0.Unix() {
		t.Errorf("expected nb=%d, got %v", t0.Unix(), raw["nb"])
	}
	if na, ok := raw["na"].(float64); !ok || int64(na) != t1.Unix() {
		t.Errorf("expected na=%d, got %v", t1.Unix(), raw["na"])
	}
	if _, ok := raw["h"].(string); !ok {
		t.Errorf("expected string for h, got %T", raw["h"])
	}

	var decoded CertHash
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal round-trip failed: %v", err)
	}
	if !decoded.NotBefore.Equal(t0) || !decoded.NotAfter.Equal(t1) || decoded.Hash != hash {
		t.Errorf("decoded %+v != original %+v", decoded, ch)
	}
}

func TestCertFilesPermissions(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	if _, err := LoadCerts(tmp, t0); err != nil {
		t.Fatalf("LoadCerts failed: %v", err)
	}

	keyPath := filepath.Join(tmp, "certkey.pem")
	if fi, err := os.Stat(keyPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("certkey.pem mode = %o, expected 0600 (err: %v)", fi.Mode().Perm(), err)
	}

	certsPath := filepath.Join(tmp, "certs.json")
	if fi, err := os.Stat(certsPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("certs.json mode = %o, expected 0600 (err: %v)", fi.Mode().Perm(), err)
	}
}

func TestLoadCertsCorruptJSON(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	if _, err := LoadCerts(tmp, t0); err != nil {
		t.Fatalf("setup LoadCerts failed: %v", err)
	}

	keyPath := filepath.Join(tmp, "certkey.pem")
	certsPath := filepath.Join(tmp, "certs.json")

	origKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	corruptData := []byte("not valid json")
	if err := os.WriteFile(certsPath, corruptData, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = LoadCerts(tmp, t0)
	if err == nil {
		t.Fatalf("expected error loading corrupt certs.json, got nil")
	}
	if !strings.Contains(err.Error(), "certs.json") {
		t.Errorf("error %q should wrap certs.json", err.Error())
	}

	curKey, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(curKey, origKey) {
		t.Errorf("certkey.pem changed on disk after failed load")
	}
	curJSON, err := os.ReadFile(certsPath)
	if err != nil || !bytes.Equal(curJSON, corruptData) {
		t.Errorf("certs.json changed on disk after failed load")
	}
}

func TestLoadCertsTruncatedPEM(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	if _, err := LoadCerts(tmp, t0); err != nil {
		t.Fatalf("setup LoadCerts failed: %v", err)
	}

	keyPath := filepath.Join(tmp, "certkey.pem")
	certsPath := filepath.Join(tmp, "certs.json")

	origJSON, err := os.ReadFile(certsPath)
	if err != nil {
		t.Fatal(err)
	}
	truncKey := []byte("-----BEGIN EC PRIVATE KEY-----\nMIIB")
	if err := os.WriteFile(keyPath, truncKey, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = LoadCerts(tmp, t0)
	if err == nil {
		t.Fatalf("expected error loading truncated key, got nil")
	}
	if !strings.Contains(err.Error(), "certkey.pem") {
		t.Errorf("error %q should wrap certkey.pem", err.Error())
	}

	curKey, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(curKey, truncKey) {
		t.Errorf("certkey.pem changed on disk after failed load")
	}
	curJSON, err := os.ReadFile(certsPath)
	if err != nil || !bytes.Equal(curJSON, origJSON) {
		t.Errorf("certs.json changed on disk after failed load")
	}
}

func TestLoadCertsOneFileMissing(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	t.Run("missing certs.json", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "remote")
		if _, err := LoadCerts(tmp, t0); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(tmp, "certkey.pem")
		certsPath := filepath.Join(tmp, "certs.json")
		origKey, _ := os.ReadFile(keyPath)
		_ = os.Remove(certsPath)

		_, err := LoadCerts(tmp, t0)
		if err == nil {
			t.Fatalf("expected error when certs.json is missing, got nil")
		}
		if !strings.Contains(err.Error(), "certs.json") {
			t.Errorf("error %q should wrap certs.json", err.Error())
		}
		curKey, _ := os.ReadFile(keyPath)
		if !bytes.Equal(curKey, origKey) {
			t.Errorf("certkey.pem changed on disk")
		}
		if _, err := os.Stat(certsPath); !os.IsNotExist(err) {
			t.Errorf("certs.json should not have been created on failed load")
		}
	})

	t.Run("missing certkey.pem", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "remote")
		if _, err := LoadCerts(tmp, t0); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(tmp, "certkey.pem")
		certsPath := filepath.Join(tmp, "certs.json")
		origJSON, _ := os.ReadFile(certsPath)
		_ = os.Remove(keyPath)

		_, err := LoadCerts(tmp, t0)
		if err == nil {
			t.Fatalf("expected error when certkey.pem is missing, got nil")
		}
		if !strings.Contains(err.Error(), "certkey.pem") {
			t.Errorf("error %q should wrap certkey.pem", err.Error())
		}
		curJSON, _ := os.ReadFile(certsPath)
		if !bytes.Equal(curJSON, origJSON) {
			t.Errorf("certs.json changed on disk")
		}
		if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
			t.Errorf("certkey.pem should not have been created on failed load")
		}
	})
}

func TestLoadCertsKeyCertMismatch(t *testing.T) {
	tmp1 := filepath.Join(t.TempDir(), "remote1")
	tmp2 := filepath.Join(t.TempDir(), "remote2")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	if _, err := LoadCerts(tmp1, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCerts(tmp2, t0); err != nil {
		t.Fatal(err)
	}

	key1, _ := os.ReadFile(filepath.Join(tmp1, "certkey.pem"))
	key2Path := filepath.Join(tmp2, "certkey.pem")
	certs2Path := filepath.Join(tmp2, "certs.json")

	// Overwrite key in tmp2 with key1
	if err := os.WriteFile(key2Path, key1, 0o600); err != nil {
		t.Fatal(err)
	}

	origKey := key1
	origJSON, _ := os.ReadFile(certs2Path)

	_, err := LoadCerts(tmp2, t0)
	if err == nil {
		t.Fatalf("expected error on key/cert mismatch, got nil")
	}

	curKey, _ := os.ReadFile(key2Path)
	curJSON, _ := os.ReadFile(certs2Path)
	if !bytes.Equal(curKey, origKey) || !bytes.Equal(curJSON, origJSON) {
		t.Errorf("files changed on disk after failed load with key/cert mismatch")
	}
}

func TestLoadCertsValidityExceeds14Days(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = ensureDir(tmp)
	if err := saveKey(filepath.Join(tmp, "certkey.pem"), k); err != nil {
		t.Fatal(err)
	}

	// 15 days validity exceeds 14 days limit
	serial, _ := rand.Int(rand.Reader, big.NewInt(100000))
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "neonmux"},
		NotBefore:             t0,
		NotAfter:              t0.Add(15 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	rec := []certRecord{{
		NotBefore: t0.Unix(),
		NotAfter:  t0.Add(15 * 24 * time.Hour).Unix(),
		DER:       base64.StdEncoding.EncodeToString(der),
		Hash:      base64.RawURLEncoding.EncodeToString(sha256.New().Sum(der)),
	}}
	data, _ := json.Marshal(rec)
	_ = os.WriteFile(filepath.Join(tmp, "certs.json"), data, 0o600)

	_, err = LoadCerts(tmp, t0)
	if err == nil {
		t.Fatalf("expected error loading cert with validity > 14 days, got nil")
	}
}

func TestLoadCertsAt100DaysDoesNotExtend(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	c1, err := LoadCerts(tmp, t0)
	if err != nil {
		t.Fatalf("initial LoadCerts failed: %v", err)
	}
	h1 := c1.Hashes(t0)

	// At t0+100d, > 182 days remain so it must not extend
	c2, err := LoadCerts(tmp, t0.Add(100*24*time.Hour))
	if err != nil {
		t.Fatalf("LoadCerts at t0+100d failed: %v", err)
	}
	h2 := c2.Hashes(t0)

	if !reflect.DeepEqual(h1, h2) {
		t.Errorf("LoadCerts at t0+100d unexpectedly modified certs (extended)")
	}
}

func TestRegenerateRollbackOnSaveFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping unwritable dir test as root")
	}
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	c, err := LoadCerts(tmp, t0)
	if err != nil {
		t.Fatal(err)
	}

	origKey := c.key
	origHashes := c.Hashes(t0)

	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(tmp, 0o700) }()

	err = c.Regenerate(t0)
	if err == nil {
		t.Fatalf("expected error from Regenerate in unwritable directory, got nil")
	}

	if c.key != origKey {
		t.Errorf("c.key was swapped in memory despite save failure")
	}
	if !reflect.DeepEqual(c.Hashes(t0), origHashes) {
		t.Errorf("c.certs was swapped in memory despite save failure")
	}
}
