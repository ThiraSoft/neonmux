package shell

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

func screenText(term *vt.Terminal) []string {
	term.Lock()
	defer term.Unlock()
	cols, rows := term.Size()
	out := make([]string, rows)
	for y := 0; y < rows; y++ {
		var b strings.Builder
		for _, c := range term.ScreenLine(y)[:cols] {
			if c.Attr()&vt.AttrWideCont != 0 {
				continue
			}
			r := c.Rune()
			if r == 0 {
				r = ' '
			}
			b.WriteRune(r)
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

func waitFor(t *testing.T, s *Session, what string, cond func([]string) bool) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lines := screenText(s.Term)
		if cond(lines) {
			return lines
		}
		time.Sleep(50 * time.Millisecond)
	}
	lines := screenText(s.Term)
	t.Fatalf("timed out waiting for %s:\n%s", what, strings.Join(lines, "\n"))
	return nil
}

func contains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// TestNeovim runs a real nvim in the emulator and checks the screen.
func TestNeovim(t *testing.T) {
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim not installed")
	}
	s, err := Start(Options{
		Argv: []string{"nvim", "--clean", "session.go"},
		Cols: 100, Rows: 30, Scrollback: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	waitFor(t, s, "the file contents", func(l []string) bool {
		return contains(l, "package shell") && contains(l, "session.go")
	})
	if s.Term.Modes()&vt.ModeAltScreen == 0 {
		t.Fatal("nvim should be on the alternate screen")
	}

	s.Write([]byte(":set number\r"))
	waitFor(t, s, "line numbers", func(l []string) bool {
		return strings.HasPrefix(strings.TrimSpace(l[0]), "1 ")
	})

	// Scrolling: ctrl+d moves down half a page through the scroll regions.
	s.Write([]byte("\x04"))
	waitFor(t, s, "scrolling", func(l []string) bool {
		return !strings.HasPrefix(strings.TrimSpace(l[0]), "1 ")
	})

	// Insert mode, then accented text.
	s.Write([]byte("ggOcafé à 日本\x1b"))
	lines := waitFor(t, s, "the inserted text", func(l []string) bool {
		return contains(l, "café à 日本")
	})
	t.Logf("nvim screen:\n%s", strings.Join(lines, "\n"))

	s.Write([]byte(":q!\r"))
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("nvim did not exit")
	}
	if s.Term.Modes()&vt.ModeAltScreen != 0 {
		t.Fatal("the alternate screen should have been left")
	}
}
