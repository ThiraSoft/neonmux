package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// On Linux, fonts are sorted into subdirectories (/usr/share/fonts/TTF/,
// ~/.local/share/fonts/NerdFonts/…): the search goes down into them, to a
// limited depth.
func TestFindSymbolsFontInSubdirs(t *testing.T) {
	root := t.TempDir()
	touch := func(rel string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, nil, 0o644)
		return p
	}
	touch("TTF/JetBrainsMonoNerdFont-Regular.ttf")
	want := touch("TTF/nerd-fonts/SymbolsNerdFontMono-Regular.ttf")
	if got := findSymbolsFontIn([]string{root}); got != want {
		t.Fatalf("found %q, want %q", got, want)
	}
	deep := t.TempDir()
	os.MkdirAll(filepath.Join(deep, "a/b/c/d/e/f"), 0o755)
	os.WriteFile(filepath.Join(deep, "a/b/c/d/e/f/SymbolsNerdFontMono-Regular.ttf"), nil, 0o644)
	if got := findSymbolsFontIn([]string{deep}); got != "" {
		t.Errorf("too deep, and yet found: %q", got)
	}
}
