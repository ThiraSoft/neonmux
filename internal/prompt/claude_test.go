package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Lines of a Claude Code transcript, one content block per assistant line.
func prompt(uuid, text string) string {
	return `{"type":"user","uuid":"` + uuid + `","message":{"role":"user","content":` + jsonStr(text) + `}}`
}
func text(uuid, s string) string {
	return `{"type":"assistant","uuid":"` + uuid + `","isSidechain":false,"message":{"content":[{"type":"text","text":` + jsonStr(s) + `}]}}`
}
func tool(uuid, id, name, input string) string {
	return `{"type":"assistant","uuid":"` + uuid + `","message":{"content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}`
}
func result(uuid, id string) string {
	return `{"type":"user","uuid":"` + uuid + `","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"ok"}]}}`
}
func side(uuid, s string) string {
	return `{"type":"assistant","uuid":"` + uuid + `","isSidechain":true,"message":{"content":[{"type":"text","text":` + jsonStr(s) + `}]}}`
}
func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func transcript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var turn = []string{
	prompt("u0", "ancienne question"), text("a0", "ancienne reponse"),
	prompt("u1", "fais le"),
	text("a1", "je regarde"),
	tool("a2", "t1", "Bash", `{}`),
	result("u2", "t1"),
	text("a3", "Fini."),
	text("a4", "Voilà."),
}

func TestClaudeFinalOnly(t *testing.T) {
	r, ok := ClaudeReply(transcript(t, turn...), StopDone, false)
	if !ok || r.Text != "Fini.\n\nVoilà." || r.ID != "a4" {
		t.Fatalf("got %+v %v", r, ok)
	}
}

func TestClaudeIntermediate(t *testing.T) {
	r, ok := ClaudeReply(transcript(t, turn...), StopDone, true)
	if !ok || r.Text != "je regarde\n\nFini.\n\nVoilà." {
		t.Fatalf("got %+v %v", r, ok)
	}
}

func TestClaudeNoTextNoTrack(t *testing.T) {
	if r, ok := ClaudeReply(transcript(t, prompt("u1", "x"), tool("a1", "t1", "Bash", `{}`)), StopDone, false); ok {
		t.Fatalf("got %+v", r)
	}
}

const ask = `{"questions":[{"question":"Quelle couleur ?","options":[{"label":"Rouge"},{"label":"Bleu"}]}]}`

func TestClaudeQuestion(t *testing.T) {
	p := transcript(t, prompt("u1", "go"), tool("a1", "t1", "Read", `{}`), result("u2", "t1"),
		text("a2", "Deux options."), tool("a3", "q1", "AskUserQuestion", ask))
	r, ok := ClaudeReply(p, StopQuestion, false)
	if !ok || r.ID != "q1" || r.Text != "Deux options.\n\nQuelle couleur ?\n\nRouge, Bleu" {
		t.Fatalf("got %+v %v", r, ok)
	}
	p = transcript(t, prompt("u1", "go"), text("a1", "je lance"), tool("a2", "t1", "Bash", `{}`))
	if r, ok := ClaudeReply(p, StopQuestion, false); ok {
		t.Fatalf("permission made a track: %+v", r)
	}
}

func TestClaudeAfterAnswer(t *testing.T) {
	p := transcript(t, prompt("u1", "go"), text("a2", "Deux options."), tool("a3", "q1", "AskUserQuestion", ask),
		result("u3", "q1"), text("a4", "Rouge, donc."))
	r, ok := ClaudeReply(p, StopDone, false)
	if !ok || r.Text != "Rouge, donc." {
		t.Fatalf("got %+v %v", r, ok)
	}
}

func TestClaudeSidechainIgnored(t *testing.T) {
	p := transcript(t, prompt("u1", "go"), text("a1", "Fini."), side("s1", "sous-agent"))
	r, ok := ClaudeReply(p, StopDone, false)
	if !ok || r.Text != "Fini." {
		t.Fatalf("got %+v %v", r, ok)
	}
}

func TestClaudePartialLastLine(t *testing.T) {
	p := transcript(t, prompt("u1", "go"), text("a1", "Fini."), `{"type":"assistant","uuid":"a2","mess`)
	r, ok := ClaudeReply(p, StopDone, false)
	if !ok || r.Text != "Fini." {
		t.Fatalf("got %+v %v", r, ok)
	}
}

func TestClaudeTranscriptSwitch(t *testing.T) {
	home := t.TempDir()
	pid := os.Getpid()
	pgid, _ := unix.Getpgid(pid)
	sess := filepath.Join(home, ".claude", "sessions")
	os.MkdirAll(sess, 0o700)
	write := func(id, proj string) string {
		os.WriteFile(filepath.Join(sess, "1.json"), []byte(`{"pid":1,"sessionId":"other"}`), 0o600)
		os.WriteFile(filepath.Join(sess, "self.json"), []byte(`{"pid":`+itoa(pid)+`,"sessionId":"`+id+`"}`), 0o600)
		dir := filepath.Join(home, ".claude", "projects", proj)
		os.MkdirAll(dir, 0o700)
		p := filepath.Join(dir, id+".jsonl")
		os.WriteFile(p, nil, 0o600)
		return p
	}
	want := write("S1", "-a")
	if got, ok := ClaudeTranscript(home, pgid); !ok || got != want {
		t.Fatalf("got %q %v, want %q", got, ok, want)
	}
	want = write("S2", "-b")
	if got, ok := ClaudeTranscript(home, pgid); !ok || got != want {
		t.Fatalf("after /clear got %q %v, want %q", got, ok, want)
	}
	if _, ok := ClaudeTranscript(home, pgid+100000); ok {
		t.Fatal("found a transcript for another group")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// With intermediate texts on, a turn whose final text is not flushed yet is
// not a reply: the text before the tool call alone would cut it.
func TestClaudeIntermediateWaitsForFinal(t *testing.T) {
	p := transcript(t, prompt("u1", "go"), text("a1", "je regarde"), tool("a2", "t1", "Bash", `{}`), result("u2", "t1"))
	if r, ok := ClaudeReply(p, StopDone, true); ok {
		t.Fatalf("got %+v", r)
	}
}
