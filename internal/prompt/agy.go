package prompt

// agy (Antigravity CLI) keeps each conversation in a SQLite base,
// ~/.gemini/antigravity-cli/conversations/<id>.db, which the running agy
// holds open (checked on a live agy, 2026-11: /proc/<pid>/fd points at the
// .db, its -wal and -shm). agy also asks the terminal whether it speaks
// OSC 7501 ("\e]7501;?"), but the reader below does not rely on it.
//
// The table steps holds one row per step, its payload in protobuf with no
// published schema. Seen on the live format: step_type 14 is the user's
// input (text at 19.2), 15 a model step (text at 20.1, a tool call when
// 20.7 is present), 132 a tool run; status 3 is done. Any surprise (other
// format, no sqlite3) makes the reader find nothing, never fail loudly.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var agyOnce = sync.OnceValue(func() bool {
	_, err := exec.LookPath("sqlite3")
	return err == nil
})

// AgyAvailable reports whether agy conversations can be read (sqlite3).
func AgyAvailable() bool { return agyOnce() }

const agyQuery = "select idx, step_type, status, hex(step_payload) as p from steps " +
	"where idx > coalesce((select max(idx) from steps where step_type=14), -1) order by idx"

// AgyReply reads the reply of the conversation's last turn, if the turn is
// over.
func AgyReply(db string, intermediate bool) (Reply, bool) {
	if !AgyAvailable() {
		return Reply{}, false
	}
	if _, err := os.Stat(db); err != nil {
		return Reply{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sqlite3", "-readonly", "-json", db, agyQuery).Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return Reply{}, false
	}
	var rows []struct {
		Idx    int    `json:"idx"`
		Type   int    `json:"step_type"`
		Status int    `json:"status"`
		P      string `json:"p"`
	}
	if json.Unmarshal(out, &rows) != nil || len(rows) == 0 {
		return Reply{}, false
	}
	type step struct {
		text string
		tool bool
	}
	steps := make([]step, len(rows))
	lastTool := -1
	for i, r := range rows {
		payload, _ := hex.DecodeString(r.P)
		if r.Type == 132 {
			steps[i].tool = true
		}
		if r.Type == 15 {
			if t, ok := pbField(payload, 20, 1); ok {
				steps[i].text = string(t)
			}
			if _, ok := pbField(payload, 20, 7); ok {
				steps[i].tool = true
			}
		}
		if steps[i].tool {
			lastTool = i
		}
	}
	last := rows[len(rows)-1]
	if last.Type != 15 || last.Status != 3 || steps[len(steps)-1].tool {
		return Reply{}, false
	}
	from := lastTool + 1
	if intermediate {
		from = 0
	}
	var parts []string
	for i := from; i < len(rows); i++ {
		if t := strings.TrimSpace(steps[i].text); rows[i].Type == 15 && t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return Reply{}, false
	}
	return Reply{ID: db + "#" + strconv.Itoa(last.Idx), Text: strings.Join(parts, "\n\n")}, true
}

// AgyConversation finds the conversation base the agy of process group
// pgid has open.
func AgyConversation(pgid int) (string, bool) {
	return agyConversationOf(descendants(pgid))
}

func agyConversationOf(pids []int) (string, bool) {
	for _, pid := range pids {
		for _, f := range openFiles(pid) {
			if strings.HasSuffix(f, ".db") && strings.Contains(f, string(filepath.Separator)+filepath.Join("antigravity-cli", "conversations")+string(filepath.Separator)) {
				return f, true
			}
		}
	}
	return "", false
}

// descendants returns pid and its children, grandchildren…
func descendants(pid int) []int {
	out := []int{pid}
	for i := 0; i < len(out) && len(out) < 64; i++ {
		out = append(out, children(out[i])...)
	}
	return out
}

func children(pid int) []int {
	var raw string
	if runtime.GOOS == "linux" {
		b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/task/" + strconv.Itoa(pid) + "/children")
		raw = string(b)
	} else {
		b, _ := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
		raw = string(b)
	}
	var out []int
	for _, f := range strings.Fields(raw) {
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func openFiles(pid int) []string {
	if runtime.GOOS == "linux" {
		dir := "/proc/" + strconv.Itoa(pid) + "/fd"
		ents, _ := os.ReadDir(dir)
		var out []string
		for _, e := range ents {
			if l, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil {
				out = append(out, l)
			}
		}
		return out
	}
	b, _ := exec.Command("lsof", "-Fn", "-p", strconv.Itoa(pid)).Output()
	var out []string
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.HasPrefix(line, "n") {
			out = append(out, line[1:])
		}
	}
	return out
}
