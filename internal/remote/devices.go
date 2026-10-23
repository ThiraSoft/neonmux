package remote

import (
	"bytes"
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

// Add saves a new paired device or replaces an existing one with the same ID.
func (d *Devices) Add(dev Device) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if dev.ID == "" {
		return errors.New("empty device id")
	}

	found := false
	for i, existing := range d.list {
		if existing.ID == dev.ID {
			d.list[i] = dev
			found = true
			break
		}
	}
	if !found {
		d.list = append(d.list, dev)
	}

	return d.saveLocked()
}

// Remove deletes the paired device with the specified ID.
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

	d.list = append(d.list[:idx], d.list[idx+1:]...)
	return d.saveLocked()
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

// Touch updates the LastSeen timestamp of the device with the given ID.
func (d *Devices) Touch(id string, t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i := range d.list {
		if d.list[i].ID == id {
			d.list[i].LastSeen = t
			return d.saveLocked()
		}
	}
	return fmt.Errorf("device %q not found", id)
}

// Clear forgets all paired devices and empties the on-disk list.
func (d *Devices) Clear() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.list = nil
	return d.saveLocked()
}

func (d *Devices) saveLocked() error {
	list := d.list
	if list == nil {
		list = []Device{}
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(d.dir, "devices.json"), data, 0o600)
}
