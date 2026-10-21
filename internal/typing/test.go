// Package typing is the engine of the typing test, in the spirit of
// monkeytype: the words to type, what was typed, and the scoring. It knows
// nothing of the screen; the times come from the caller.
package typing

import (
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/ThiraSoft/neonmux/internal/games"
)

// Mode is a test against the clock (N seconds) or over N words.
type Mode struct {
	Kind string
	N    int
}

var Modes = []Mode{{"time", 15}, {"time", 30}, {"time", 60}, {"words", 25}, {"words", 50}, {"words", 100}}

const DefaultMode = "time 30"

func (m Mode) String() string { return m.Kind + " " + strconv.Itoa(m.N) }

// ParseMode reads a mode written by String, among the listed ones.
func ParseMode(s string) (Mode, bool) {
	for _, m := range Modes {
		if m.String() == s {
			return m, true
		}
	}
	return Mode{}, false
}

const (
	// maxExtra caps the letters typed beyond the end of a word.
	maxExtra = 10
	// lowWater is the number of words left under which a test against the
	// clock asks for more text.
	lowWater = 40
)

// key is a keystroke that counts for accuracy: a letter or the space that
// ends a word (backspaces do not count).
type key struct {
	at    time.Duration // playing time when it was typed
	right bool
}

// Test is one run of the typing test.
type Test struct {
	Words []string
	Typed []string
	Cur   int
	Mode  Mode

	clock games.Clock
	keys  []key
	done  bool
}

// New prepares a test. A test over N words keeps the first N.
func New(words []string, m Mode) *Test {
	if m.Kind == "words" && len(words) > m.N {
		words = words[:m.N]
	}
	return &Test{Words: words, Typed: make([]string, len(words)), Mode: m}
}

// Append adds text at the end: more to type against the clock, or the
// words a words test still lacks.
func (t *Test) Append(words []string) {
	if t.Mode.Kind == "words" {
		words = words[:max(0, min(len(words), t.Mode.N-len(t.Words)))]
	}
	t.Words = append(t.Words, words...)
	t.Typed = append(t.Typed, make([]string, len(words))...)
}

// NeedsMore reports a test against the clock that is running out of text,
// or a words test whose article was too short for its count.
func (t *Test) NeedsMore() bool {
	if t.Mode.Kind == "words" {
		return len(t.Words) < t.Mode.N
	}
	return len(t.Words)-t.Cur < lowWater
}

func (t *Test) Started() bool { return t.clock.Started() }

// Pause stops the clock while the player is away; the next key resumes it.
func (t *Test) Pause(now time.Time) { t.clock.Pause(now) }

// Done reports the end of the test: time is up, or every word is typed.
func (t *Test) Done(now time.Time) bool {
	if !t.done && t.Mode.Kind == "time" && t.clock.Elapsed(now) >= t.limit() {
		t.done = true
		t.clock.Pause(now)
	}
	return t.done
}

func (t *Test) limit() time.Duration { return time.Duration(t.Mode.N) * time.Second }

// Remaining returns the time left in a test against the clock.
func (t *Test) Remaining(now time.Time) time.Duration {
	if t.Mode.Kind != "time" {
		return 0
	}
	return max(0, t.limit()-t.clock.Elapsed(now))
}

// elapsed is the playing time, capped at the limit of a test against the
// clock (the last frame may come a little late).
func (t *Test) elapsed(now time.Time) time.Duration {
	e := t.clock.Elapsed(now)
	if t.Mode.Kind == "time" {
		e = min(e, t.limit())
	}
	return e
}

// input prepares a keystroke: nothing once the test is over, otherwise the
// clock runs.
func (t *Test) input(now time.Time) bool {
	if t.Done(now) || t.Cur >= len(t.Words) {
		return false
	}
	t.clock.Resume(now)
	return true
}

// Type handles a typed character. A space ends the word.
func (t *Test) Type(r rune, now time.Time) {
	if r == ' ' {
		if t.Cur >= len(t.Words) || t.Typed[t.Cur] == "" || !t.input(now) {
			return
		}
		t.keys = append(t.keys, key{t.clock.Elapsed(now), t.Typed[t.Cur] == t.Words[t.Cur]})
		t.Cur++
		if t.Mode.Kind == "words" && t.Cur == len(t.Words) {
			t.end(now)
		}
		return
	}
	if !t.input(now) {
		return
	}
	word, typed := t.Words[t.Cur], t.Typed[t.Cur]
	pos := utf8.RuneCountInString(typed)
	if pos >= len(word)+maxExtra {
		return
	}
	t.keys = append(t.keys, key{t.clock.Elapsed(now), pos < len(word) && rune(word[pos]) == r})
	t.Typed[t.Cur] += string(r)
	if t.Mode.Kind == "words" && t.Cur == len(t.Words)-1 && t.Typed[t.Cur] == word {
		t.end(now)
	}
}

func (t *Test) end(now time.Time) {
	t.done = true
	t.clock.Pause(now)
}

// Backspace removes a letter, or goes back into the previous word if it is
// wrong (a right word stays committed).
func (t *Test) Backspace(now time.Time) {
	if !t.input(now) {
		return
	}
	if s := t.Typed[t.Cur]; s != "" {
		_, n := utf8.DecodeLastRuneInString(s)
		t.Typed[t.Cur] = s[:len(s)-n]
		return
	}
	t.backIntoWrong()
}

// DeleteWord clears the word, or the previous one if it is wrong.
func (t *Test) DeleteWord(now time.Time) {
	if !t.input(now) {
		return
	}
	if t.Typed[t.Cur] == "" && !t.backIntoWrong() {
		return
	}
	t.Typed[t.Cur] = ""
}

func (t *Test) backIntoWrong() bool {
	if t.Cur > 0 && t.Typed[t.Cur-1] != t.Words[t.Cur-1] {
		t.Cur--
		return true
	}
	return false
}

// Result is the score of a test.
type Result struct {
	WPM, Raw, Accuracy, Seconds       float64
	Correct, Incorrect, Extra, Missed int
	Samples                           []Sample
}

// Sample is one second of the speed curve.
type Sample struct {
	WPM, Raw float64
	Errors   int
}

// Result scores the test as monkeytype does: WPM counts the characters of
// the right words (and their spaces), raw every keystroke, both divided by
// five per minute of play.
func (t *Test) Result(now time.Time) Result {
	el := t.elapsed(now)
	r := Result{Seconds: el.Seconds()}
	last := min(t.Cur, len(t.Words)-1)
	for i := 0; i <= last; i++ {
		word, typed := t.Words[i], []rune(t.Typed[i])
		committed := i < t.Cur
		switch {
		case committed && string(typed) == word:
			r.Correct += len(word) + 1
		case !committed && t.done && string(typed) == word:
			r.Correct += len(word)
		}
		for j := 0; j < min(len(typed), len(word)); j++ {
			if typed[j] != rune(word[j]) {
				r.Incorrect++
			}
		}
		r.Extra += max(0, len(typed)-len(word))
		if committed {
			r.Missed += max(0, len(word)-len(typed))
		}
	}
	right := 0
	for _, k := range t.keys {
		if k.right {
			right++
		}
	}
	if len(t.keys) > 0 {
		r.Accuracy = 100 * float64(right) / float64(len(t.keys))
	}
	if r.Seconds > 0 {
		r.WPM = 12 * float64(r.Correct) / r.Seconds
		r.Raw = 12 * float64(len(t.keys)) / r.Seconds
	}
	r.Samples = t.samples(el)
	return r
}

func (t *Test) samples(el time.Duration) []Sample {
	n := int(el / time.Second)
	out := make([]Sample, n)
	k, right := 0, 0
	for s := 1; s <= n; s++ {
		end := time.Duration(s) * time.Second
		errs := 0
		for ; k < len(t.keys) && t.keys[k].at <= end; k++ {
			if t.keys[k].right {
				right++
			} else {
				errs++
			}
		}
		out[s-1] = Sample{WPM: 12 * float64(right) / float64(s), Raw: 12 * float64(k) / float64(s), Errors: errs}
	}
	return out
}
