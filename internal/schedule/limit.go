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
	Command  string    `json:"command"` // Suggested resume command, e.g. "continue"
}

// Regexes to extract reset time from terminal output.
var (
	// Matches "resets 5:20pm (Europe/Paris)" or "resets 17:30" or "reset at 5:20 PM"
	reResetTime = regexp.MustCompile(`(?i)(?:resets?|reset at)\s+([0-9]{1,2}(?::[0-9]{2})?\s*(?:am|pm)?)(?:\s*\(([^)]+)\))?`)

	// Matches status bar icon "↻ 17:20" or "↻ 5:20pm"
	reResetClock = regexp.MustCompile(`(?:↻|reset:)\s*([0-9]{1,2}:[0-9]{2}\s*(?:am|pm)?)`)

	// Matches "resets in 1 hour 45 minutes" or "resets in 45m" or "resets in 2h"
	reResetIn = regexp.MustCompile(`(?i)resets?\s+in\s+(?:([0-9]+)\s*h(?:ours?)?)?\s*(?:([0-9]+)\s*m(?:in(?:utes?)?)?)?`)

	// Matches "retry after 60s", "try again in 10 minutes", or "retry in 5m"
	reRetryAfter = regexp.MustCompile(`(?i)(?:retry after|try again in|retry in)\s+([0-9]+)\s*(s(?:ec(?:onds?)?)?|m(?:in(?:utes?)?)?|h(?:ours?)?)`)

	// Keywords indicating a limit in Claude Code or AGY
	reLimitKeywords = regexp.MustCompile(`(?i)(?:5-hour limit|usage limit|rate limit|quota exceeded|resource exhausted|too many requests|at capacity)`)
)

// DetectLimit scans terminal screen lines for Claude Code or AGY rate/usage limits.
// If found, it parses the reset time and returns LimitInfo with ResumeAt = ResetAt + 1 min.
func DetectLimit(lines []string, now time.Time) (*LimitInfo, error) {
	if len(lines) == 0 {
		return nil, errors.New("no lines to analyze")
	}

	// Scan from bottom to top (most recent output first)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		// 1. Check for relative duration: "resets in X hours Y minutes"
		if m := reResetIn.FindStringSubmatch(line); len(m) > 0 && (m[1] != "" || m[2] != "") {
			var dur time.Duration
			if m[1] != "" {
				h, _ := strconv.Atoi(m[1])
				dur += time.Duration(h) * time.Hour
			}
			if m[2] != "" {
				mins, _ := strconv.Atoi(m[2])
				dur += time.Duration(mins) * time.Minute
			}
			if dur > 0 {
				resetAt := now.Add(dur)
				return &LimitInfo{
					App:      detectApp(lines, line),
					ResetAt:  resetAt,
					ResumeAt: resetAt.Add(1 * time.Minute),
					Message:  line,
					RawTime:  m[0],
					Command:  "continue",
				}, nil
			}
		}

		// 2. Check for "retry after X s/m/h"
		if m := reRetryAfter.FindStringSubmatch(line); len(m) > 2 {
			val, _ := strconv.Atoi(m[1])
			unit := strings.ToLower(m[2])
			var dur time.Duration
			switch {
			case strings.HasPrefix(unit, "s"):
				dur = time.Duration(val) * time.Second
			case strings.HasPrefix(unit, "m"):
				dur = time.Duration(val) * time.Minute
			case strings.HasPrefix(unit, "h"):
				dur = time.Duration(val) * time.Hour
			}
			if dur > 0 {
				resetAt := now.Add(dur)
				return &LimitInfo{
					App:      detectApp(lines, line),
					ResetAt:  resetAt,
					ResumeAt: resetAt.Add(1 * time.Minute),
					Message:  line,
					RawTime:  m[0],
					Command:  "continue",
				}, nil
			}
		}

		// 3. Check for "resets 5:20pm (Europe/Paris)" or "reset at 17:30"
		if m := reResetTime.FindStringSubmatch(line); len(m) > 1 {
			timeStr := strings.TrimSpace(m[1])
			tzStr := ""
			if len(m) > 2 {
				tzStr = strings.TrimSpace(m[2])
			}
			resetAt, err := parseClockTime(timeStr, tzStr, now)
			if err == nil {
				return &LimitInfo{
					App:      detectApp(lines, line),
					ResetAt:  resetAt,
					ResumeAt: resetAt.Add(1 * time.Minute),
					Message:  line,
					RawTime:  timeStr,
					Command:  "continue",
				}, nil
			}
		}

		// 4. Check for status line clock: "5h 100% ↻ 17:20"
		if m := reResetClock.FindStringSubmatch(line); len(m) > 1 {
			timeStr := strings.TrimSpace(m[1])
			resetAt, err := parseClockTime(timeStr, "", now)
			if err == nil {
				return &LimitInfo{
					App:      detectApp(lines, line),
					ResetAt:  resetAt,
					ResumeAt: resetAt.Add(1 * time.Minute),
					Message:  line,
					RawTime:  timeStr,
					Command:  "continue",
				}, nil
			}
		}
	}

	return nil, errors.New("no rate limit detected")
}

// detectApp guesses whether the terminal is running claude or agy.
func detectApp(lines []string, matchedLine string) string {
	combined := strings.Join(lines, " ")
	lower := strings.ToLower(combined)
	if strings.Contains(lower, "claude") || strings.Contains(lower, "5-hour limit") {
		return "claude"
	}
	if strings.Contains(lower, "agy") || strings.Contains(lower, "antigravity") || strings.Contains(lower, "gemini") {
		return "agy"
	}
	return "agent"
}

// parseClockTime parses e.g. "5:20pm", "5:20 PM", "17:20", "17:20:00"
// with an optional timezone name like "Europe/Paris".
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
	clean = strings.TrimSuffix(strings.TrimSuffix(clean, "pm"), "am")
	clean = strings.TrimSpace(clean)

	parts := strings.Split(clean, ":")
	if len(parts) == 0 {
		return time.Time{}, fmt.Errorf("invalid time %q", timeStr)
	}

	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return time.Time{}, err
	}
	min := 0
	if len(parts) > 1 {
		min, err = strconv.Atoi(parts[1])
		if err != nil {
			return time.Time{}, err
		}
	}
	sec := 0
	if len(parts) > 2 {
		sec, _ = strconv.Atoi(parts[2])
	}

	if isPM && hour < 12 {
		hour += 12
	} else if isAM && hour == 12 {
		hour = 0
	}

	// Build date in target location
	nowInLoc := now.In(loc)
	candidate := time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), hour, min, sec, 0, loc)

	// If candidate time is already in the past by more than 2 minutes, it refers to tomorrow
	if candidate.Before(nowInLoc.Add(-2 * time.Minute)) {
		candidate = candidate.Add(24 * time.Hour)
	}

	return candidate, nil
}
