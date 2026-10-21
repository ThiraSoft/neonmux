package kata

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

// The harness is a Lua script run by Neovim (nvim --clean -u harness.lua).
// neonmux hands it the missions as a JSON file in a private directory it
// watches; the harness answers through OSC 7777 ("kata=…"), which only the
// kata's own pane listens to.

//go:embed harness.lua
var harnessLua []byte

// HarnessScript returns the Lua harness.
func HarnessScript() []byte { return harnessLua }

// Prepare writes the harness and its launcher into dir.
func Prepare(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "harness.lua"), harnessLua, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "launch.sh"), []byte(LaunchScript(dir)+"\n"), 0o600)
}

// quote1 quotes s for a POSIX shell.
func quote1(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// LaunchCommand is what the user's login shell runs: it only brings the
// user's PATH, and hands over to /bin/sh, since that shell may be fish or
// nushell, which do not read POSIX syntax.
func LaunchCommand(dir string) string {
	return "exec /bin/sh " + quote1(filepath.Join(dir, "launch.sh"))
}

// LaunchScript is the POSIX script of launch.sh: without nvim, it tells
// the pane and stops.
func LaunchScript(dir string) string {
	return `command -v nvim >/dev/null 2>&1 || { printf '\033]7777;kata=missing\007'; exit 127; }; ` +
		"exec nvim --clean -n -i NONE -u " + quote1(filepath.Join(dir, "harness.lua"))
}

var nonce atomic.Int64

// missionFile is what the harness reads.
type missionFile struct {
	ID        string   `json:"id"`
	Nonce     int64    `json:"nonce"`
	Lines     []string `json:"lines"`
	Cursor    [2]int   `json:"cursor"` // 1-based line, 0-based column, as nvim_win_set_cursor
	Target    []string `json:"target"`
	Goal      *[2]int  `json:"goal"`
	Highlight *[3]int  `json:"highlight"`
	Par       string   `json:"par"`
}

// WriteMission hands m to the harness. Every write is a new mission, even
// the same text again (a practice try).
func WriteMission(dir string, m Mission) error {
	f := missionFile{
		ID: m.ID, Nonce: nonce.Add(1), Lines: m.Start, Target: m.Target, Par: m.Par.Keys(),
		Cursor: [2]int{m.Cursor.Line + 1, m.Cursor.Col},
	}
	if f.Target == nil {
		f.Target = m.Start
	}
	if g := m.Goal; g != nil {
		f.Goal = &[2]int{g.Line + 1, g.Col}
	}
	if h := m.Highlight; h != nil {
		f.Highlight = &[3]int{h.Line + 1, h.Col, h.End}
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "mission.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Msg is a message of the harness.
type Msg struct {
	Kind    string // ready, missing, old, loaded, keys, done, quit, badpar
	ID      string
	Version string
	Seq     string // the keys typed, in vim notation (done, quit)
	Keys    int
}

// ParseMsg reads the body of an OSC 7777 sent by the harness.
func ParseMsg(body string) (Msg, bool) {
	rest, ok := strings.CutPrefix(body, "kata=")
	if !ok {
		return Msg{}, false
	}
	kind, arg, _ := strings.Cut(rest, ":")
	m := Msg{Kind: kind}
	switch kind {
	case "ready", "missing":
		return m, arg == ""
	case "old":
		m.Version = arg
	case "loaded", "badpar":
		m.ID = arg
	case "keys":
		n, err := strconv.Atoi(arg)
		if err != nil {
			return Msg{}, false
		}
		m.Keys = n
	case "done", "quit":
		data, err := base64.StdEncoding.DecodeString(arg)
		var p struct {
			Keys int    `json:"keys"`
			Seq  string `json:"seq"`
		}
		if err != nil || json.Unmarshal(data, &p) != nil {
			return Msg{}, false
		}
		m.Keys, m.Seq = p.Keys, p.Seq
	default:
		return Msg{}, false
	}
	return m, arg != "" || kind == "ready" || kind == "missing"
}
