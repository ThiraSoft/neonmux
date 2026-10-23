package remote

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Device represents a paired mobile device authorized to control the daemon.
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	CredID   []byte    `json:"cred_id"`
	PubKey   []byte    `json:"pub_key"` // SPKI DER, P-256
	Origin   string    `json:"origin"`
	Added    time.Time `json:"added"`
	LastSeen time.Time `json:"last_seen"`
}

// Devices manages the list of paired phones stored on disk.
type Devices struct {
	mu   sync.RWMutex
	dir  string
	list []Device
}

// LoadDevices loads paired devices from dir/devices.json. If the file does
// not exist yet, it returns an empty device store.
func LoadDevices(dir string) (*Devices, error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}

	path := filepath.Join(dir, "devices.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Devices{
				dir:  dir,
				list: nil,
			}, nil
		}
		return nil, err
	}

	var list []Device
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return &Devices{
		dir:  dir,
		list: list,
	}, nil
}

// Add saves a new paired device. It rejects an empty ID, an empty CredID,
// a public key that is not ECDSA P-256 SPKI, and any device whose ID or CredID
// is already present. On disk write failure, in-memory state is unchanged.
func (d *Devices) Add(dev Device) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if dev.ID == "" {
		return errors.New("empty device id")
	}
	if len(dev.CredID) == 0 {
		return errors.New("empty credential id")
	}

	pub, err := x509.ParsePKIXPublicKey(dev.PubKey)
	if err != nil {
		return fmt.Errorf("invalid public key SPKI: %w", err)
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok || ecPub.Curve != elliptic.P256() {
		return errors.New("public key is not ECDSA P-256")
	}

	for _, existing := range d.list {
		if existing.ID == dev.ID {
			return fmt.Errorf("device id %q already exists", dev.ID)
		}
		if bytes.Equal(existing.CredID, dev.CredID) {
			return errors.New("credential id already exists")
		}
	}

	newList := make([]Device, len(d.list)+1)
	copy(newList, d.list)
	newList[len(d.list)] = dev

	if err := d.saveLocked(newList); err != nil {
		return err
	}
	d.list = newList
	return nil
}

// Remove deletes the paired device with the specified ID. On disk write
// failure, in-memory state is unchanged.
func (d *Devices) Remove(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	idx := -1
	for i, existing := range d.list {
		if existing.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("device %q not found", id)
	}

	newList := make([]Device, 0, len(d.list)-1)
	newList = append(newList, d.list[:idx]...)
	newList = append(newList, d.list[idx+1:]...)

	if err := d.saveLocked(newList); err != nil {
		return err
	}
	d.list = newList
	return nil
}

// ByCredID looks up a paired device by its WebAuthn credential ID.
func (d *Devices) ByCredID(credID []byte) (Device, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, dev := range d.list {
		if bytes.Equal(dev.CredID, credID) {
			return dev, true
		}
	}
	return Device{}, false
}

// List returns a copy of the list of all paired devices.
func (d *Devices) List() []Device {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(d.list) == 0 {
		return nil
	}
	out := make([]Device, len(d.list))
	copy(out, d.list)
	return out
}

// Touch updates the LastSeen timestamp of the device with the given ID. On
// disk write failure, in-memory state is unchanged.
func (d *Devices) Touch(id string, t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	idx := -1
	for i, existing := range d.list {
		if existing.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("device %q not found", id)
	}

	newList := make([]Device, len(d.list))
	copy(newList, d.list)
	newList[idx].LastSeen = t

	if err := d.saveLocked(newList); err != nil {
		return err
	}
	d.list = newList
	return nil
}

// Clear forgets all paired devices and empties the on-disk list. On disk
// write failure, in-memory state is unchanged.
func (d *Devices) Clear() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.saveLocked([]Device{}); err != nil {
		return err
	}
	d.list = nil
	return nil
}

func (d *Devices) saveLocked(list []Device) error {
	if list == nil {
		list = []Device{}
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(d.dir, "devices.json"), data, 0o600)
}
