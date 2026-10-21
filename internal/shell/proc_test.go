package shell

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestProcInfo(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	time.Sleep(50 * time.Millisecond)
	args := procArgs(cmd.Process.Pid)
	if len(args) != 2 || !strings.HasSuffix(args[0], "sleep") || args[1] != "30" {
		t.Fatalf("argv = %q", args)
	}
	want, _ := os.Stat(os.TempDir())
	got, err := os.Stat(procCwd(cmd.Process.Pid))
	if err != nil || !os.SameFile(want, got) {
		t.Fatalf("cwd = %q", procCwd(cmd.Process.Pid))
	}
}

func TestDisplayName(t *testing.T) {
	args := func(a ...string) func() []string { return func() []string { return a } }
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"2.1.304", []string{"claude", "--dangerously-skip-permissions"}, "claude"},
		{"2.1.304", []string{"/Users/x/.local/bin/claude"}, "claude"},
		{"nvim", []string{"nvim", "main.go"}, "nvim"},
		{"1", []string{"foo"}, "1"},       // not a version
		{"2.1.", []string{"foo"}, "2.1."}, // not a version
		{"3.12", nil, "3.12"},             // no argv, keep the name
	}
	for _, c := range cases {
		if got := displayName(c.name, args(c.args...)); got != c.want {
			t.Errorf("%q %q: %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

func TestLeftForShell(t *testing.T) {
	cases := []struct {
		prev, cur string
		want      bool
	}{
		{"claude", "zsh", true},
		{"claude", "-zsh", true},
		{"", "zsh", false},       // first sample
		{"zsh", "claude", false}, // the program starts
		{"bash", "zsh", false},   // wrapper script: never
		{"nvim", "claude", false},
	}
	for _, c := range cases {
		if got := LeftForShell(c.prev, c.cur); got != c.want {
			t.Errorf("%q → %q: %v", c.prev, c.cur, got)
		}
	}
}
