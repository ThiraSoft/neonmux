package schedule

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LimitInfo describes a rate or usage limit detected on a pane.
type LimitInfo struct {
	App      string    `json:"app"`
	ResetAt  time.Time `json:"reset_at"`
	ResumeAt time.Time `json:"resume_at"` // ResetAt + 1 minute
	Message  string    `json:"message"`
	RawTime  string    `json:"raw_time"`
	Command  string    `json:"command"` // what to type once it resets
}

var (
	// "resets 5:20pm (Europe/Paris)", "resets 17:30", "reset at 5:20 PM"
	reResetTime = regexp.MustCompile(`(?i)\b(?:reset at|resets?)\s+([0-9]{1,2}(?::[0-9]{2})?\s*(?:am|pm)?)(?:\s*\(([^)]+)\))?`)

	// "resets in 1 hour 45 minutes", "resets in 45m", "resets in 2h"
	reResetIn = regexp.MustCompile(`(?i)\bresets?\s+in\s+(?:([0-9]+)\s*h(?:ours?|rs?)?)?\s*(?:([0-9]+)\s*m(?:in(?:utes?|s)?)?)?`)

	// "retry after 60s", "try again in 10 minutes", "retry in 5m"
	reRetryAfter = regexp.MustCompile(`(?i)\b(?:retry after|try again in|retry in)\s+([0-9]+)\s*(s(?:ec(?:onds?|s)?)?|m(?:in(?:utes?|s)?)?|h(?:ours?|rs?)?)\b`)

	// A status line at the end of the window: "5h 100% ↻ 17:20". The clock
	// alone is no limit: the status line shows it at any usage.
	reStatusFull = regexp.MustCompile(`([0-9]{2,3})%\s*↻\s*([0-9]{1,2}:[0-9]{2}\s*(?:am|pm)?)`)

	// What says that the time next to it is the end of a limit.
	reLimitWords = regexp.MustCompile(`(?i)limit|quota|exhausted|too many requests|at capacity|rate.?limited|\b429\b`)
)

// staleAfter is how long a reset clock time already past still counts as
// gone by, rather than tomorrow's: longer than any limit window.
const staleAfter = 6 * time.Hour

// DetectLimit scans the lines of a screen for a usage or rate limit of
// Claude Code or Antigravity. A reset time counts only next to the words of
// a limit, or on a status line that shows 100 %; a reset already past is an
// old message, not a limit.
func DetectLimit(lines []string, now time.Time) (*LimitInfo, error) {
	if len(lines) == 0 {
		return nil, errors.New("no lines to analyze")
	}
	// The status line says what holds now: it goes first.
	for i := len(lines) - 1; i >= 0; i-- {
		m := reStatusFull.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		if pct, _ := strconv.Atoi(m[1]); pct < 100 {
			continue
		}
		raw := strings.TrimSpace(m[2])
		if at, ok := limitClock(raw, "", now); ok {
			return found(lines, lines[i], raw, at), nil
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || !nearLimitWords(lines, i) {
			continue
		}
		if m := reResetIn.FindStringSubmatch(line); m != nil && (m[1] != "" || m[2] != "") {
			h, _ := strconv.Atoi(m[1])
			mins, _ := strconv.Atoi(m[2])
			if d := time.Duration(h)*time.Hour + time.Duration(mins)*time.Minute; d > 0 {
				return found(lines, line, strings.TrimSpace(m[0]), now.Add(d)), nil
			}
		}
		if m := reRetryAfter.FindStringSubmatch(line); m != nil {
			val, _ := strconv.Atoi(m[1])
			var unit time.Duration
			switch strings.ToLower(m[2])[0] {
			case 's':
				unit = time.Second
			case 'm':
				unit = time.Minute
			case 'h':
				unit = time.Hour
			}
			if d := time.Duration(val) * unit; d > 0 {
				return found(lines, line, strings.TrimSpace(m[0]), now.Add(d)), nil
			}
		}
		if m := reResetTime.FindStringSubmatch(line); m != nil {
			raw := strings.TrimSpace(m[1])
			if at, ok := limitClock(raw, strings.TrimSpace(m[2]), now); ok {
				return found(lines, line, raw, at), nil
			}
		}
	}
	return nil, errors.New("no rate limit detected")
}

// nearLimitWords says whether line i or the two above it speak of a limit:
// a message can wrap its reset time onto the next line.
func nearLimitWords(lines []string, i int) bool {
	for j := i; j >= 0 && j >= i-2; j-- {
		if reLimitWords.MatchString(lines[j]) {
			return true
		}
	}
	return false
}

// limitClock is the reset at clock time raw, false when that time went by
// a moment ago: the message on screen is then an old one.
func limitClock(raw, tz string, now time.Time) (time.Time, bool) {
	at, err := parseClockTime(raw, tz, now)
	if err != nil {
		return time.Time{}, false
	}
	if at.Sub(now) > 24*time.Hour-staleAfter {
		return time.Time{}, false
	}
	return at, true
}

func found(lines []string, line, raw string, reset time.Time) *LimitInfo {
	return &LimitInfo{
		App:      detectApp(lines),
		ResetAt:  reset,
		ResumeAt: reset.Add(time.Minute),
		Message:  strings.TrimSpace(line),
		RawTime:  raw,
		Command:  "continue",
	}
}

// detectApp guesses whether the terminal runs claude or agy.
func detectApp(lines []string) string {
	lower := strings.ToLower(strings.Join(lines, " "))
	switch {
	case strings.Contains(lower, "claude") || strings.Contains(lower, "5-hour limit"):
		return "claude"
	case strings.Contains(lower, "agy") || strings.Contains(lower, "antigravity") || strings.Contains(lower, "gemini"):
		return "agy"
	}
	return "agent"
}

// parseClockTime reads "5:20pm", "5pm", "17:20" or "17:20:00", in the time
// zone tz when it is a known one, and returns its next occurrence: today, or
// tomorrow when it went by more than 2 minutes ago. A bare number is no
// clock time.
func parseClockTime(timeStr, tzStr string, now time.Time) (time.Time, error) {
	loc := now.Location()
	if tzStr != "" {
		if l, err := time.LoadLocation(tzStr); err == nil {
			loc = l
		}
	}
	clean := strings.ToLower(strings.TrimSpace(timeStr))
	isPM := strings.HasSuffix(clean, "pm")
	isAM := strings.HasSuffix(clean, "am")
	clean = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(clean, "pm"), "am"))
	parts := strings.Split(clean, ":")
	if len(parts) > 3 || (len(parts) == 1 && !isPM && !isAM) {
		return time.Time{}, fmt.Errorf("invalid time %q", timeStr)
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (i > 0 && len(p) != 2) {
			return time.Time{}, fmt.Errorf("invalid time %q", timeStr)
		}
		v[i] = n
	}
	hour, min, sec := v[0], v[1], v[2]
	if isPM || isAM {
		if hour < 1 || hour > 12 {
			return time.Time{}, fmt.Errorf("invalid time %q", timeStr)
		}
		if isPM && hour < 12 {
			hour += 12
		} else if isAM && hour == 12 {
			hour = 0
		}
	}
	if hour > 23 || min > 59 || sec > 59 {
		return time.Time{}, fmt.Errorf("invalid time %q", timeStr)
	}
	n := now.In(loc)
	at := time.Date(n.Year(), n.Month(), n.Day(), hour, min, sec, 0, loc)
	if at.Before(n.Add(-2 * time.Minute)) {
		// AddDate, not 24 h: the day of a clock change lasts 23 or 25.
		at = time.Date(n.Year(), n.Month(), n.Day()+1, hour, min, sec, 0, loc)
	}
	return at, nil
}
