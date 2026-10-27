package ui

import (
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
