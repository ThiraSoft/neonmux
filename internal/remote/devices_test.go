package remote

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testP256SPKI(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestDevicesRoundTripAndPerms(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	devices, err := LoadDevices(tmp)
	if err != nil {
		t.Fatalf("LoadDevices failed: %v", err)
	}

	pubKey := testP256SPKI(t)
	dev := Device{
		ID:       "dev-1",
		Name:     "iPhone de Ricardo",
		CredID:   []byte{0x01, 0x02, 0x03, 0x04},
		PubKey:   pubKey,
		Origin:   "https://arch.ts.net",
		Added:    t0,
		LastSeen: t0,
	}

	if err := devices.Add(dev); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Reload from disk
	d2, err := LoadDevices(tmp)
	if err != nil {
		t.Fatalf("second LoadDevices failed: %v", err)
	}

	found, ok := d2.ByCredID([]byte{0x01, 0x02, 0x03, 0x04})
	if !ok {
		t.Fatalf("ByCredID failed to find added device")
	}
	if found.ID != dev.ID || found.Name != dev.Name || found.Origin != dev.Origin {
		t.Errorf("found device %+v != original %+v", found, dev)
	}
	if !bytes.Equal(found.CredID, dev.CredID) || !bytes.Equal(found.PubKey, dev.PubKey) {
		t.Errorf("found keys mismatch: CredID %v, PubKey %v", found.CredID, found.PubKey)
	}

	// Remove
	if err := d2.Remove(dev.ID); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	if _, ok := d2.ByCredID([]byte{0x01, 0x02, 0x03, 0x04}); ok {
		t.Errorf("device still found after Remove")
	}

	// Check permissions: devices.json mode is 0600 and the dir 0700
	jsonPath := filepath.Join(tmp, "devices.json")
	fi, err := os.Stat(jsonPath)
	if err != nil {
		t.Fatalf("stat %s failed: %v", jsonPath, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("devices.json mode = %o, expected 0600", perm)
	}

	di, err := os.Stat(tmp)
	if err != nil {
		t.Fatalf("stat %s failed: %v", tmp, err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("remote dir mode = %o, expected 0700", perm)
	}
}

func TestEnsureDirRefusesLoosePerms(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}

	err := ensureDir(tmp)
	if err == nil {
		t.Fatalf("ensureDir accepted mode 0755, expected error")
	}
}

func TestDevicesTouchAndClear(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	devices, err := LoadDevices(tmp)
	if err != nil {
		t.Fatalf("LoadDevices failed: %v", err)
	}

	pubKey := testP256SPKI(t)
	dev := Device{
		ID:       "dev-touch",
		Name:     "Test Device",
		CredID:   []byte{0xaa, 0xbb},
		PubKey:   pubKey,
		Origin:   "https://work.local",
		Added:    t0,
		LastSeen: t0,
	}
	if err := devices.Add(dev); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	t1 := t0.Add(1 * time.Hour)
	if err := devices.Touch("dev-touch", t1); err != nil {
		t.Fatalf("Touch failed: %v", err)
	}

	d2, err := LoadDevices(tmp)
	if err != nil {
		t.Fatalf("LoadDevices reload failed: %v", err)
	}
	found, ok := d2.ByCredID([]byte{0xaa, 0xbb})
	if !ok {
		t.Fatalf("ByCredID not found after touch")
	}
	if !found.LastSeen.Equal(t1) {
		t.Errorf("LastSeen = %v, expected %v", found.LastSeen, t1)
	}

	// List
	list := d2.List()
	if len(list) != 1 || list[0].ID != "dev-touch" {
		t.Errorf("unexpected list: %+v", list)
	}

	// Clear
	if err := d2.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}
	if len(d2.List()) != 0 {
		t.Errorf("List not empty after Clear")
	}

	d3, err := LoadDevices(tmp)
	if err != nil {
		t.Fatalf("LoadDevices reload after Clear failed: %v", err)
	}
	if len(d3.List()) != 0 {
		t.Errorf("List not empty after reload following Clear")
	}
}

func TestAddValidation(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	devices, err := LoadDevices(tmp)
	if err != nil {
		t.Fatal(err)
	}

	validSPKI := testP256SPKI(t)

	// Empty ID
	err = devices.Add(Device{
		ID:     "",
		CredID: []byte{1},
		PubKey: validSPKI,
	})
	if err == nil {
		t.Errorf("expected error for empty ID, got nil")
	}

	// Empty CredID
	err = devices.Add(Device{
		ID:     "dev-empty-cred",
		CredID: nil,
		PubKey: validSPKI,
	})
	if err == nil {
		t.Errorf("expected error for empty CredID, got nil")
	}
	err = devices.Add(Device{
		ID:     "dev-empty-cred-len0",
		CredID: []byte{},
		PubKey: validSPKI,
	})
	if err == nil {
		t.Errorf("expected error for 0-length CredID, got nil")
	}

	// Garbage PubKey
	err = devices.Add(Device{
		ID:     "dev-garbage-key",
		CredID: []byte{1},
		PubKey: []byte("not-spki"),
	})
	if err == nil {
		t.Errorf("expected error for garbage PubKey, got nil")
	}

	// RSA PubKey
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaSPKI, _ := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	err = devices.Add(Device{
		ID:     "dev-rsa",
		CredID: []byte{2},
		PubKey: rsaSPKI,
	})
	if err == nil {
		t.Errorf("expected error for RSA PubKey, got nil")
	}

	// P-384 PubKey
	p384Key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384SPKI, _ := x509.MarshalPKIXPublicKey(&p384Key.PublicKey)
	err = devices.Add(Device{
		ID:     "dev-p384",
		CredID: []byte{3},
		PubKey: p384SPKI,
	})
	if err == nil {
		t.Errorf("expected error for P-384 PubKey, got nil")
	}

	// Valid device
	dev1 := Device{
		ID:       "dev-1",
		Name:     "Device 1",
		CredID:   []byte{10, 20},
		PubKey:   validSPKI,
		Origin:   "https://test.ts.net",
		Added:    t0,
		LastSeen: t0,
	}
	if err := devices.Add(dev1); err != nil {
		t.Fatalf("valid Add failed: %v", err)
	}

	// Duplicate ID
	err = devices.Add(Device{
		ID:     "dev-1",
		Name:     "Duplicate ID",
		CredID: []byte{30, 40},
		PubKey: testP256SPKI(t),
	})
	if err == nil {
		t.Errorf("expected error for duplicate ID, got nil")
	}

	// Duplicate CredID
	err = devices.Add(Device{
		ID:     "dev-2",
		Name:     "Duplicate CredID",
		CredID: []byte{10, 20},
		PubKey: testP256SPKI(t),
	})
	if err == nil {
		t.Errorf("expected error for duplicate CredID, got nil")
	}
}

func TestDevicesRollbackOnSaveFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping unwritable dir test as root")
	}
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	devices, err := LoadDevices(tmp)
	if err != nil {
		t.Fatal(err)
	}

	dev1 := Device{
		ID:       "dev-1",
		Name:     "Device 1",
		CredID:   []byte{1, 2},
		PubKey:   testP256SPKI(t),
		Origin:   "https://host.ts.net",
		Added:    t0,
		LastSeen: t0,
	}
	if err := devices.Add(dev1); err != nil {
		t.Fatal(err)
	}

	// Make directory read-only to force saveLocked to fail
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(tmp, 0o700) }()

	// Add rollback
	dev2 := Device{
		ID:       "dev-2",
		Name:     "Device 2",
		CredID:   []byte{3, 4},
		PubKey:   testP256SPKI(t),
		Origin:   "https://host.ts.net",
		Added:    t0,
		LastSeen: t0,
	}
	if err := devices.Add(dev2); err == nil {
		t.Errorf("expected Add to fail in read-only directory")
	}
	list := devices.List()
	if len(list) != 1 || list[0].ID != "dev-1" {
		t.Errorf("Add modified memory on save failure: %+v", list)
	}

	// Remove rollback
	if err := devices.Remove("dev-1"); err == nil {
		t.Errorf("expected Remove to fail in read-only directory")
	}
	list = devices.List()
	if len(list) != 1 || list[0].ID != "dev-1" {
		t.Errorf("Remove modified memory on save failure: %+v", list)
	}

	// Touch rollback
	t1 := t0.Add(5 * time.Minute)
	if err := devices.Touch("dev-1", t1); err == nil {
		t.Errorf("expected Touch to fail in read-only directory")
	}
	list = devices.List()
	if len(list) != 1 || !list[0].LastSeen.Equal(t0) {
		t.Errorf("Touch modified memory on save failure: LastSeen = %v, expected %v", list[0].LastSeen, t0)
	}

	// Clear rollback
	if err := devices.Clear(); err == nil {
		t.Errorf("expected Clear to fail in read-only directory")
	}
	list = devices.List()
	if len(list) != 1 || list[0].ID != "dev-1" {
		t.Errorf("Clear modified memory on save failure: %+v", list)
	}
}
