package prompt

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Stop is how an agent's turn ended.
type Stop int

const (
	StopDone     Stop = iota // finished (or failed)
	StopQuestion             // blocked: a question, or a permission
)

// Reply is the text of a turn, with an id that does not change when the
// same reply is read again.
type Reply struct {
	ID, Text string
}

const claudeTail = 2 << 20 // bytes read at the end of a transcript

// ClaudeTranscript finds the transcript of the Claude Code process in the
// process group pgid: Claude writes ~/.claude/sessions/<pid>.json with its
// session id, read again each time since /clear and --resume change it.
func ClaudeTranscript(home string, pgid int) (string, bool) {
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "sessions", "*.json"))
	var best string
	var bestTime int64
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s struct {
			Pid       int    `json:"pid"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(data, &s) != nil || s.Pid <= 0 || s.SessionID == "" || strings.ContainsAny(s.SessionID, "/*?[") {
			continue
		}
		if g, err := unix.Getpgid(s.Pid); err != nil || g != pgid {
			continue
		}
		paths, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", s.SessionID+".jsonl"))
		for _, p := range paths {
			if st, err := os.Stat(p); err == nil && (best == "" || st.ModTime().UnixNano() > bestTime) {
				best, bestTime = p, st.ModTime().UnixNano()
			}
		}
	}
	return best, best != ""
}

type claudeEvent struct {
	text   string // a text block
	uuid   string
	tool   string // name of a tool_use
	toolID string
	input  json.RawMessage
}

// ClaudeReply reads the reply of the last turn of a transcript.
func ClaudeReply(path string, stop Stop, intermediate bool) (Reply, bool) {
	evs, ok := claudeTurn(path)
	if !ok {
		return Reply{}, false
	}
	lastTool := -1
	for i, e := range evs {
		if e.tool != "" {
			lastTool = i
		}
	}
	switch stop {
	case StopQuestion:
		if lastTool < 0 || evs[lastTool].tool != "AskUserQuestion" {
			return Reply{}, false
		}
		from := 0
		if !intermediate {
			for i := lastTool - 1; i >= 0; i-- {
				if evs[i].tool != "" {
					from = i + 1
					break
				}
			}
		}
		parts, _ := textParts(evs[from:lastTool])
		parts = append(parts, questionText(evs[lastTool].input)...)
		if len(parts) == 0 {
			return Reply{}, false
		}
		return Reply{ID: evs[lastTool].toolID, Text: strings.Join(parts, "\n\n")}, true
	default:
		from := lastTool + 1
		if intermediate {
			from = 0
		}
		parts, id := textParts(evs[from:])
		if len(parts) == 0 {
			return Reply{}, false
		}
		return Reply{ID: id, Text: strings.Join(parts, "\n\n")}, true
	}
}

func textParts(evs []claudeEvent) (parts []string, lastUUID string) {
	for _, e := range evs {
		if t := strings.TrimSpace(e.text); e.tool == "" && t != "" {
			parts = append(parts, t)
			lastUUID = e.uuid
		}
	}
	return parts, lastUUID
}

func questionText(input json.RawMessage) []string {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	var out []string
	for _, q := range in.Questions {
		if q.Question != "" {
			out = append(out, q.Question)
		}
		var labels []string
		for _, o := range q.Options {
			if o.Label != "" {
				labels = append(labels, o.Label)
			}
		}
		if len(labels) > 0 {
			out = append(out, strings.Join(labels, ", "))
		}
	}
	return out
}

// claudeTurn returns the text and tool blocks after the last prompt of the
// transcript, subagents left out.
func claudeTurn(path string) ([]claudeEvent, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false
	}
	off := max(st.Size()-claudeTail, 0)
	data := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(data, off); err != nil && err != io.EOF {
		return nil, false
	}
	if off > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	var evs []claudeEvent
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		var e struct {
			Type        string `json:"type"`
			UUID        string `json:"uuid"`
			IsSidechain bool   `json:"isSidechain"`
			Message     struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if len(line) == 0 || json.Unmarshal(line, &e) != nil || e.IsSidechain {
			continue
		}
		if e.Type != "user" && e.Type != "assistant" {
			continue
		}
		var s string
		if json.Unmarshal(e.Message.Content, &s) == nil {
			if e.Type == "user" {
				evs = evs[:0] // a new prompt: a new turn
			}
			continue
		}
		var blocks []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		if e.Type == "user" {
			for _, b := range blocks {
				if b.Type == "text" {
					evs = evs[:0]
					break
				}
			}
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				evs = append(evs, claudeEvent{text: b.Text, uuid: e.UUID})
			case "tool_use":
				evs = append(evs, claudeEvent{tool: b.Name, toolID: b.ID, input: b.Input, uuid: e.UUID})
			}
		}
	}
	return evs, true
}
