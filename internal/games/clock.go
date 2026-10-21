// Package games holds what the waiting games share: a clock that only runs
// while the player is there, and the records kept in games.json.
package games

import "time"

// Clock measures playing time: it pauses when the game loses the focus
// and resumes at the next key.
type Clock struct {
	acc     time.Duration
	since   time.Time
	running bool
	started bool
}

// Resume starts the clock, or restarts it after a pause.
func (c *Clock) Resume(now time.Time) {
	if c.running {
		return
	}
	c.since, c.running, c.started = now, true, true
}

// Pause stops the clock, keeping the time already played.
func (c *Clock) Pause(now time.Time) {
	if !c.running {
		return
	}
	c.acc += now.Sub(c.since)
	c.running = false
}

func (c *Clock) Running() bool { return c.running }
func (c *Clock) Started() bool { return c.started }

// Elapsed returns the time played until now.
func (c *Clock) Elapsed(now time.Time) time.Duration {
	if c.running {
		return c.acc + now.Sub(c.since)
	}
	return c.acc
}
