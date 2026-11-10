package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Job represents a scheduled command to run on a pane.
type Job struct {
	ID        string    `json:"id"`
	Pane      string    `json:"pane"`
	RunAt     time.Time `json:"run_at"`
	Tokens    []string  `json:"tokens"`
	Desc      string    `json:"desc"`
	CreatedAt time.Time `json:"created_at"`
	Done      bool      `json:"done"`
}

// ExecFunc is called when a scheduled job is due.
type ExecFunc func(pane string, tokens []string) error

// Scheduler manages in-memory scheduled jobs.
type Scheduler struct {
	mu      sync.Mutex
	exec    ExecFunc
	jobs    []*Job
	timer   *time.Timer
	closed  bool
	OnEvent func(event string, job *Job)
}

// NewScheduler creates an active scheduler.
func NewScheduler(exec ExecFunc) *Scheduler {
	return &Scheduler{
		exec: exec,
	}
}

// Schedule queues a job to be executed at runAt.
func (s *Scheduler) Schedule(pane string, runAt time.Time, tokens []string, desc string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, errors.New("scheduler is closed")
	}
	if pane == "" {
		return nil, errors.New("empty pane")
	}
	if len(tokens) == 0 {
		return nil, errors.New("empty command/tokens")
	}

	id := randomID(6)
	if desc == "" {
		desc = strings.Join(tokens, " ")
	}

	j := &Job{
		ID:        id,
		Pane:      pane,
		RunAt:     runAt,
		Tokens:    tokens,
		Desc:      desc,
		CreatedAt: time.Now(),
	}

	s.jobs = append(s.jobs, j)
	s.rearmLocked()

	if s.OnEvent != nil {
		s.OnEvent("scheduled", j)
	}

	return j, nil
}

// Cancel removes a scheduled job by ID.
func (s *Scheduler) Cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, j := range s.jobs {
		if j.ID == id && !j.Done {
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			s.rearmLocked()
			if s.OnEvent != nil {
				s.OnEvent("cancelled", j)
			}
			return true
		}
	}
	return false
}

// List returns a copy of pending (not done) jobs.
func (s *Scheduler) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []Job
	for _, j := range s.jobs {
		if !j.Done {
			out = append(out, *j)
		}
	}
	return out
}

// rearmLocked resets the internal timer to fire at the earliest pending job.
func (s *Scheduler) rearmLocked() {
	if s.closed {
		return
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}

	// Prune completed jobs to keep memory bounded
	active := s.jobs[:0]
	for _, j := range s.jobs {
		if !j.Done {
			active = append(active, j)
		}
	}
	s.jobs = active

	var earliest *Job
	for _, j := range s.jobs {
		if earliest == nil || j.RunAt.Before(earliest.RunAt) {
			earliest = j
		}
	}

	if earliest == nil {
		return
	}

	delay := time.Until(earliest.RunAt)
	if delay < 0 {
		delay = 0
	}

	s.timer = time.AfterFunc(delay, s.fire)
}

func (s *Scheduler) fire() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	now := time.Now()
	var toRun []*Job
	for _, j := range s.jobs {
		if !j.Done && !now.Before(j.RunAt) {
			j.Done = true
			toRun = append(toRun, j)
		}
	}
	s.rearmLocked()
	s.mu.Unlock()

	for _, j := range toRun {
		if s.exec != nil {
			_ = s.exec(j.Pane, j.Tokens)
		}
		if s.OnEvent != nil {
			s.OnEvent("executed", j)
		}
	}
}

// Close stops the scheduler.
func (s *Scheduler) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// NormalizeTokens converts command string or tokens into tokens suitable for keys.EncodeTokens.
// If given e.g. ["continue"], it outputs ["text:continue", "enter"].
// If given ["text:foo", "enter"], it leaves them intact.
func NormalizeTokens(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	hasText := false
	hasEnter := false
	isSpecialKey := false
	for _, a := range args {
		if strings.HasPrefix(a, "text:") {
			hasText = true
		}
		if a == "enter" || a == "return" {
			hasEnter = true
		}
		if strings.HasPrefix(a, "ctrl+") || strings.HasPrefix(a, "alt+") || a == "escape" || a == "tab" {
			isSpecialKey = true
		}
	}
	if hasText || (len(args) > 1 && hasEnter) || (len(args) == 1 && isSpecialKey) {
		return args
	}

	cmd := strings.Join(args, " ")
	return []string{"text:" + cmd, "enter"}
}

// ParseScheduleTime parses a relative duration ("+5m", "30s", "in:10m")
// or an absolute time ("17:21", "5:21pm", "2026-11-10T17:21:00").
func ParseScheduleTime(input string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return time.Time{}, errors.New("empty time string")
	}

	// 1. Relative: "+5m", "in:5m", "in 5m", "10m", "30s", "1h"
	relStr := s
	if strings.HasPrefix(relStr, "+") {
		relStr = strings.TrimPrefix(relStr, "+")
	} else if strings.HasPrefix(strings.ToLower(relStr), "in:") {
		relStr = strings.TrimPrefix(relStr, "in:")
	} else if strings.HasPrefix(strings.ToLower(relStr), "in ") {
		relStr = strings.TrimPrefix(relStr, "in ")
	}

	if d, err := time.ParseDuration(relStr); err == nil && d > 0 {
		return now.Add(d), nil
	}

	// 2. ISO / RFC3339 timestamp
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}

	// 3. Clock time: "17:21", "17:21:00", "5:21pm"
	return parseClockTime(s, "", now)
}

func randomID(length int) string {
	b := make([]byte, (length+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:length]
}

// FormatDuration returns a short human-readable duration like "12s", "5m", "1h05m".
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%02dm", h, m)
	}
}
