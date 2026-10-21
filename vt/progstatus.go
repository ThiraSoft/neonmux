package vt

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Program Status Protocol (OSC 7501): a program tells the terminal what it
// is doing (working, blocked on a permission, done...). Claude Code has
// used it since 2.1.295. Spec, revision 0.4:
// https://superlogical.com/rex/docs/build/program-status

// ProgState is the state a program reports, from least to most urgent.
type ProgState uint8

const (
	PSIdle    ProgState = iota + 1 // idle, waiting for the next instruction
	PSWorking                      // working
	PSDone                         // done, result not seen yet
	PSError                        // failed
	PSBlocked                      // cannot go on without the user
)

var progStates = map[string]ProgState{
	"idle": PSIdle, "working": PSWorking, "done": PSDone, "error": PSError, "blocked": PSBlocked,
}

// ProgStatus is a protocol record.
type ProgStatus struct {
	ID       string
	State    ProgState
	Kind     string // permission, question or auth (blocked only)
	Progress int    // 0 to 100, -1 if indeterminate
	App      string
	Title    string
	Msg      string
	Since    time.Time // last state change (not message change)
	// Gen numbers the terminal's state changes. The daemon and the window
	// read the same stream and carry this number in the snapshot: it is the
	// same on both sides, unlike the clocks.
	Gen     uint64
	touched uint64 // last report received (for eviction)
}

const (
	progMaxLen     = 4096
	progMaxRecords = 256
)

var progKinds = map[string]bool{"permission": true, "question": true, "auth": true}

// progStatus handles the payload of an OSC 7501 (what follows "7501;").
func (t *Terminal) progStatus(rest []byte) {
	if len(rest) > progMaxLen {
		return
	}
	s := string(rest)
	if s == "?" {
		// Detection: advertise the default states and reasons.
		t.replyString("\x1b]7501;?\x1b\\")
		return
	}
	kv := map[string]string{}
	for _, pair := range strings.Split(s, ":") {
		k, v, ok := strings.Cut(pair, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !progKey(k) {
			continue // malformed pair: only that pair is ignored
		}
		kv[k] = v
	}
	id := kv["id"]
	if !progID(id) {
		return
	}
	state := kv["state"]
	if state == "clear" {
		t.progClear(id)
		return
	}
	st, ok := progStates[state]
	if !ok {
		return // unknown state: report ignored
	}
	rec := ProgStatus{ID: id, State: st, Progress: -1}
	if st == PSBlocked && progKinds[kv["kind"]] {
		rec.Kind = kv["kind"] // an unknown reason counts as missing
	}
	if p, err := strconv.Atoi(kv["progress"]); err == nil && p >= 0 && p <= 100 {
		rec.Progress = p
	}
	if app := kv["app"]; len(app) <= 32 && progSegment(app) {
		rec.App = app
	}
	var bad bool
	rec.Title, bad = progText(kv["title"], 192)
	if bad {
		return
	}
	rec.Msg, bad = progText(kv["msg"], 2048)
	if bad {
		return
	}
	if t.progress == nil {
		t.progress = map[string]*ProgStatus{}
	}
	old := t.progress[id]
	if old == nil && len(t.progress) >= progMaxRecords {
		t.progEvict()
	}
	if rec.App == "" {
		rec.App = t.progInherit(id)
	}
	t.progTick++
	rec.touched = t.progTick
	if old != nil && old.State == st {
		rec.Since, rec.Gen = old.Since, old.Gen
	} else {
		t.progGen++
		rec.Since, rec.Gen = time.Now(), t.progGen
	}
	t.progress[id] = &rec
}

// progEvict removes the least recently updated record.
func (t *Terminal) progEvict() {
	var oldest *ProgStatus
	for _, r := range t.progress {
		if oldest == nil || r.touched < oldest.touched {
			oldest = r
		}
	}
	if oldest != nil {
		delete(t.progress, oldest.ID)
	}
}

// progInherit returns the app of the closest ancestor that has one.
func (t *Terminal) progInherit(id string) string {
	for id != "" {
		i := strings.LastIndexByte(id, '/')
		if i < 0 {
			id = ""
		} else {
			id = id[:i]
		}
		if r := t.progress[id]; r != nil && r.App != "" {
			return r.App
		}
	}
	return ""
}

func progKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// progID validates an id: up to 8 segments [A-Za-z0-9_.+-]{1,32}, 128 bytes
// at most. Empty: the root record.
func progID(id string) bool {
	if id == "" {
		return true
	}
	segs := strings.Split(id, "/")
	if len(id) > 128 || len(segs) > 8 {
		return false
	}
	for _, s := range segs {
		if len(s) < 1 || len(s) > 32 || !progSegment(s) {
			return false
		}
	}
	return true
}

// progSegment accepts [A-Za-z0-9_.+-]* (an id segment, or an app).
func progSegment(s string) bool {
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.+-", r)
		if !ok {
			return false
		}
	}
	return true
}

// progText decodes a base64 text. bad reports a text that rejects the whole
// report with it: too long once decoded (maxLen bytes), badly encoded or
// containing control characters.
func progText(enc string, maxLen int) (string, bool) {
	if enc == "" {
		return "", false
	}
	if len(enc) > (maxLen+2)/3*4+4 {
		return "", true
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(enc, "="))
	}
	if err != nil || len(raw) > maxLen || !utf8.Valid(raw) {
		return "", true
	}
	for _, r := range string(raw) {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return "", true
		}
	}
	return string(raw), false
}

// progClear removes id and its children, or everything if id is empty.
func (t *Terminal) progClear(id string) {
	if id == "" {
		t.progress = nil
		return
	}
	for k := range t.progress {
		if k == id || strings.HasPrefix(k, id+"/") {
			delete(t.progress, k)
		}
	}
}

// progDrop removes the records in the given states: at the start of a new
// prompt (OSC 133 A) or when the program that set them has exited.
func (t *Terminal) progDrop(states ...ProgState) {
	for k, r := range t.progress {
		for _, s := range states {
			if r.State == s {
				delete(t.progress, k)
			}
		}
	}
}

// ProgramStatus returns the most urgent record (blocked first, then error,
// done, working, idle).
func (t *Terminal) ProgramStatus() (ProgStatus, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var best *ProgStatus
	for _, r := range t.progress {
		if best == nil || r.State > best.State || r.State == best.State && r.ID < best.ID {
			best = r
		}
	}
	if best == nil {
		return ProgStatus{}, false
	}
	return *best, true
}

// DropStatus removes the records in the given states, for example working
// or blocked ones when the program that set them has exited. The sequence
// number does not move: this is not output from the program.
func (t *Terminal) DropStatus(states ...ProgState) {
	t.mu.Lock()
	t.progDrop(states...)
	t.mu.Unlock()
}

func (e *enc) progress(t *Terminal) {
	e.u(t.progGen)
	e.u(t.progTick)
	e.u(uint64(len(t.progress)))
	for _, r := range t.progress {
		e.str(r.ID)
		e.byte1(byte(r.State))
		e.str(r.Kind)
		e.i(int64(r.Progress))
		e.str(r.App)
		e.str(r.Title)
		e.str(r.Msg)
		e.i(r.Since.UnixNano())
		e.u(r.Gen)
		e.u(r.touched)
	}
}

// progress reads back the status section: counters, then records.
func (d *dec) progress() (gen, tick uint64, m map[string]*ProgStatus) {
	gen, tick = d.u(), d.u()
	n := int(min(d.u(), progMaxRecords))
	if n == 0 {
		return gen, tick, nil
	}
	m = make(map[string]*ProgStatus, n)
	for i := 0; i < n && d.err == nil; i++ {
		r := &ProgStatus{ID: d.str(), State: ProgState(d.byte1()), Kind: d.str(), Progress: int(d.i()),
			App: d.str(), Title: d.str(), Msg: d.str(), Since: time.Unix(0, d.i()), Gen: d.u(), touched: d.u()}
		if r.State >= PSIdle && r.State <= PSBlocked {
			m[r.ID] = r
		}
	}
	return gen, tick, m
}
