package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A window started from the desktop inherits the system's EDITOR
// (/etc/environment), not the one the user sets in their shell config.
func TestLoginEditorWinsOverInheritedEnvironment(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	home := t.TempDir()
	profile := "echo welcome back\nexport EDITOR=nvim\n"
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("EDITOR", "nano")

	if got := loginEditor(bash); got != "nvim" {
		t.Errorf("loginEditor = %q, want nvim", got)
	}
}

// A shell that cannot run says nothing: the caller falls back on the
// inherited environment.
func TestLoginEditorMissingShell(t *testing.T) {
	if got := loginEditor(filepath.Join(t.TempDir(), "absent")); got != "" {
		t.Errorf("loginEditor = %q, want empty", got)
	}
}
