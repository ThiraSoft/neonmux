package cdp

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// commands are the browser names looked up in PATH, by preference.
// Flatpak and Snap builds are left out: their sandbox does not pass file
// descriptors 3 and 4 through, which --remote-debugging-pipe needs.
var commands = []string{
	"brave", "brave-browser", "brave-browser-stable",
	"chromium", "chromium-browser",
	"google-chrome", "google-chrome-stable", "chrome",
	"microsoft-edge", "microsoft-edge-stable",
	"vivaldi", "vivaldi-stable",
}

// macApps are the macOS bundles, in the same order.
var macApps = []string{
	"Brave Browser.app/Contents/MacOS/Brave Browser",
	"Chromium.app/Contents/MacOS/Chromium",
	"Google Chrome.app/Contents/MacOS/Google Chrome",
	"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	"Vivaldi.app/Contents/MacOS/Vivaldi",
}

// ErrNotFound says that no browser of the Chromium family was found.
var ErrNotFound = errors.New("no Chromium-family browser found (Brave, Chromium, Chrome, Edge, Vivaldi): set \"browser\" in the config")

// Find returns the command of the browser to launch. configured (the
// "browser" key of the config) wins: a path, or a command with its
// arguments. Otherwise the first browser found, then the Chromium that
// Playwright downloads, as a last resort.
func Find(configured string) ([]string, error) {
	if f := strings.Fields(configured); len(f) > 0 {
		bin, err := exec.LookPath(expandHome(f[0]))
		if err != nil {
			return nil, err
		}
		return append([]string{bin}, f[1:]...), nil
	}
	for _, c := range commands {
		if bin, err := exec.LookPath(c); err == nil {
			return []string{bin}, nil
		}
	}
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		for _, app := range macApps {
			for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
				if p := filepath.Join(dir, app); isExec(p) {
					return []string{p}, nil
				}
			}
		}
	}
	if p := playwright(); p != "" {
		return []string{p}, nil
	}
	return nil, ErrNotFound
}

// playwright returns the most recent full Chromium of the Playwright
// cache, if any.
func playwright() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	pattern := filepath.Join(home, ".cache/ms-playwright/chromium-*/chrome-linux*/chrome")
	if runtime.GOOS == "darwin" {
		pattern = filepath.Join(home, "Library/Caches/ms-playwright/chromium-*/chrome-mac*/*.app/Contents/MacOS/*")
	}
	found, _ := filepath.Glob(pattern)
	slices.Sort(found)
	for i := len(found) - 1; i >= 0; i-- {
		if isExec(found[i]) {
			return found[i]
		}
	}
	return ""
}

func isExec(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}
