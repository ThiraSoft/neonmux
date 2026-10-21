package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Config is read from ~/.config/neonmux/config.json. Missing fields keep
// their default value. Changes made from the app (theme, font, size, power
// mode) are written back to it, key by key.
type Config struct {
	Theme        string            `json:"theme"`
	Font         string            `json:"font"`      // iosevka, pixel, terminus, gomono or the path of a .ttf/.otf/.ttc
	FontSize     float64           `json:"font_size"` // in points
	UIFont       string            `json:"ui_font"`   // interface font, same values as font
	UIFontSize   float64           `json:"ui_font_size"`
	SymbolsFont  string            `json:"symbols_font"` // auto, none or the path of a Nerd Font
	Shell        string            `json:"shell"`
	Leader       string            `json:"leader"`
	Mod          string            `json:"mod"`
	OptionAsMeta bool              `json:"option_as_meta"`
	Power        string            `json:"power"`      // eco, balanced, max
	Background   string            `json:"background"` // auto, none or a background from fx.Backgrounds
	Animate      bool              `json:"animate_background"`
	Scrollback   int               `json:"scrollback"`
	MaxPanes     int               `json:"max_panes"`
	CopyOnSelect bool              `json:"copy_on_select"`
	CursorBlink  bool              `json:"cursor_blink"`
	TypingFX     bool              `json:"typing_fx"`
	Battery      bool              `json:"battery"` // battery level in the status bar (hidden without a battery)
	Editor       string            `json:"editor"`
	Decorations  bool              `json:"decorations"`        // macOS title bar
	FocusBorder  float64           `json:"focus_border"`       // focus border thickness, in pixels (0 to 8)
	SoloBorder   bool              `json:"focus_border_alone"` // focus border even when the pane is alone on screen
	FocusRest    float64           `json:"focus_border_rest"`  // border intensity once dimmed, in % (100: it stays full)
	FocusFade    float64           `json:"focus_border_fade"`  // how long the dimming takes, in seconds (0 to 10)
	PaneOpacity  float64           `json:"pane_opacity"`       // pane background opacity, in % (0: the theme's)
	Persist      bool              `json:"persist"`            // terminals kept by the daemon
	RestoreCmds  []string          `json:"restore_commands"`   // relaunched after a restart
	Prewarm      bool              `json:"prewarm"`            // a shell prewarmed for the next terminal
	Bindings     map[string]string `json:"bindings"`           // key -> action, overriding the defaults

	// The border of an agent that finishes flashes (agent.go).
	AgentFlash         bool    `json:"agent_flash"`
	AgentFlashCount    int     `json:"agent_flash_count"`    // 1 to 10
	AgentFlashDuration float64 `json:"agent_flash_duration"` // each flash, in seconds (0.1 to 5)
}

// DefaultConfig returns the default settings.
func DefaultConfig() Config {
	mod := "alt"
	if runtime.GOOS == "darwin" {
		mod = "cmd"
	}
	return Config{
		Theme:        theme.Default,
		Font:         "iosevka",
		FontSize:     16,
		UIFont:       "terminus",
		UIFontSize:   12,
		SymbolsFont:  "auto",
		Leader:       "ctrl+space",
		Mod:          mod,
		Power:        "balanced",
		Background:   "stars", // "auto" picks each theme's own background
		Animate:      true,
		Scrollback:   10000,
		MaxPanes:     4,
		CopyOnSelect: true,
		CursorBlink:  true,
		Battery:      true,
		Decorations:  true,
		FocusBorder:  1,
		FocusRest:    50,
		FocusFade:    0.35,
		Persist:      true,
		Prewarm:      true,

		AgentFlash:         true,
		AgentFlashCount:    2,
		AgentFlashDuration: 0.5,
	}
}

// ConfigPath returns the path of the config file ($NEONMUX_CONFIG if it is
// set).
func ConfigPath() string {
	if p := os.Getenv("NEONMUX_CONFIG"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "neonmux", "config.json")
}

// LoadConfig reads the configuration (the defaults if the file does not
// exist). warn reports an unknown key or a value clamped to its bounds,
// neither of which prevents loading. An unreadable file is remembered:
// nothing will be written to it until it reads cleanly again (see
// saveConfig).
func LoadConfig() (c Config, warn string, err error) {
	data, err := os.ReadFile(ConfigPath())
	switch {
	case os.IsNotExist(err):
		c, err = DefaultConfig(), nil
	case err != nil:
		c = DefaultConfig()
	default:
		c, warn, err = ParseConfig(data)
	}
	noteConfigRead(c, err)
	return c, warn, err
}

// maxScrollback bounds a terminal's scrollback. On every reattach, the
// daemon sends the terminal's snapshot in a single frame, rejected above
// 64 MiB. In the worst case (one color per cell), a scrollback cell costs
// about 12.5 bytes (measured): 15,000 lines of 200 columns then take
// 36 MiB, and the limit is only reached around 350 columns. An ordinary
// log line costs about a hundred bytes: the bound only gets in the way in
// extreme cases, and also keeps each pane's memory reasonable (the window
// holds a copy).
const maxScrollback = 15000

// ParseConfig reads a JSON configuration. On a syntax error, the offending
// line is reported. An unknown key (a typo) or a scrollback that is too
// long are reported in warn without blocking the rest. An empty file means
// the default config.
func ParseConfig(data []byte) (c Config, warn string, err error) {
	c = DefaultConfig()
	if len(bytes.TrimSpace(data)) == 0 {
		return c, "", nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c); err != nil {
		if !strings.HasPrefix(err.Error(), "json: unknown field") {
			return DefaultConfig(), "", withLine(data, err)
		}
		warn = "unknown key " + strings.TrimPrefix(err.Error(), "json: unknown field ")
		c = DefaultConfig()
		if err = json.Unmarshal(data, &c); err != nil {
			return DefaultConfig(), "", withLine(data, err)
		}
	}
	c.MaxPanes = min(max(c.MaxPanes, 1), 9)
	c.FocusBorder = min(max(c.FocusBorder, 0), 8)
	c.FocusRest = min(max(c.FocusRest, 0), 100)
	c.FocusFade = min(max(c.FocusFade, 0), 10)
	c.PaneOpacity = min(max(c.PaneOpacity, 0), 100)
	c.AgentFlashCount = min(max(c.AgentFlashCount, 1), 10)
	c.AgentFlashDuration = min(max(c.AgentFlashDuration, 0.1), 5)
	if c.FontSize <= 0 {
		c.FontSize = 14
	}
	if c.UIFontSize <= 0 {
		c.UIFontSize = c.FontSize
	}
	if c.Scrollback > maxScrollback {
		c.Scrollback = maxScrollback
		warn = joinWarn(warn, "scrollback capped at "+itoa(maxScrollback)+" lines, the maximum")
	}
	c.Scrollback = max(c.Scrollback, 0)
	return c, warn, nil
}

func joinWarn(warn, more string) string {
	if warn == "" {
		return more
	}
	return warn + "; " + more
}

// paneAlpha returns the pane background opacity: pane_opacity if it is
// set, otherwise the theme's.
func (c Config) paneAlpha(themeAlpha float32) float32 {
	if c.PaneOpacity <= 0 {
		return themeAlpha
	}
	return float32(c.PaneOpacity / 100)
}

func withLine(data []byte, err error) error {
	var off int64 = -1
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &se):
		off = se.Offset
	case errors.As(err, &te):
		off = te.Offset
	}
	if off < 0 {
		return err
	}
	// Offset counts the bytes read, offending character included.
	line := bytes.Count(data[:min(max(int(off)-1, 0), len(data))], []byte("\n")) + 1
	return &LineError{Line: line, Err: err}
}

// LineError is a syntax error located in the config file.
type LineError struct {
	Line int
	Err  error
}

func (e *LineError) Error() string { return fmt.Sprintf("line %d: %v", e.Line, e.Err) }
func (e *LineError) Unwrap() error { return e.Err }

// cfgFile remembers what the window knows about the config file. It all
// happens on the main thread: LoadConfig before the window, then
// saveConfig and reloadConfig.
var cfgFile struct {
	// base is the reference config: the file's, plus the command-line
	// options. saveConfig only writes what differs from it, so an option
	// (-theme, -font…) never becomes permanent.
	base Config
	set  bool
	// font is the requested font: loadFonts replaces a.cfg.Font with the
	// pixel font when the file cannot be found, a fallback that must never
	// be saved.
	font string
	// broken is the error from the last read. The file then holds the
	// user's settings, which the in-memory config (the defaults) would
	// overwrite: nothing is written to it until it reads cleanly again.
	broken error
}

// noteConfigRead remembers the result of reading the file.
func noteConfigRead(c Config, err error) {
	cfgFile.broken = err
	if err == nil {
		cfgFile.base, cfgFile.font, cfgFile.set = c, c.Font, true
	}
}

// SetConfigBase sets the reference config: the one read by LoadConfig,
// plus the command-line options.
func SetConfigBase(c Config) {
	cfgFile.base, cfgFile.font, cfgFile.set = c, c.Font, true
}

// configErrMsg describes an unreadable config file in a few words (for a
// toast).
func configErrMsg(err error) string {
	var le *LineError
	if errors.As(err, &le) {
		return "config.json line " + itoa(le.Line) + " is invalid"
	}
	return "config.json is unreadable"
}

// configField is a top-level key of the file, with its value as written
// there.
type configField struct {
	key string
	val json.RawMessage
}

// readFields splits a JSON object into top-level keys, in file order. An
// empty file has no keys.
func readFields(data []byte) ([]configField, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("config.json must contain a JSON object")
	}
	var fields []configField
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, withLine(data, err)
		}
		key, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, withLine(data, err)
		}
		fields = append(fields, configField{key, v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, withLine(data, err)
	}
	return fields, nil
}

// fieldsOf returns the keys of a config, in struct order.
func fieldsOf(c Config) []configField {
	data, _ := json.Marshal(c)
	f, _ := readFields(data)
	return f
}

// changedFields returns the keys of cur that differ from base.
func changedFields(base, cur Config) []configField {
	b, c := fieldsOf(base), fieldsOf(cur)
	var out []configField
	for i, f := range c {
		if !bytes.Equal(f.val, b[i].val) {
			out = append(out, f)
		}
	}
	return out
}

// mergeFields replaces the keys of changes in fields, and appends those
// that are not there yet.
func mergeFields(fields, changes []configField) []configField {
	out := slices.Clone(fields)
	for _, ch := range changes {
		found := false
		for i := range out {
			if out[i].key == ch.key {
				out[i].val, found = ch.val, true
			}
		}
		if !found {
			out = append(out, ch)
		}
	}
	return out
}

// encodeFields writes the keys as JSON indented by two spaces.
func encodeFields(fields []configField) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.key)
		b.WriteString("\n  ")
		b.Write(k)
		b.WriteString(": ")
		if json.Indent(&b, f.val, "  ", "  ") != nil {
			b.Write(f.val)
		}
	}
	if len(fields) > 0 {
		b.WriteByte('\n')
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// saveFields writes changes on top of the JSON on disk: the other keys
// (those from a newer version, for example) and their order stay. Returns
// the content written.
func saveFields(changes []configField) ([]byte, error) {
	p := ConfigPath()
	data, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	fields, err := readFields(data)
	if err != nil {
		return nil, err
	}
	out := encodeFields(mergeFields(fields, changes))
	return out, writeConfigFile(p, out)
}

// Save writes the whole configuration (unknown keys in the file stay) and
// returns the content written.
func (c Config) Save() ([]byte, error) { return saveFields(fieldsOf(c)) }

// saveChanges only writes the keys of c that differ from base. Nothing is
// written (nil, nil) if nothing changed.
func (c Config) saveChanges(base Config) ([]byte, error) {
	changes := changedFields(base, c)
	if len(changes) == 0 {
		return nil, nil
	}
	return saveFields(changes)
}

// configTarget returns the file to replace: the target of a symbolic link
// (dotfiles, stow, home-manager), even one not created yet.
func configTarget(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	if t, err := os.Readlink(p); err == nil {
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(p), t)
		}
		return t
	}
	return p
}

// writeConfigFile replaces the file in one go: the content is written next
// to it, then renamed. A crash in the middle of the write leaves the old
// file intact. A symbolic link is followed: its target is replaced, the
// link stays. The existing file's permissions are kept.
func writeConfigFile(p string, data []byte) error {
	target := configTarget(p)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(target)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, target)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// createConfigFile creates the file with every key of fields, only if it
// does not exist yet (O_EXCL): an existing file, even an unreadable one, is
// never replaced. Returns nil, nil if it already existed.
func createConfigFile(fields []configField) ([]byte, error) {
	p := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data := encodeFields(fields)
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return data, err
}
