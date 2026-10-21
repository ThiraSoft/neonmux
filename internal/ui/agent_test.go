package ui

import (
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

func TestFmtDurAndWake(t *testing.T) {
	cases := map[time.Duration]string{
		-time.Second:                   "0s",
		12 * time.Second:               "12s",
		4*time.Minute + 59*time.Second: "4m",
		65 * time.Minute:               "1h05m",
	}
	for d, want := range cases {
		if got := fmtDur(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
	since := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if got := durWake(since, since.Add(1500*time.Millisecond)); !got.Equal(since.Add(2 * time.Second)) {
		t.Errorf("wake on the second: %v", got.Sub(since))
	}
	if got := durWake(since, since.Add(90*time.Second)); !got.Equal(since.Add(2 * time.Minute)) {
		t.Errorf("wake on the minute: %v", got.Sub(since))
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"✳ Claude Code":     "Claude Code",
		"⠂ Floating panes":  "Floating panes",
		"Shell probe":       "Shell probe",
		"« quotes »":        "« quotes »",
		"  spaces around  ": "spaces around",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func agentPane(id int, state vt.ProgState, since time.Time, seen bool) *TermPane {
	p := NewTermPane(id, nil, "", nil)
	p.prog = vt.ProgStatus{State: state, Since: since, Progress: -1}
	p.hasProg = true
	p.progSeen = seen
	return p
}

func TestAttentionOrder(t *testing.T) {
	t0 := time.Now()
	doneOld := agentPane(1, vt.PSDone, t0.Add(-time.Hour), false)
	blockedNew := agentPane(2, vt.PSBlocked, t0.Add(-time.Minute), false)
	working := agentPane(3, vt.PSWorking, t0, false)
	doneSeen := agentPane(4, vt.PSDone, t0.Add(-2*time.Hour), true)
	blockedOld := agentPane(5, vt.PSBlocked, t0.Add(-time.Hour), false)
	notified := &fakePane{}
	notified.notify = "bell"
	a := &App{cfg: DefaultConfig(), theme: theme.Get("neon-tokyo"),
		panes: []Pane{doneOld, blockedNew, working, doneSeen, blockedOld, notified}}
	got := a.attention()
	want := []Pane{blockedOld, blockedNew, notified, doneOld}
	if !slices.Equal(got, want) {
		t.Fatalf("order %v", got)
	}
	// mod+n skips the focused pane.
	a.focus = 4
	a.nextAttention()
	if a.focused() != blockedNew {
		t.Errorf("focus %d, want the next blocked agent", a.focus)
	}
	// mod+shift+n goes back to the previous pane.
	a.focusBack()
	if a.focused() != blockedOld {
		t.Errorf("back: focus %d", a.focus)
	}
}

func TestProgBadge(t *testing.T) {
	th := theme.Get("neon-tokyo")
	now := time.Now()
	p := agentPane(1, vt.PSBlocked, now.Add(-4*time.Minute), false)
	p.prog.Kind, p.prog.Msg = "permission", "Bash: go test"
	if icon, got, c := p.progBadge(th, now); icon != "st-blocked" || got != "permission: Bash: go test · 4m" || c != th.Warn {
		t.Errorf("blocked: %q", got)
	}
	p.prog = vt.ProgStatus{State: vt.PSWorking, Since: now.Add(-12 * time.Second), Progress: 40, Msg: "Tests"}
	if _, got, _ := p.progBadge(th, now); got != "Tests 40% · 12s" {
		t.Errorf("working: %q", got)
	}
	p.prog = vt.ProgStatus{State: vt.PSIdle, Progress: -1}
	if _, got, _ := p.progBadge(th, now); got != "ready" {
		t.Errorf("ready: %q", got)
	}
}

func TestParseDiff(t *testing.T) {
	out := []byte(`diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -10,3 +10,4 @@ func main() {
 	a := 1
-	b := 2
+	b := 3
+	c := 4
 	return
diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-goodbye
`)
	files, rows := parseDiff(out)
	if len(files) != 2 || files[0].path != "main.go" || files[0].add != 2 || files[0].del != 1 || files[1].del != 1 {
		t.Fatalf("files %+v", files)
	}
	// The added line "c := 4" is line 12 of the new file.
	for _, r := range rows {
		if r.kind == '+' && r.text == "\tc := 4" && r.line != 12 {
			t.Errorf("line of c := 4: %d", r.line)
		}
	}
	if rows[files[1].row].kind != 'F' || rows[files[1].row].text != "old.txt" {
		t.Errorf("header of the 2nd file: %+v", rows[files[1].row])
	}
}

func TestReadGitStatAndDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n2\nthree\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\ny\nz"), 0o644)

	st := readGitStat(dir)
	if !st.ok || st.branch != "main" || st.files != 2 || st.add != 2+3 || st.del != 1 {
		t.Fatalf("stat %+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "index.lock")); err == nil {
		t.Error("the index lock must never be left behind")
	}
	d := readDiff(dir)
	files, rows, branch, err := d.files, d.rows, d.branch, d.err
	if err != nil || branch != "main" || len(files) != 2 || files[1].path != "new.txt" || files[1].add != 3 {
		t.Fatalf("diff %+v %v %q", files, err, branch)
	}
	if last := rows[len(rows)-1]; last.kind != '+' || last.text != "z" || last.line != 3 {
		t.Errorf("last line of the new file: %+v", last)
	}
}

func TestZenVisibilityReverses(t *testing.T) {
	a := &App{}
	if a.zenVisibility(time.Now()) != 1 {
		t.Fatal("visible at the start")
	}
	a.toggleZen()
	time.Sleep(zenDur / 2)
	mid := a.zenVisibility(time.Now())
	a.toggleZen() // turn around in the middle of the fade: no jump
	if got := a.zenVisibility(time.Now()); got < mid-0.08 || got > mid+0.08 {
		t.Errorf("opacity jump: %v then %v", mid, got)
	}
	time.Sleep(zenDur)
	if a.zen || a.zenVisibility(time.Now()) != 1 {
		t.Error("once back, everything must be visible")
	}
}

func TestZenAlertBlinksWhileAgentBlocked(t *testing.T) {
	p := &TermPane{}
	p.hasProg, p.prog.State = true, vt.PSBlocked
	a := &App{panes: []Pane{p}}
	if a.zenAlert() {
		t.Fatal("outside zen mode, no frame")
	}
	a.zen = true
	if !a.zenAlert() {
		t.Fatal("agent blocked in zen: frame expected")
	}
	t0 := time.UnixMilli(1_000_000)
	if a.zenAlertLit(t0) == a.zenAlertLit(t0.Add(zenAlertBlink)) {
		t.Error("the frame must blink")
	}
	if next := zenAlertNext(t0.Add(120 * time.Millisecond)); !next.Equal(t0.Add(zenAlertBlink)) {
		t.Errorf("next toggle %v, want %v", next, t0.Add(zenAlertBlink))
	}
	p.prog.State = vt.PSWorking
	if a.zenAlert() {
		t.Error("working agent: no frame")
	}
}

func TestZenAllowsDecorKeys(t *testing.T) {
	for act, ok := range map[string]bool{
		"view.zen": true, "quit": true, "bg.next": true, "bg.city": true, "bg.animate": true,
		"term.new": false, "pane.close": false, "theme.next": false, "": false,
	} {
		if zenAllows(act) != ok {
			t.Errorf("%q: want %v", act, ok)
		}
	}
}

func TestWorktreeName(t *testing.T) {
	for name, ok := range map[string]bool{
		"fix-login": true, "feat_2.1": true, "": false, "-x": false, ".x": false, "a/b": false, "a b": false,
		"a..b": false, "x.lock": false, "x.": false,
	} {
		if validWorktreeName(name) != ok {
			t.Errorf("%q: want %v", name, ok)
		}
	}
}

func TestPaletteInput(t *testing.T) {
	a := &App{}
	got := ""
	a.openPalette("NEW AGENT", nil)
	a.palette.input = func(s string) { got = s }
	a.palette.chars([]rune("fix-login fixes the bug"))
	a.palette.key(KeyEvent{Name: "enter"})
	if got != "fix-login fixes the bug" || a.palette != nil {
		t.Errorf("input %q, palette %v", got, a.palette)
	}
}

func TestParseDiffContentLikeHeaders(t *testing.T) {
	out := []byte(`diff --git a/q.sql b/q.sql
--- a/q.sql
+++ b/q.sql
@@ -1,3 +1,3 @@
 select 1;
--- old comment
+++i;
 ctx
`)
	files, rows := parseDiff(out)
	if len(files) != 1 || files[0].add != 1 || files[0].del != 1 {
		t.Fatalf("files %+v", files)
	}
	for _, r := range rows {
		if r.kind == ' ' && r.text == "ctx" && r.line != 3 {
			t.Errorf("ctx reported at line %d, want 3", r.line)
		}
	}
	if rows[len(rows)-2].kind != '+' || rows[len(rows)-2].text != "++i;" {
		t.Errorf("added line ++i; lost: %+v", rows)
	}
}

// finish makes the agent of p announce an end state ("done", "error"), then
// lets the window read it.
func finish(a *App, p *TermPane, state string) {
	fmt.Fprintf(p.t, "\x1b]7501;state=%s\x07", state)
	p.Update(a.frameCtx(time.Now()))
}

func TestFinishedAgentFlashesItsBorder(t *testing.T) {
	for state, want := range map[string]func(*theme.Theme) color.RGBA{
		"done":  func(th *theme.Theme) color.RGBA { return th.Accent2 },
		"error": func(th *theme.Theme) color.RGBA { return th.Warn },
	} {
		a, ts := coreApp(t, 2)
		p := ts[1]                                      // the agent at work
		p.animAt, p.glitchAt = time.Time{}, time.Time{} // appearing animation over
		p.Update(a.frameCtx(time.Now()))
		finish(a, p, state)
		now := time.Now()
		if !p.animating(now) {
			t.Errorf("%s: the border must flash", state)
		}
		if p.flashC != want(a.theme) {
			t.Errorf("%s: flash color %v", state, p.flashC)
		}
		if p.animating(now.Add(time.Second)) {
			t.Errorf("%s: the flash must stop on its own", state)
		}
	}
}

// agent_flash_count and agent_flash_duration set how long the border
// flashes.
func TestAgentFlashFollowsConfig(t *testing.T) {
	a, ts := coreApp(t, 2)
	a.cfg.AgentFlashCount, a.cfg.AgentFlashDuration = 3, 0.4
	p := ts[1]
	p.animAt, p.glitchAt = time.Time{}, time.Time{}
	p.Update(a.frameCtx(time.Now()))
	finish(a, p, "done")
	now := time.Now()
	if !p.animating(now.Add(1100 * time.Millisecond)) {
		t.Error("3 flashes of 0.4s: still flashing after 1.1s")
	}
	if p.animating(now.Add(1300 * time.Millisecond)) {
		t.Error("3 flashes of 0.4s: over after 1.2s")
	}
}

func TestFlashBlinksCount(t *testing.T) {
	for _, c := range []struct {
		n         int
		each, end time.Duration
	}{
		{2, 500 * time.Millisecond, time.Second},
		{3, 200 * time.Millisecond, 600 * time.Millisecond},
		{1, time.Second, time.Second},
	} {
		flashes, lit := 0, false
		for d := time.Duration(0); d <= c.end; d += 5 * time.Millisecond {
			on := flashLevel(d, c.n, c.each) > 0.5
			if on && !lit {
				flashes++
			}
			lit = on
		}
		if flashes != c.n {
			t.Errorf("%d of %v: %d flashes", c.n, c.each, flashes)
		}
		if flashLevel(c.end, c.n, c.each) != 0 || flashLevel(-time.Second, c.n, c.each) != 0 {
			t.Errorf("%d of %v: no light outside the flash", c.n, c.each)
		}
	}
}

func TestNoFlashOutOfSight(t *testing.T) {
	for name, hide := range map[string]func(*App, *TermPane){
		"hidden": func(a *App, p *TermPane) { p.hidden = true },
		"zen":    func(a *App, p *TermPane) { a.zen = true },
		// The state predates the window: reattaching is not a new end.
		"stale": func(a *App, p *TermPane) { a.start = time.Now().Add(time.Hour) },
		// Turned off in the config.
		"disabled": func(a *App, p *TermPane) { a.cfg.AgentFlash = false },
	} {
		a, ts := coreApp(t, 2)
		p := ts[1]
		p.Update(a.frameCtx(time.Now()))
		hide(a, p)
		finish(a, p, "done")
		// The border's flash (paneBase), not the visual bell's (TermPane).
		if !p.Base().flashAt.IsZero() {
			t.Errorf("%s: no flash expected", name)
		}
	}
}
