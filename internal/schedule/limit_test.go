package schedule

import (
	"testing"
	"time"
)

func TestDetectLimitRealCapture(t *testing.T) {
	lines := []string{
		"● Étape 6/7 : tests du navigateur écrits et en échec, j'écris le code.",
		"  Ran 1 shell command",
		"  ⎿  You've hit your 5-hour limit · resets 5:20pm (Europe/Paris)",
		"     /upgrade or /usage-credits to finish what you’re working on.",
		"     Usage credits are at least 30% off for one purchase · 3 days left · /usage-credits",
		"✻ Brewed for 18m 48s · done 13:00",
		"                        new task? /clear to save 304.6k tokens",
		"────────────────────────────────────────────────────────────────",
		"❯ ",
		"────────────────────────────────────────────────────────────────",
		"  ctx 29% │ /mnt/data/dev/daw-ia ⎇ main │ 5h 100% ↻ 17:20 │ 7…",
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 1 agent",
	}

	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("Europe/Paris not available")
	}
	now := time.Date(2026, 11, 10, 16, 30, 0, 0, loc)

	info, err := DetectLimit(lines, now)
	if err != nil {
		t.Fatalf("DetectLimit failed: %v", err)
	}

	if info.App != "claude" {
		t.Errorf("expected App=claude, got %q", info.App)
	}

	// 5:20pm is 17:20
	wantReset := time.Date(2026, 11, 10, 17, 20, 0, 0, loc)
	wantResume := time.Date(2026, 11, 10, 17, 21, 0, 0, loc)

	if !info.ResetAt.Equal(wantReset) {
		t.Errorf("ResetAt = %v, want %v", info.ResetAt, wantReset)
	}
	if !info.ResumeAt.Equal(wantResume) {
		t.Errorf("ResumeAt = %v, want %v", info.ResumeAt, wantResume)
	}
	if info.Command != "continue" {
		t.Errorf("Command = %q, want 'continue'", info.Command)
	}
}

func TestDetectLimitRelative(t *testing.T) {
	lines := []string{
		"Rate limit reached.",
		"resets in 1 hour 45 minutes",
	}

	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	info, err := DetectLimit(lines, now)
	if err != nil {
		t.Fatalf("DetectLimit failed: %v", err)
	}

	wantReset := now.Add(1*time.Hour + 45*time.Minute)
	wantResume := wantReset.Add(1 * time.Minute)

	if !info.ResetAt.Equal(wantReset) {
		t.Errorf("ResetAt = %v, want %v", info.ResetAt, wantReset)
	}
	if !info.ResumeAt.Equal(wantResume) {
		t.Errorf("ResumeAt = %v, want %v", info.ResumeAt, wantResume)
	}
}

func TestDetectLimitAGY(t *testing.T) {
	lines := []string{
		"Antigravity CLI (agy)",
		"Quota exceeded: try again in 10 minutes",
	}

	now := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)
	info, err := DetectLimit(lines, now)
	if err != nil {
		t.Fatalf("DetectLimit failed: %v", err)
	}

	if info.App != "agy" {
		t.Errorf("expected App=agy, got %q", info.App)
	}

	wantReset := now.Add(10 * time.Minute)
	wantResume := wantReset.Add(1 * time.Minute)
	if !info.ResetAt.Equal(wantReset) {
		t.Errorf("ResetAt = %v, want %v", info.ResetAt, wantReset)
	}
	if !info.ResumeAt.Equal(wantResume) {
		t.Errorf("ResumeAt = %v, want %v", info.ResumeAt, wantResume)
	}
}

func TestDetectLimitClockOnly(t *testing.T) {
	lines := []string{
		"claude code session",
		"  ctx 45% │ 5h 100% ↻ 18:45 │ main",
	}

	loc, _ := time.LoadLocation("UTC")
	now := time.Date(2026, 11, 10, 18, 0, 0, 0, loc)
	info, err := DetectLimit(lines, now)
	if err != nil {
		t.Fatalf("DetectLimit failed: %v", err)
	}

	if info.App != "claude" {
		t.Errorf("expected App=claude, got %q", info.App)
	}

	wantReset := time.Date(2026, 11, 10, 18, 45, 0, 0, loc)
	wantResume := time.Date(2026, 11, 10, 18, 46, 0, 0, loc)
	if !info.ResetAt.Equal(wantReset) {
		t.Errorf("ResetAt = %v, want %v", info.ResetAt, wantReset)
	}
	if !info.ResumeAt.Equal(wantResume) {
		t.Errorf("ResumeAt = %v, want %v", info.ResumeAt, wantResume)
	}
}

func TestDetectLimitIgnoresStatusBelowFull(t *testing.T) {
	lines := []string{
		"claude code session",
		"  ctx 29% │ /mnt/data/dev/x ⎇ main │ 5h 34% ↻ 17:20 │ 7d 12%",
	}
	now := time.Date(2026, 11, 10, 16, 30, 0, 0, time.UTC)
	if info, err := DetectLimit(lines, now); err == nil {
		t.Fatalf("a status line at 34%% is no limit: %+v", info)
	}
}

func TestDetectLimitNeedsLimitWords(t *testing.T) {
	now := time.Date(2026, 11, 10, 16, 30, 0, 0, time.UTC)
	for _, lines := range [][]string{
		{"npm WARN network: retry in 5s", "$ "},
		{"the counter resets 5:20pm every day"},
		{"cache resets in 10 minutes"},
	} {
		if info, err := DetectLimit(lines, now); err == nil {
			t.Errorf("%q: no limit words, got %+v", lines, info)
		}
	}
}

func TestDetectLimitStaleMessage(t *testing.T) {
	// The limit reset at 17:20; at 18:30 the message is an old one, not
	// a limit until tomorrow.
	lines := []string{"  ⎿  You've hit your 5-hour limit · resets 5:20pm", "❯ "}
	now := time.Date(2026, 11, 10, 18, 30, 0, 0, time.UTC)
	if info, err := DetectLimit(lines, now); err == nil {
		t.Fatalf("stale limit detected: %+v", info)
	}
}

func TestDetectLimitAfterMidnight(t *testing.T) {
	lines := []string{"You've hit your 5-hour limit · resets 1am"}
	now := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	info, err := DetectLimit(lines, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC); !info.ResetAt.Equal(want) {
		t.Fatalf("ResetAt = %v, want %v", info.ResetAt, want)
	}
}

func TestDetectLimitWrappedMessage(t *testing.T) {
	lines := []string{"Error: usage limit reached for this model.", "Please try again in 15 minutes."}
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	info, err := DetectLimit(lines, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(15 * time.Minute); !info.ResetAt.Equal(want) {
		t.Fatalf("ResetAt = %v, want %v", info.ResetAt, want)
	}
}

func TestParseClockTimeRejects(t *testing.T) {
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	for _, s := range []string{"5", "25:00", "17:99", "13pm", "0am", "17:5", "1:2:3:4", "abc"} {
		if got, err := parseClockTime(s, "", now); err == nil {
			t.Errorf("%q accepted: %v", s, got)
		}
	}
}

func TestParseClockTimeDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("Europe/Paris not available")
	}
	// 25 October 2026: the clocks go back an hour at 3:00.
	now := time.Date(2026, 10, 24, 20, 0, 0, 0, loc)
	got, err := parseClockTime("19:00", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 25, 19, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
