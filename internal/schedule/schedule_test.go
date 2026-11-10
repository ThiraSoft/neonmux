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

