package vt

import (
	"encoding/base64"
	"strconv"
	"testing"
	"time"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestProgStatusQuery(t *testing.T) {
	term := New(10, 3, 10)
	var got []byte
	term.SetReply(func(b []byte) { got = append(got, b...) })
	feed(term, "\x1b]7501;?\x1b\\")
	if string(got) != "\x1b]7501;?\x1b\\" {
		t.Errorf("reply %q", got)
	}
}

func TestProgStatusReport(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "\x1b]7501;state=working:app=claude:msg="+b64("Reading app.go")+"\x07")
	st, ok := term.ProgramStatus()
	if !ok || st.State != PSWorking || st.App != "claude" || st.Msg != "Reading app.go" || st.Progress != -1 {
		t.Fatalf("%+v", st)
	}
	since := st.Since
	// Same state, different message: the clock does not restart.
	time.Sleep(2 * time.Millisecond)
	feed(term, "\x1b]7501;state=working:progress=40:msg="+b64("Tests")+"\x1b\\")
	st, _ = term.ProgramStatus()
	if st.Msg != "Tests" || st.Progress != 40 || !st.Since.Equal(since) || st.App != "" {
		t.Fatalf("update: %+v", st)
	}
	// New state: the clock restarts, and the reason is only kept with blocked.
	feed(term, "\x1b]7501;state=blocked:kind=permission:msg="+b64("Allow Bash?")+"\x1b\\")
	st, _ = term.ProgramStatus()
	if st.State != PSBlocked || st.Kind != "permission" || !st.Since.After(since) {
		t.Fatalf("blocked: %+v", st)
	}
	feed(term, "\x1b]7501;state=done:kind=permission\x1b\\")
	if st, _ = term.ProgramStatus(); st.State != PSDone || st.Kind != "" {
		t.Fatalf("done: %+v", st)
	}
}

func TestProgStatusRejects(t *testing.T) {
	term := New(10, 3, 10)
	for _, seq := range []string{
		"state=sleeping",                   // unknown state
		"state=working:msg=" + b64("a\nb"), // control character in the text
		"state=working:msg=%%%",            // invalid base64
		"state=working:id=a b",             // invalid id
		"msg=" + b64("no state"),           // state is required
	} {
		feed(term, "\x1b]7501;"+seq+"\x1b\\")
		if st, ok := term.ProgramStatus(); ok {
			t.Errorf("%q accepted: %+v", seq, st)
		}
	}
	// A malformed pair is ignored on its own.
	feed(term, "\x1b]7501;state=done:junk:Uppercase=1\x1b\\")
	if st, ok := term.ProgramStatus(); !ok || st.State != PSDone {
		t.Errorf("malformed pair: %+v %v", st, ok)
	}
}

func TestProgStatusRecordsAndClear(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "\x1b]7501;state=working:id=build\x1b\\")
	feed(term, "\x1b]7501;state=blocked:kind=question:id=build/test\x1b\\")
	feed(term, "\x1b]7501;state=done:id=deploy\x1b\\")
	if st, _ := term.ProgramStatus(); st.ID != "build/test" {
		t.Fatalf("the most urgent should be the blocked one: %+v", st)
	}
	feed(term, "\x1b]7501;state=clear:id=build\x1b\\") // also removes the child
	if st, _ := term.ProgramStatus(); st.ID != "deploy" {
		t.Fatalf("after clearing build: %+v", st)
	}
	feed(term, "\x1b]7501;state=clear\x1b\\")
	if _, ok := term.ProgramStatus(); ok {
		t.Fatal("clear without an id should remove everything")
	}
}

func TestProgStatusPromptAndExit(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "\x1b]7501;state=working:id=a\x1b\\\x1b]7501;state=done:id=b\x1b\\")
	feed(term, "\x1b]133;A\x1b\\") // new prompt: the work in progress is cleared
	if st, ok := term.ProgramStatus(); !ok || st.ID != "b" {
		t.Fatalf("after OSC 133 A: %+v", st)
	}
	feed(term, "\x1b]7501;state=blocked\x1b\\")
	term.DropStatus(PSWorking, PSBlocked)
	if st, _ := term.ProgramStatus(); st.ID != "b" {
		t.Fatalf("after the program exited: %+v", st)
	}
	feed(term, "\x1bc") // RIS
	if _, ok := term.ProgramStatus(); ok {
		t.Fatal("RIS should clear everything")
	}
}

func TestProgStatusSnapshot(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "\x1b]7501;state=blocked:kind=permission:app=claude:title="+b64("Task")+":msg="+b64("Allow?")+"\x1b\\")
	want, _ := term.ProgramStatus()
	other := New(10, 3, 10)
	if err := other.LoadSnapshot(term.Snapshot()); err != nil {
		t.Fatal(err)
	}
	got, ok := other.ProgramStatus()
	if !ok || got.Msg != want.Msg || got.Title != "Task" || got.Kind != "permission" || !got.Since.Equal(want.Since) {
		t.Fatalf("after snapshot: %+v", got)
	}
	// A snapshot from a version without statuses still loads.
	old := term.Snapshot()
	px := &enc{}
	px.pixelSize(term)
	px.kbd(term)
	old = old[:len(old)-len(encodeProgress(term))-len(px.b)]
	if err := other.LoadSnapshot(old); err != nil {
		t.Fatalf("old snapshot: %v", err)
	}
	if _, ok := other.ProgramStatus(); ok {
		t.Error("old snapshot: no status expected")
	}
	other.RestoreAfterRestart(time.Now())
	if _, ok := other.ProgramStatus(); ok {
		t.Error("after a restart, the statuses should be gone")
	}
}

func encodeProgress(t *Terminal) []byte {
	e := &enc{}
	e.progress(t)
	return e.b
}

func TestProgStatusRev04(t *testing.T) {
	term := New(10, 3, 10)
	// Spaces around keys and values, unknown reason ignored.
	feed(term, "\x1b]7501; state = blocked : kind = telepathy : app = claude \x1b\\")
	st, ok := term.ProgramStatus()
	if !ok || st.State != PSBlocked || st.Kind != "" || st.App != "claude" {
		t.Fatalf("%+v %v", st, ok)
	}
	// A child without an app inherits its ancestor's.
	feed(term, "\x1b]7501;state=working:id=a:app=cargo\x1b\\\x1b]7501;state=working:id=a/b/c\x1b\\")
	term.Lock()
	app := term.progress["a/b/c"].App
	term.Unlock()
	if app != "cargo" {
		t.Errorf("app inheritance: %q", app)
	}
	// At the cap, the least recently updated record makes room.
	term = New(10, 3, 10)
	for i := 0; i < progMaxRecords; i++ {
		feed(term, "\x1b]7501;state=working:id=r"+itoa(i)+"\x1b\\")
	}
	feed(term, "\x1b]7501;state=working:id=r0\x1b\\") // r0 refreshed
	feed(term, "\x1b]7501;state=done:id=new\x1b\\")
	term.Lock()
	_, kept := term.progress["r0"]
	_, gone := term.progress["r1"]
	_, added := term.progress["new"]
	term.Unlock()
	if !kept || gone || !added {
		t.Errorf("eviction: r0 kept %v, r1 removed %v, new %v", kept, !gone, added)
	}
}

func TestProgStatusGenSurvivesSnapshot(t *testing.T) {
	daemon, window := New(10, 3, 10), New(10, 3, 10)
	stream := "\x1b]7501;state=working\x1b\\\x1b]7501;state=working:msg=" + b64("x") + "\x1b\\\x1b]7501;state=done\x1b\\"
	feed(daemon, stream)
	feed(window, stream)
	time.Sleep(2 * time.Millisecond)
	before, _ := window.ProgramStatus()
	// Reattach: the daemon's snapshot replaces the window's state.
	if err := window.LoadSnapshot(daemon.Snapshot()); err != nil {
		t.Fatal(err)
	}
	after, _ := window.ProgramStatus()
	if after.Gen != before.Gen || after.State != PSDone {
		t.Fatalf("Gen %d then %d: a reattach should not count as a change", before.Gen, after.Gen)
	}
	// The same state with another message does not change Gen.
	feed(window, "\x1b]7501;state=done:msg="+b64("finished")+"\x1b\\")
	if st, _ := window.ProgramStatus(); st.Gen != before.Gen {
		t.Errorf("Gen changed without a state change")
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
