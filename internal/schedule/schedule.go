package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
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
}

// ExecFunc is called when a scheduled job is due.
type ExecFunc func(pane string, tokens []string) error

// Scheduler keeps the jobs to come, in memory and, once Load gave it a
// file, on disk: a daemon restart finds them again.
type Scheduler struct {
	mu      sync.Mutex
	exec    ExecFunc
	jobs    []*Job // sorted by RunAt
	timer   *time.Timer
	closed  bool
	path    string
	onEvent func(event string, job Job)
}

// Overdue jobs found on disk run after loadGrace, so the panes of a hot
// restart are back first; those more than loadMaxLate late are dropped.
const (
	loadGrace   = 5 * time.Second
	loadMaxLate = time.Hour
)

// NewScheduler creates an active scheduler.
func NewScheduler(exec ExecFunc) *Scheduler {
	return &Scheduler{exec: exec}
}

// OnEvent sets f, called outside the lock with "scheduled", "cancelled" or
// "executed" after each change of the list.
func (s *Scheduler) OnEvent(f func(event string, job Job)) {
	s.mu.Lock()
	s.onEvent = f
	s.mu.Unlock()
}

// Load reads the jobs saved in path and saves there from now on.
func (s *Scheduler) Load(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved []*Job
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	now := time.Now()
	for _, j := range saved {
		if j == nil || j.ID == "" || j.Pane == "" || len(j.Tokens) == 0 {
			continue
		}
		if now.Sub(j.RunAt) > loadMaxLate {
			log.Printf("schedule: job %s on %s dropped, due at %s", j.ID, j.Pane, j.RunAt.Format(time.DateTime))
			continue
		}
		if j.RunAt.Before(now.Add(loadGrace)) {
			j.RunAt = now.Add(loadGrace)
		}
		s.insertLocked(j)
	}
	s.rearmLocked()
	return nil
}

// Schedule queues a job to be executed at runAt.
func (s *Scheduler) Schedule(pane string, runAt time.Time, tokens []string, desc string) (*Job, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("scheduler is closed")
	}
	if pane == "" {
		s.mu.Unlock()
		return nil, errors.New("empty pane")
	}
	if len(tokens) == 0 {
		s.mu.Unlock()
		return nil, errors.New("empty command")
	}
	if desc == "" {
		desc = strings.Join(tokens, " ")
	}
	j := &Job{
		ID:        s.newIDLocked(),
		Pane:      pane,
		RunAt:     runAt,
		Tokens:    slices.Clone(tokens),
		Desc:      desc,
		CreatedAt: time.Now(),
	}
	s.insertLocked(j)
	s.rearmLocked()
	s.saveLocked()
	f := s.onEvent
	s.mu.Unlock()
	if f != nil {
		f("scheduled", *j)
	}
	return j, nil
}

// Cancel removes a scheduled job by ID.
func (s *Scheduler) Cancel(id string) bool {
	s.mu.Lock()
	i := slices.IndexFunc(s.jobs, func(j *Job) bool { return j.ID == id })
	if i < 0 {
		s.mu.Unlock()
		return false
	}
	j := s.jobs[i]
	s.jobs = slices.Delete(s.jobs, i, i+1)
	s.rearmLocked()
	s.saveLocked()
	f := s.onEvent
	s.mu.Unlock()
	if f != nil {
		f("cancelled", *j)
	}
	return true
}

// Get returns the job id, if it is still to come.
func (s *Scheduler) Get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			return *j, true
		}
	}
	return Job{}, false
}

// List returns a copy of the jobs to come, the next one first.
func (s *Scheduler) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, *j)
	}
	return out
}

func (s *Scheduler) insertLocked(j *Job) {
	i, _ := slices.BinarySearchFunc(s.jobs, j.RunAt, func(e *Job, t time.Time) int {
		if e.RunAt.After(t) {
			return 1
		}
		return -1 // equal times keep their order
	})
	s.jobs = slices.Insert(s.jobs, i, j)
}

func (s *Scheduler) newIDLocked() string {
	for {
		id := randomID(6)
		if !slices.ContainsFunc(s.jobs, func(j *Job) bool { return j.ID == id }) {
			return id
		}
	}
}

// rearmLocked sets the timer to the next job.
func (s *Scheduler) rearmLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.closed || len(s.jobs) == 0 {
		return
	}
	s.timer = time.AfterFunc(max(time.Until(s.jobs[0].RunAt), 0), s.fire)
}

// saveLocked writes the jobs to the file given to Load, through a rename so
// a crash never leaves half a file.
func (s *Scheduler) saveLocked() {
	if s.path == "" {
		return
	}
	data, err := json.Marshal(s.jobs)
	if err == nil {
		tmp := s.path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, s.path)
		}
	}
	if err != nil {
		log.Printf("schedule: save %s: %v", filepath.Base(s.path), err)
	}
}

func (s *Scheduler) fire() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	n := 0
	for n < len(s.jobs) && !now.Before(s.jobs[n].RunAt) {
		n++
	}
	toRun := slices.Clone(s.jobs[:n])
	s.jobs = slices.Delete(s.jobs, 0, n)
	s.rearmLocked()
	if n > 0 {
		s.saveLocked()
	}
	f := s.onEvent
	s.mu.Unlock()

	for _, j := range toRun {
		if s.exec != nil {
			if err := s.exec(j.Pane, j.Tokens); err != nil {
				log.Printf("schedule: job %s on %s: %v", j.ID, j.Pane, err)
			}
		}
		if f != nil {
			f("executed", *j)
		}
	}
}

// Close stops the scheduler. The file keeps the jobs for the next daemon.
func (s *Scheduler) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// Reopen undoes Close: the jobs to come wait again.
func (s *Scheduler) Reopen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = false
	s.rearmLocked()
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
	return []string{"text:" + strings.Join(args, " "), "enter"}
}

// ParseScheduleTime parses a relative duration ("+5m", "30s", "in 10m",
// "1h30m") or an absolute time ("17:21", "5:21pm", "2026-11-10T17:21:00").
// A bare number is neither: "5" is refused rather than read as 05:00.
func ParseScheduleTime(input string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return time.Time{}, errors.New("empty time")
	}
	rel := s
	lower := strings.ToLower(rel)
	switch {
	case strings.HasPrefix(rel, "+"):
		rel = rel[1:]
	case strings.HasPrefix(lower, "in:"), strings.HasPrefix(lower, "in "):
		rel = strings.TrimSpace(rel[3:])
	}
	if d, err := time.ParseDuration(strings.ToLower(rel)); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("%q is not in the future", input)
		}
		return now.Add(d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			if !t.After(now) {
				return time.Time{}, fmt.Errorf("%s is already past", t.Format("2006-01-02 15:04"))
			}
			return t, nil
		}
	}
	t, err := parseClockTime(s, "", now)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q: use +5m, 1h30m, 17:21 or 5:21pm", input)
	}
	return t, nil
}

// FormatAt is a job's time as people read it: "17:21" today, "tomorrow
// 01:05", or the date further on.
func FormatAt(at, now time.Time) string {
	at = at.In(now.Location())
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch day := at.Sub(today); {
	case day >= 0 && day < 24*time.Hour:
		return at.Format("15:04")
	case at.Before(time.Date(y, m, d+2, 0, 0, 0, 0, now.Location())) && at.After(today):
		return "tomorrow " + at.Format("15:04")
	}
	return at.Format("Jan 2 15:04")
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
