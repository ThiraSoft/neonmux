package ui

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ThiraSoft/neonmux/internal/cdp"
	"github.com/ThiraSoft/neonmux/internal/server"
)

// browserHost shares one headless browser between the browser panes. It
// starts with the first pane and quits with the last one. Its fields are
// only touched on the main thread.
type browserHost struct {
	b        *cdp.Browser
	ua       string // user agent without "Headless", which some sites refuse
	starting bool
	users    int // panes holding the browser (acquire, release)
	waiters  []func(*cdp.Browser, error)
	pages    map[string]*BrowserPane // by target id
	popups   map[string]string       // popup target id -> target id of its opener
}

// targetInfo is the part of Target.TargetInfo neonmux reads.
type targetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	OpenerID string `json:"openerId"`
}

// acquireBrowser hands the browser to fn, starting it first if needed.
// When fn gets a browser, it holds it until releaseBrowser.
func (a *App) acquireBrowser(fn func(*cdp.Browser, error)) {
	h := &a.web
	if h.b != nil {
		h.users++
		fn(h.b, nil)
		return
	}
	h.waiters = append(h.waiters, func(b *cdp.Browser, err error) {
		if err == nil {
			h.users++
		}
		fn(b, err)
	})
	if h.starting {
		return
	}
	h.starting = true
	configured := a.cfg.Browser
	go func() {
		b, ua, err := launchBrowser(configured, a)
		a.post(func() {
			h.starting = false
			if err == nil {
				h.b, h.ua = b, ua
				if h.pages == nil {
					h.pages, h.popups = map[string]*BrowserPane{}, map[string]string{}
				}
				go func() {
					<-b.Done()
					a.post(func() { a.browserGone(b) })
				}()
			}
			ws := h.waiters
			h.waiters = nil
			for _, w := range ws {
				w(b, err)
			}
		})
	}()
}

func launchBrowser(configured string, a *App) (*cdp.Browser, string, error) {
	argv, err := cdp.Find(configured)
	if err != nil {
		return nil, "", err
	}
	b, err := cdp.Launch(argv, filepath.Join(server.StateDir(), "browser"), func(method string, params json.RawMessage) {
		switch method {
		case "Target.targetCreated", "Target.targetInfoChanged":
			var ev struct {
				TargetInfo targetInfo `json:"targetInfo"`
			}
			// The browser's own targets (service workers, its UI) are
			// many: only pages go to the main thread.
			if json.Unmarshal(params, &ev) == nil && ev.TargetInfo.Type == "page" {
				a.post(func() { a.browserTarget(ev.TargetInfo) })
			}
		}
	})
	if err != nil {
		return nil, "", err
	}
	var v struct {
		UserAgent string `json:"userAgent"`
	}
	if err := b.Call("Browser.getVersion", nil, &v); err != nil {
		b.Close()
		return nil, "", err
	}
	if err := b.Call("Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		b.Close()
		return nil, "", err
	}
	return b, strings.Replace(v.UserAgent, "HeadlessChrome", "Chrome", 1), nil
}

// browserTarget follows the title and address of the pages, and turns a
// popup (target="_blank", window.open) into a navigation of its opener:
// a pane shows one page.
func (a *App) browserTarget(t targetInfo) {
	h := &a.web
	if p := h.pages[t.TargetID]; p != nil {
		if p.title != t.Title || (t.URL != "" && p.url != t.URL) {
			p.title = t.Title
			if t.URL != "" {
				p.url = t.URL
			}
			p.dirty = true
			a.layoutDirty = true
		}
		return
	}
	if h.b == nil {
		return
	}
	opener := t.OpenerID
	if opener == "" {
		opener = h.popups[t.TargetID]
	}
	p := h.pages[opener]
	if p == nil {
		return
	}
	if t.URL == "" || t.URL == "about:blank" {
		// The address comes in a later Target.targetInfoChanged.
		h.popups[t.TargetID] = opener
		return
	}
	delete(h.popups, t.TargetID)
	h.b.Send("Target.closeTarget", map[string]any{"targetId": t.TargetID})
	p.navigate(t.URL)
}

// releaseBrowser lets go of b, and quits it once no pane holds it. A
// browser that already went away is not counted any more.
func (a *App) releaseBrowser(b *cdp.Browser) {
	h := &a.web
	if b == nil || h.b != b {
		return
	}
	if h.users--; h.users > 0 {
		return
	}
	h.users = 0
	h.b = nil
	clear(h.popups)
	go b.Close()
}

// browserGone tells the panes that the browser went away (crash, killed).
func (a *App) browserGone(b *cdp.Browser) {
	h := &a.web
	if h.b != b {
		return
	}
	h.b, h.users = nil, 0
	err := b.Err()
	for id, p := range h.pages {
		delete(h.pages, id)
		p.lost(err)
	}
	clear(h.popups)
	a.needRedraw = true
}

// shutdownBrowser closes the browser when the window quits.
func (a *App) shutdownBrowser() {
	if b := a.web.b; b != nil {
		a.web.b, a.web.users = nil, 0
		b.Close()
	}
}

// promptBrowser asks for an address in the palette. With p, the address
// goes to that pane; otherwise a new pane opens.
func (a *App) promptBrowser(p *BrowserPane) {
	a.openPalette("BROWSER", nil)
	pal := a.palette
	pal.status = "address or search   ↑↓ bookmarks   enter open   esc cancel"
	if p != nil {
		pal.query = []rune(p.url)
	}
	open := func(u string) {
		if u == "" {
			return
		}
		if p != nil && a.hasPane(p) {
			p.navigate(u)
			return
		}
		a.addPane(NewBrowserPane(a.newID(), u))
	}
	pal.input = func(s string) { open(browserURL(s)) }
	pal.pick = func(c command) { open(c.action) }
	dirs := bookmarkDirs()
	go func() {
		cmds := bookmarkCommands(loadBookmarks(dirs))
		a.post(func() {
			if a.palette == pal && len(cmds) > 0 {
				pal.refresh(cmds)
			}
		})
	}()
}

func (a *App) hasPane(p Pane) bool {
	for _, q := range a.panes {
		if q == p {
			return true
		}
	}
	return false
}

// browserURL turns what was typed into an address: a URL as it is, a
// path into file://, a bare host into https:// (http:// for localhost),
// anything else into a search.
func browserURL(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return ""
	case strings.HasPrefix(s, "/"), strings.HasPrefix(s, "~/"), strings.HasPrefix(s, "./"):
		p := s
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			if home, err := os.UserHomeDir(); err == nil {
				p = filepath.Join(home, rest)
			}
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return (&url.URL{Scheme: "file", Path: p}).String()
	case strings.Contains(s, "://"), strings.HasPrefix(s, "about:"), strings.HasPrefix(s, "data:"):
		return s
	}
	if !strings.ContainsAny(s, " \t") {
		host := s
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		name := host
		if i := strings.LastIndexByte(name, ':'); i >= 0 {
			name = name[:i]
		}
		switch {
		case name == "localhost", strings.HasPrefix(name, "127."), name == "[::1]":
			return "http://" + s
		case strings.Contains(name, ".") && !strings.HasPrefix(name, ".") && !strings.HasSuffix(name, "."):
			return "https://" + s
		}
	}
	return "https://duckduckgo.com/?q=" + url.QueryEscape(s)
}
