package schedule

import (
	"slices"
	"sync"
	"testing"
	"time"
)

func TestParseScheduleTime(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 11, 10, 16, 30, 0, 0, loc)

	// Relative
	got, err := ParseScheduleTime("+10m", now)
	if err != nil {
		t.Fatalf("+10m failed: %v", err)
	}
	if want := now.Add(10 * time.Minute); !got.Equal(want) {
		t.Errorf("+10m = %v, want %v", got, want)
	}

	// Clock 24h
	got, err = ParseScheduleTime("17:21", now)
	if err != nil {
		t.Fatalf("17:21 failed: %v", err)
	}
	if want := time.Date(2026, 11, 10, 17, 21, 0, 0, loc); !got.Equal(want) {
		t.Errorf("17:21 = %v, want %v", got, want)
	}

	// Clock 12h
	got, err = ParseScheduleTime("5:21pm", now)
	if err != nil {
		t.Fatalf("5:21pm failed: %v", err)
	}
	if want := time.Date(2026, 11, 10, 17, 21, 0, 0, loc); !got.Equal(want) {
		t.Errorf("5:21pm = %v, want %v", got, want)
	}
}

func TestSchedulerFire(t *testing.T) {
	var mu sync.Mutex
	var executed []string

	exec := func(pane string, tokens []string) error {
		mu.Lock()
		defer mu.Unlock()
		executed = append(executed, pane+":"+tokens[0])
		return nil
	}

	s := NewScheduler(exec)
	defer s.Close()

	now := time.Now()
	// Schedule job in 50ms
	j1, err := s.Schedule("p1", now.Add(50*time.Millisecond), []string{"continue"}, "")
	if err != nil {
		t.Fatalf("Schedule failed: %v", err)
	}

	// Check pending
	if list := s.List(); len(list) != 1 || list[0].ID != j1.ID {
		t.Fatalf("List = %+v, want job %s", list, j1.ID)
	}

	// Wait for execution
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(executed, []string{"p1:continue"}) {
		t.Fatalf("executed = %v, want ['p1:continue']", executed)
	}

	if list := s.List(); len(list) != 0 {
		t.Fatalf("expected 0 pending jobs, got %d", len(list))
	}
}

func TestSchedulerCancel(t *testing.T) {
	var executed bool
	s := NewScheduler(func(pane string, tokens []string) error {
		executed = true
		return nil
	})
	defer s.Close()

	j, _ := s.Schedule("p1", time.Now().Add(50*time.Millisecond), []string{"continue"}, "")
	if !s.Cancel(j.ID) {
		t.Fatal("expected Cancel to return true")
	}

	time.Sleep(100 * time.Millisecond)
	if executed {
		t.Fatal("cancelled job should not execute")
	}
}

func TestNormalizeTokens(t *testing.T) {
	if got := NormalizeTokens([]string{"continue"}); !slices.Equal(got, []string{"text:continue", "enter"}) {
		t.Errorf("NormalizeTokens(continue) = %v", got)
	}
	if got := NormalizeTokens([]string{"echo", "hello"}); !slices.Equal(got, []string{"text:echo hello", "enter"}) {
		t.Errorf("NormalizeTokens(echo, hello) = %v", got)
	}
	if got := NormalizeTokens([]string{"text:ls", "enter"}); !slices.Equal(got, []string{"text:ls", "enter"}) {
		t.Errorf("NormalizeTokens(text:ls, enter) = %v", got)
	}
	if got := NormalizeTokens([]string{"ctrl+c"}); !slices.Equal(got, []string{"ctrl+c"}) {
		t.Errorf("NormalizeTokens(ctrl+c) = %v", got)
	}
}

func TestParseScheduleTimeForms(t *testing.T) {
	now := time.Date(2026, 11, 10, 16, 30, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"In 5m":            now.Add(5 * time.Minute),
		"in:1h30m":         now.Add(90 * time.Minute),
		"30S":              now.Add(30 * time.Second),
		"1am":              time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC),
		"2026-11-12 08:00": time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC),
	} {
		got, err := ParseScheduleTime(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q = %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"5", "25:99", "-5m", "+0s", "2026-11-09 08:00", "soon"} {
		if got, err := ParseScheduleTime(in, now); err == nil {
			t.Errorf("%q accepted: %v", in, got)
		}
	}
}

func TestSchedulerListSorted(t *testing.T) {
	s := NewScheduler(nil)
	defer s.Close()
	now := time.Now()
	for _, d := range []time.Duration{3 * time.Hour, time.Hour, 2 * time.Hour} {
		if _, err := s.Schedule("p", now.Add(d), []string{"x"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	list := s.List()
	for i := 1; i < len(list); i++ {
		if list[i].RunAt.Before(list[i-1].RunAt) {
			t.Fatalf("not sorted: %v", list)
		}
	}
}

func TestSchedulerPersists(t *testing.T) {
	path := t.TempDir() + "/schedule.json"
	s := NewScheduler(nil)
	if err := s.Load(path); err != nil {
		t.Fatal(err)
	}
	j, _ := s.Schedule("p1", time.Now().Add(time.Hour), []string{"text:continue", "enter"}, "continue")
	s.Schedule("p2", time.Now().Add(2*time.Hour), []string{"x"}, "")
	s.Cancel(j.ID)
	s.Close()

	var mu sync.Mutex
	var ran []string
	s2 := NewScheduler(func(pane string, _ []string) error {
		mu.Lock()
		ran = append(ran, pane)
		mu.Unlock()
		return nil
	})
	defer s2.Close()
	if err := s2.Load(path); err != nil {
		t.Fatal(err)
	}
	if list := s2.List(); len(list) != 1 || list[0].Pane != "p2" {
		t.Fatalf("reloaded %+v", list)
	}
}

func TestSchedulerLoadOverdue(t *testing.T) {
	path := t.TempDir() + "/schedule.json"
	s := NewScheduler(nil)
	s.Load(path)
	s.Schedule("late", time.Now().Add(time.Hour), []string{"x"}, "")
	s.Schedule("old", time.Now().Add(time.Hour), []string{"x"}, "")
	s.Close()
	// Rewrite the times as if the daemon had been away.
	s.mu.Lock()
	s.jobs[0].RunAt = time.Now().Add(-time.Minute)
	s.jobs[1].RunAt = time.Now().Add(-2 * time.Hour)
	s.saveLocked()
	s.mu.Unlock()

	s2 := NewScheduler(nil)
	defer s2.Close()
	s2.Load(path)
	list := s2.List()
	if len(list) != 1 || list[0].Pane != "late" || time.Until(list[0].RunAt) < loadGrace-time.Second {
		t.Fatalf("overdue jobs: %+v", list)
	}
}

func TestFormatAt(t *testing.T) {
	now := time.Date(2026, 11, 10, 22, 0, 0, 0, time.UTC)
	for at, want := range map[time.Time]string{
		time.Date(2026, 11, 10, 23, 5, 0, 0, time.UTC): "23:05",
		time.Date(2026, 11, 11, 1, 5, 0, 0, time.UTC):  "tomorrow 01:05",
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC):  "Nov 13 09:00",
	} {
		if got := FormatAt(at, now); got != want {
			t.Errorf("FormatAt(%v) = %q, want %q", at, got, want)
		}
	}
}
