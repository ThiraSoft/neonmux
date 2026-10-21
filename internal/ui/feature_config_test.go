package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/fx"
	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// featureApp sets up an app without a window: the post queue, the pacer
// and the background are enough for the actions tested here.
func featureApp() *App {
	return &App{cfg: DefaultConfig(), theme: theme.Get("neon-tokyo"), bg: fx.NewBackground(nil),
		posted: make(chan func(), 64), pacer: fx.NewPacer(60)}
}

// drain runs the posted functions until done is true, or fails after d.
func drain(t *testing.T, a *App, d time.Duration, done func() bool) {
	t.Helper()
	deadline := time.After(d)
	for !done() {
		select {
		case fn := <-a.posted:
			fn()
		case <-deadline:
			t.Fatal("timed out")
		}
	}
}

// configEnv isolates the test's config (temporary HOME and NEONMUX_CONFIG)
// and writes content to the file if it is not empty.
func configEnv(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	p := filepath.Join(dir, "config.json")
	t.Setenv("NEONMUX_CONFIG", p)
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// startApp mimics the launch: reading the config, the command-line options
// (opts), then the window.
func startApp(t *testing.T, opts func(*Config)) *App {
	t.Helper()
	cfg, _, _ := LoadConfig()
	if opts != nil {
		opts(&cfg)
	}
	SetConfigBase(cfg)
	a := featureApp()
	a.cfg = cfg
	a.theme = theme.Get(cfg.Theme)
	return a
}

func lastToast(a *App) string {
	if len(a.toasts) == 0 {
		return ""
	}
	return a.toasts[len(a.toasts)-1].text
}

func readConfigFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A config.json with a syntax error must never be overwritten by the
// default settings: neither by a toggle nor by cmd+, (config.open).
func TestConfigBrokenNeverOverwritten(t *testing.T) {
	broken := "{\n  \"theme\": \"abyss\",\n  \"typing_fx\": tru\n}\n"
	p := configEnv(t, broken)
	if _, _, err := LoadConfig(); err == nil {
		t.Fatal("expected a syntax error")
	}
	a := startApp(t, nil)
	a.do("fx.typing")
	if got := readConfigFile(t, p); got != broken {
		t.Fatalf("file rewritten despite the error:\n%s", got)
	}
	if msg := lastToast(a); !strings.Contains(msg, "line 3") || !strings.Contains(msg, "nothing saved") {
		t.Errorf("toast %q", msg)
	}
	a.createConfig()
	if got := readConfigFile(t, p); got != broken {
		t.Fatalf("config.open rewrote the file:\n%s", got)
	}

	// Once fixed, it is read again on the next change and becomes the reference again.
	fixed := `{"theme": "abyss", "typing_fx": false, "future_key": 7}`
	if err := os.WriteFile(p, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	a.reloadConfig()
	if a.theme.Name != "abyss" {
		t.Fatalf("fixed config not applied: %s", a.theme.Name)
	}
	a.do("fx.typing")
	var m map[string]any
	if err := json.Unmarshal([]byte(readConfigFile(t, p)), &m); err != nil || m["typing_fx"] != true || m["future_key"] != 7.0 {
		t.Fatalf("after the fix: %v %v", m, err)
	}
}

// Reloaded with a syntax error, the file stays protected.
func TestConfigReloadBrokenRefusesWrites(t *testing.T) {
	p := configEnv(t, `{"theme": "abyss"}`)
	a := startApp(t, nil)
	broken := `{"theme": "matrix",`
	if err := os.WriteFile(p, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	a.reloadConfig()
	a.do("fx.typing")
	if got := readConfigFile(t, p); got != broken {
		t.Fatalf("file rewritten after a failed reload:\n%s", got)
	}
}

// config.open creates a complete file if it is missing, and never replaces
// an existing one.
func TestConfigOpenCreatesOnlyIfMissing(t *testing.T) {
	p := configEnv(t, "")
	a := startApp(t, func(c *Config) { c.Theme = "matrix" }) // -theme matrix
	a.createConfig()
	c, warn, err := ParseConfig([]byte(readConfigFile(t, p)))
	if err != nil || warn != "" || c.Theme != theme.Default || !strings.Contains(readConfigFile(t, p), `"scrollback"`) {
		t.Fatalf("file created: %+v %q %v", c, warn, err)
	}
	mine := `{"theme": "acid"}`
	os.WriteFile(p, []byte(mine), 0o644)
	a.createConfig()
	if got := readConfigFile(t, p); got != mine {
		t.Fatalf("existing file replaced:\n%s", got)
	}
}

// A save only writes the changed keys, on top of the JSON on disk: unknown
// keys (from a newer version) and the order stay.
func TestConfigSaveKeepsUnknownKeys(t *testing.T) {
	p := configEnv(t, "{\n  \"zeta_future\": {\"a\": [1, 2]},\n  \"theme\": \"abyss\",\n  \"scrollback\": 5000\n}\n")
	a := startApp(t, nil)
	a.do("fx.typing")
	got := readConfigFile(t, p)
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 4 || string(m["typing_fx"]) != "true" || string(m["theme"]) != `"abyss"` || string(m["scrollback"]) != "5000" {
		t.Fatalf("keys written:\n%s", got)
	}
	if !strings.Contains(got, `"zeta_future"`) || strings.Index(got, "zeta_future") > strings.Index(got, "theme") {
		t.Fatalf("unknown key lost or moved:\n%s", got)
	}
}

// The -theme, -font and -size options only apply to this launch,
// like the fallback to the pixel font.
func TestConfigFlagsStayTemporary(t *testing.T) {
	p := configEnv(t, `{"theme": "abyss", "font": "/missing.ttf", "font_size": 13}`)
	a := startApp(t, func(c *Config) {
		c.Theme, c.FontSize = "matrix", 20
	})
	a.cfg.Font = gfx.FontPixel // loadFonts fallback: font not found
	a.do("fx.typing")
	c, _, err := ParseConfig([]byte(readConfigFile(t, p)))
	if err != nil || !c.TypingFX || c.Theme != "abyss" || c.FontSize != 13 || c.Font != "/missing.ttf" {
		t.Fatalf("options became permanent: %+v %v", c, err)
	}
	// A change made in the app, however, is saved, even one that goes back
	// to the option's value.
	a.setTheme(theme.Get("acid"))
	a.setTheme(theme.Get("matrix"))
	if c, _, _ := ParseConfig([]byte(readConfigFile(t, p))); c.Theme != "matrix" {
		t.Fatalf("theme %q, want matrix", c.Theme)
	}
}

// The write is atomic (a temporary file, renamed) and follows a dotfiles
// symbolic link instead of replacing it with a file.
func TestConfigSaveAtomicThroughSymlink(t *testing.T) {
	p := configEnv(t, "")
	dots := filepath.Join(t.TempDir(), "dotfiles")
	os.MkdirAll(dots, 0o755)
	target := filepath.Join(dots, "neonmux.json")
	os.WriteFile(target, []byte(`{"theme": "abyss"}`), 0o600)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	a := startApp(t, nil)
	old, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	a.do("fx.typing")
	if st, err := os.Lstat(p); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v", err)
	}
	if c, _, _ := ParseConfig([]byte(readConfigFile(t, target))); !c.TypingFX || c.Theme != "abyss" {
		t.Fatalf("target: %+v", c)
	}
	if st, _ := os.Stat(target); st.Mode().Perm() != 0o600 {
		t.Errorf("permissions %v, want 0600 as before", st.Mode().Perm())
	}
	// The old file, still open, is intact: it was replaced, not truncated
	// and then rewritten.
	data := make([]byte, 64)
	n, _ := old.Read(data)
	if string(data[:n]) != `{"theme": "abyss"}` {
		t.Fatalf("old content modified in place: %q", data[:n])
	}
	if ents, _ := os.ReadDir(dots); len(ents) != 1 {
		t.Errorf("temporary files left behind: %v", ents)
	}
}

// The scrollback is bounded: beyond that, a terminal's snapshot gets close
// to the daemon's maximum frame size. Going over is reported like an
// unknown key.
func TestScrollbackBounded(t *testing.T) {
	c, warn, err := ParseConfig([]byte(`{"scrollback": 1000000, "themme": "x"}`))
	if err != nil || c.Scrollback != maxScrollback || !strings.Contains(warn, "scrollback") || !strings.Contains(warn, "themme") {
		t.Fatalf("%d %q %v", c.Scrollback, warn, err)
	}
	if c, warn, _ := ParseConfig([]byte(`{"scrollback": 12000}`)); c.Scrollback != 12000 || warn != "" {
		t.Fatalf("reasonable value: %d %q", c.Scrollback, warn)
	}
	if c, _, _ := ParseConfig([]byte(`{"scrollback": -3}`)); c.Scrollback != 0 {
		t.Fatalf("negative: %d", c.Scrollback)
	}
}
