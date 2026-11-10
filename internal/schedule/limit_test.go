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
