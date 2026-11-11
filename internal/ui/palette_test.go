package ui

import (
	"testing"

	"github.com/ThiraSoft/neonmux/internal/gfx"
)

func TestTailCells(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"+20m continue the work", 10, "… the work"},
		{"abc", 0, ""},
	} {
		got := tailCells(c.in, c.n)
		if got != c.want {
			t.Errorf("tailCells(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if gfx.TextCells(got) > c.n {
			t.Errorf("tailCells(%q, %d) = %q, %d cells", c.in, c.n, got, gfx.TextCells(got))
		}
	}
}
