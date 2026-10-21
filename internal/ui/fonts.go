package ui

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

type fontEntry struct {
	Family string
	Style  string
	Path   string
}

func fontDirs() []string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return []string{"/System/Library/Fonts", "/Library/Fonts", filepath.Join(home, "Library/Fonts")}
	}
	return []string{"/usr/share/fonts", "/usr/local/share/fonts", filepath.Join(home, ".local/share/fonts"), filepath.Join(home, ".fonts")}
}

// scanFonts lists the installed monospace fonts. Files are read through
// ReaderAt: only the tables needed are loaded, not the whole font.
func scanFonts() []fontEntry {
	best := map[string]fontEntry{}
	var buf sfnt.Buffer
	for _, dir := range fontDirs() {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".ttf" && ext != ".otf" && ext != ".ttc" {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			var ft *sfnt.Font
			if ext == ".ttc" {
				col, err := sfnt.ParseCollectionReaderAt(f)
				if err != nil {
					return nil
				}
				ft, err = col.Font(0)
				if err != nil {
					return nil
				}
			} else if ft, err = sfnt.ParseReaderAt(f); err != nil {
				return nil
			}
			if !isMonospace(ft, &buf) {
				return nil
			}
			family, _ := ft.Name(&buf, sfnt.NameIDTypographicFamily)
			if family == "" {
				family, _ = ft.Name(&buf, sfnt.NameIDFamily)
			}
			style, _ := ft.Name(&buf, sfnt.NameIDTypographicSubfamily)
			if style == "" {
				style, _ = ft.Name(&buf, sfnt.NameIDSubfamily)
			}
			if family == "" || strings.HasPrefix(family, ".") {
				return nil
			}
			e := fontEntry{Family: family, Style: style, Path: path}
			if old, ok := best[family]; !ok || styleRank(style) < styleRank(old.Style) {
				best[family] = e
			}
			return nil
		})
	}
	out := make([]fontEntry, 0, len(best))
	for _, e := range best {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b fontEntry) int { return strings.Compare(strings.ToLower(a.Family), strings.ToLower(b.Family)) })
	return out
}

func styleRank(s string) int {
	switch strings.ToLower(s) {
	case "regular", "book", "normal":
		return 0
	case "retina", "medium":
		return 1
	}
	return 2
}

func isMonospace(f *sfnt.Font, buf *sfnt.Buffer) bool {
	var ref fixed.Int26_6
	for i, r := range "iMW.m" {
		gi, err := f.GlyphIndex(buf, r)
		if err != nil || gi == 0 {
			return false
		}
		adv, err := f.GlyphAdvance(buf, gi, fixed.I(1000), font.HintingNone)
		if err != nil {
			return false
		}
		if i == 0 {
			ref = adv
		} else if adv != ref {
			return false
		}
	}
	return true
}

// symbolsScan is the Nerd Font search, done once per process, in the
// background.
var symbolsScan struct {
	once sync.Once
	done chan struct{}
	path string
}

// symbolsWait bounds how long the first call waits: on a slow disk, the
// window starts without icons rather than frozen, and later loadFonts calls
// pick up the result once it has arrived.
const symbolsWait = 500 * time.Millisecond

// PrefetchSymbolsFont starts the Nerd Font search in the background while
// the window opens: findSymbolsFont then only has to read the result.
func PrefetchSymbolsFont() {
	symbolsScan.once.Do(func() {
		symbolsScan.done = make(chan struct{})
		go func() {
			symbolsScan.path = findSymbolsFontIn(fontDirs())
			close(symbolsScan.done)
		}()
	})
}

// findSymbolsFont returns the Nerd Font found by PrefetchSymbolsFont
// (started here if needed), or an empty string if there is none.
func findSymbolsFont() string {
	PrefetchSymbolsFont()
	select {
	case <-symbolsScan.done:
		return symbolsScan.path
	case <-time.After(symbolsWait):
		return ""
	}
}

// maxFontDepth bounds how deep the search goes into font directories: on
// Linux, fonts are sorted by format or by family (/usr/share/fonts/TTF/,
// ~/.local/share/fonts/NerdFonts/JetBrainsMono/).
const maxFontDepth = 4

// findSymbolsFontIn looks for a Nerd Font in dirs, preferably a "Mono"
// variant (icons on a single cell). Only file names are read, so it is
// fast.
func findSymbolsFontIn(dirs []string) string {
	var best string
	bestRank := 99
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if rel, _ := filepath.Rel(dir, path); rel != "." && strings.Count(rel, string(filepath.Separator)) >= maxFontDepth-1 {
					return fs.SkipDir
				}
				return nil
			}
			n := d.Name()
			if !strings.Contains(n, "NerdFont") || !strings.Contains(n, "Regular") {
				return nil
			}
			rank := 3
			switch {
			case strings.HasPrefix(n, "SymbolsNerdFontMono"):
				rank = 0
			case strings.Contains(n, "NerdFontMono-"):
				rank = 1
			case strings.HasPrefix(n, "SymbolsNerdFont"):
				rank = 2
			}
			if rank < bestRank {
				best, bestRank = path, rank
			}
			return nil
		})
	}
	return best
}
