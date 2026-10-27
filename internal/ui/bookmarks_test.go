package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBookmarks(t *testing.T, dir, profile, data string) {
	t.Helper()
	p := filepath.Join(dir, profile)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "Bookmarks"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadBookmarks(t *testing.T) {
	brave, chrome := t.TempDir(), t.TempDir()
	writeBookmarks(t, brave, "Default", `{"roots": {
		"bookmark_bar": {"type": "folder", "name": "Bar", "children": [
			{"type": "url", "name": "Go", "url": "https://go.dev/"},
			{"type": "folder", "name": "Work", "children": [
				{"type": "folder", "name": "CI", "children": [{"type": "url", "name": "Builds", "url": "https://ci.example.org/builds"}]},
				{"type": "url", "name": "Script", "url": "javascript:alert(1)"}
			]}
		]},
		"other": {"type": "folder", "name": "Other", "children": [{"type": "url", "name": "Notes", "url": "file:///home/me/notes.html"}]}
	}}`)
	writeBookmarks(t, brave, "Profile 1", `{"roots": {"bookmark_bar": {"type": "folder", "children": [
		{"type": "url", "name": "Go again", "url": "https://go.dev/"},
		{"type": "url", "name": "", "url": "https://pkg.go.dev/"}
	]}}}`)
	writeBookmarks(t, chrome, "Default", `not json`)
	got := loadBookmarks([]string{brave, chrome, filepath.Join(t.TempDir(), "missing")})
	want := []bookmark{
		{"Go", "https://go.dev/", ""},
		{"Builds", "https://ci.example.org/builds", "Work/CI"},
		{"Notes", "file:///home/me/notes.html", ""},
		{"", "https://pkg.go.dev/", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	cmds := bookmarkCommands(got)
	if cmds[1].label != "Builds" || cmds[1].hint != "Work/CI · ci.example.org" || cmds[1].action != "https://ci.example.org/builds" {
		t.Errorf("command %+v", cmds[1])
	}
	if cmds[3].label != "https://pkg.go.dev/" {
		t.Errorf("unnamed bookmark shown as %q", cmds[3].label)
	}
}

// TestBrowserPaletteBookmarks: typed text opens as an address, unless the
// selection was moved onto a bookmark; with nothing typed, enter opens the
// selected bookmark.
func TestBrowserPaletteBookmarks(t *testing.T) {
	a, _ := coreApp(t, 1)
	open := func(keys func(p *Palette)) (typed, picked string) {
		a.openPalette("BROWSER", []command{{label: "Go", action: "https://go.dev/"}, {label: "Go packages", action: "https://pkg.go.dev/"}})
		pal := a.palette
		pal.input = func(s string) { typed = s }
		pal.pick = func(c command) { picked = c.action }
		keys(pal)
		return
	}
	if typed, picked := open(func(p *Palette) {
		p.chars([]rune("go"))
		p.key(KeyEvent{Name: "enter"})
	}); typed != "go" || picked != "" {
		t.Errorf("typed text: typed %q picked %q", typed, picked)
	}
	if typed, picked := open(func(p *Palette) {
		p.chars([]rune("go"))
		p.key(KeyEvent{Name: "down"})
		p.key(KeyEvent{Name: "enter"})
	}); typed != "" || picked != "https://go.dev/" {
		t.Errorf("first move: typed %q picked %q", typed, picked)
	}
	if typed, picked := open(func(p *Palette) {
		p.chars([]rune("go"))
		p.key(KeyEvent{Name: "down"})
		p.key(KeyEvent{Name: "down"})
		p.key(KeyEvent{Name: "enter"})
	}); picked != "https://pkg.go.dev/" {
		t.Errorf("second move: typed %q picked %q", typed, picked)
	}
	if typed, picked := open(func(p *Palette) { p.key(KeyEvent{Name: "enter"}) }); picked != "https://go.dev/" {
		t.Errorf("nothing typed: typed %q picked %q", typed, picked)
	}
}

func TestToggleBookmark(t *testing.T) {
	t.Setenv("NEONMUX_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	a, _ := coreApp(t, 1)
	p := NewBrowserPane(a.newID(), "https://go.dev/")
	p.title = "The Go Programming Language"
	a.toggleBookmark(p)
	if !a.isBookmarked("https://go.dev/") {
		t.Fatal("not bookmarked")
	}
	// Read back from the file, as a new window would.
	b, _ := coreApp(t, 1)
	if got := b.savedBookmarks(); len(got) != 1 || got[0] != (savedBookmark{"The Go Programming Language", "https://go.dev/"}) {
		t.Fatalf("saved %+v", got)
	}
	items := b.bookmarkItems([]bookmark{{"Go", "https://go.dev/", ""}, {"Pkg", "https://pkg.go.dev/", "Dev"}})
	if len(items) != 2 || items[0].hint != "★ go.dev" || items[1].label != "Pkg" {
		t.Errorf("items %+v", items)
	}
	a.toggleBookmark(p)
	if a.isBookmarked("https://go.dev/") {
		t.Fatal("still bookmarked")
	}
	data, err := os.ReadFile(bookmarksPath())
	if err != nil || string(data) != "[]\n" {
		t.Errorf("file %q %v", data, err)
	}
}
