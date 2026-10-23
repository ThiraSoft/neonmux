package remote

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDevicesRoundTripAndPerms(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "remote")
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	devices, err := LoadDevices(tmp)
	if err != nil {
		t.Fatalf("LoadDevices failed: %v", err)
	}

	dev := Device{
		ID:       "dev-1",
		Name:     "iPhone de Ricardo",
		CredID:   []byte{0x01, 0x02, 0x03, 0x04},
		PubKey:   []byte("spki-der-public-key"),
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

	dev := Device{
		ID:       "dev-touch",
		Name:     "Test Device",
		CredID:   []byte{0xaa, 0xbb},
		PubKey:   []byte("spki"),
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
