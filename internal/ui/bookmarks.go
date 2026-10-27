package ui

import (
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/ThiraSoft/neonmux/internal/gfx"
)

// Bookmarks of the browsers installed on the machine, offered by the
// browser palette. Chromium-family browsers keep them in plain JSON, in a
// "Bookmarks" file per profile; neonmux only reads it.

// maxBookmarks bounds the list, and maxBookmarksFile the bytes read per
// file.
const (
	maxBookmarks     = 5000
	maxBookmarksFile = 16 << 20
)

type bookmark struct {
	name, url, folder string
}

// bookmarkDirs returns the user data directories of the browsers, Brave
// first, like the browser itself (cdp.Find).
func bookmarkDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	if runtime.GOOS == "darwin" {
		base := filepath.Join(home, "Library", "Application Support")
		return []string{
			filepath.Join(base, "BraveSoftware", "Brave-Browser"),
			filepath.Join(base, "Chromium"),
			filepath.Join(base, "Google", "Chrome"),
			filepath.Join(base, "Microsoft Edge"),
			filepath.Join(base, "Vivaldi"),
		}
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	return []string{
		filepath.Join(cfg, "BraveSoftware", "Brave-Browser"),
		// Flatpak builds cannot be driven, but their bookmarks can be read.
		filepath.Join(home, ".var", "app", "com.brave.Browser", "config", "BraveSoftware", "Brave-Browser"),
		filepath.Join(cfg, "chromium"),
		filepath.Join(home, ".var", "app", "org.chromium.Chromium", "config", "chromium"),
		filepath.Join(cfg, "google-chrome"),
		filepath.Join(cfg, "microsoft-edge"),
		filepath.Join(cfg, "vivaldi"),
	}
}

// loadBookmarks reads the bookmarks of every profile of every browser
// found, once per address.
func loadBookmarks(dirs []string) []bookmark {
	var out []bookmark
	seen := map[string]bool{}
	for _, dir := range dirs {
		// The default profile first, then "Profile 1", "Profile 2"…
		files := []string{filepath.Join(dir, "Default", "Bookmarks")}
		more, _ := filepath.Glob(filepath.Join(dir, "Profile *", "Bookmarks"))
		for _, f := range append(files, more...) {
			for _, b := range readBookmarks(f) {
				if len(out) >= maxBookmarks {
					return out
				}
				if !seen[b.url] {
					seen[b.url] = true
					out = append(out, b)
				}
			}
		}
	}
	return out
}

type bookmarkNode struct {
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	URL      string         `json:"url"`
	Children []bookmarkNode `json:"children"`
}

// readBookmarks parses one Bookmarks file; a missing or broken file gives
// nothing.
func readBookmarks(path string) []bookmark {
	f, err := openRegular(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBookmarksFile))
	if err != nil {
		return nil
	}
	var file struct {
		Roots map[string]json.RawMessage `json:"roots"`
	}
	if json.Unmarshal(data, &file) != nil {
		return nil
	}
	var out []bookmark
	var walk func(n bookmarkNode, folder string)
	walk = func(n bookmarkNode, folder string) {
		switch n.Type {
		case "url":
			if u, err := url.Parse(n.URL); err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "file") {
				out = append(out, bookmark{name: strings.TrimSpace(n.Name), url: n.URL, folder: folder})
			}
		case "folder":
			sub := n.Name
			if folder != "" {
				sub = folder + "/" + n.Name
			}
			for _, c := range n.Children {
				walk(c, sub)
			}
		}
	}
	// The bar first, then the other bookmarks and those of the phone.
	for _, root := range []string{"bookmark_bar", "other", "synced"} {
		var n bookmarkNode
		if raw, ok := file.Roots[root]; ok && json.Unmarshal(raw, &n) == nil {
			for _, c := range n.Children {
				walk(c, "")
			}
		}
	}
	return out
}

// bookmarkCommands turns bookmarks into palette items: the name, and the
// host with the folder as a hint, both searched.
func bookmarkCommands(bs []bookmark) []command {
	cmds := make([]command, 0, len(bs))
	for _, b := range bs {
		label := b.name
		if label == "" {
			label = b.url
		}
		hint := b.url
		if u, err := url.Parse(b.url); err == nil && u.Host != "" {
			hint = strings.TrimPrefix(u.Host, "www.")
		}
		if b.folder != "" {
			hint = b.folder + " · " + hint
		}
		cmds = append(cmds, command{label: label, hint: gfx.Truncate(hint, 36), action: b.url})
	}
	return cmds
}

// neonmux's own bookmarks, added from a browser pane (ctrl+d), live in
// bookmarks.json next to the config: the browsers' files are never
// written, a running browser would overwrite them anyway.

type savedBookmark struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func bookmarksPath() string { return filepath.Join(filepath.Dir(ConfigPath()), "bookmarks.json") }

// savedBookmarks returns neonmux's bookmarks, read once.
func (a *App) savedBookmarks() []savedBookmark {
	h := &a.web
	if !h.marksRead {
		h.marksRead = true
		if data, err := os.ReadFile(bookmarksPath()); err == nil {
			_ = json.Unmarshal(data, &h.marks)
		}
	}
	return h.marks
}

func (a *App) writeBookmarks() {
	data, _ := json.MarshalIndent(a.web.marks, "", "  ")
	path := bookmarksPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	err := os.WriteFile(tmp, append(data, '\n'), 0o600)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		a.toast("bookmarks not saved: "+err.Error(), a.theme.Warn)
	}
}

func (a *App) isBookmarked(u string) bool {
	return slices.ContainsFunc(a.savedBookmarks(), func(b savedBookmark) bool { return b.URL == u })
}

// toggleBookmark adds the page of p to the bookmarks, or removes it.
func (a *App) toggleBookmark(p *BrowserPane) {
	if p.url == "" || p.url == "about:blank" {
		return
	}
	if a.isBookmarked(p.url) {
		a.removeBookmark(p.url)
		a.toast("bookmark removed", a.theme.Dim)
	} else {
		a.web.marks = append(a.savedBookmarks(), savedBookmark{Name: displayName(p), URL: p.url})
		a.writeBookmarks()
		a.toast("bookmarked: "+gfx.Truncate(displayName(p), 40), a.theme.Accent2)
	}
	a.needRedraw = true
}

func (a *App) removeBookmark(u string) {
	a.web.marks = slices.DeleteFunc(a.savedBookmarks(), func(b savedBookmark) bool { return b.URL == u })
	a.writeBookmarks()
}

// bookmarkItems lists neonmux's bookmarks first, marked with a star, then
// those of the browsers that are not already among them.
func (a *App) bookmarkItems(browsers []bookmark) []command {
	saved := a.savedBookmarks()
	bs := make([]bookmark, 0, len(saved)+len(browsers))
	seen := map[string]bool{}
	for _, b := range saved {
		seen[b.URL] = true
		bs = append(bs, bookmark{name: b.Name, url: b.URL})
	}
	cmds := bookmarkCommands(bs)
	for i := range cmds {
		cmds[i].hint = "★ " + cmds[i].hint
	}
	return append(cmds, bookmarkCommands(slices.DeleteFunc(slices.Clone(browsers), func(b bookmark) bool { return seen[b.url] }))...)
}
