package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps the whole package away from the user's config: an action
// reached through a shortcut (mod+t, mod+shift+b…) saves it, and a single
// test that forgets to isolate itself would overwrite the real file.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "neonmux-ui-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("NEONMUX_CONFIG", filepath.Join(dir, "config.json"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
