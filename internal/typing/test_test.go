package typing

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func at(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

// typeAt types s one rune every step seconds from start, and returns the
// time after the last rune.
func typeAt(t *Test, s string, start, step float64) float64 {
	for _, r := range s {
		t.Type(r, at(start))
		start += step
	}
	return start - step
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestModes(t *testing.T) {
	m, ok := ParseMode(DefaultMode)
	if !ok || m != (Mode{"time", 30}) || m.String() != "time 30" {
		t.Errorf("default mode %v %v", m, ok)
	}
	if _, ok := ParseMode("time 31"); ok {
		t.Error("only the listed modes parse")
	}
}

func TestPerfectRun(t *testing.T) {
	tt := New([]string{"the", "cat", "sat"}, Mode{"words", 2})
	end := typeAt(tt, "the cat", 0, 0.5)
	if !tt.Done(at(end)) {
		t.Fatal("the last word typed exactly ends a words test")
	}
	r := tt.Result(at(end + 5))
	if r.Seconds != 3 || !near(r.WPM, 28) || !near(r.Raw, 28) || r.Accuracy != 100 || len(r.Samples) != 3 {
		t.Errorf("result %+v", r)
	}
	if r.Correct != 7 || r.Incorrect != 0 || r.Extra != 0 || r.Missed != 0 {
		t.Errorf("chars %+v", r)
	}
}

func TestBackIntoWrongWord(t *testing.T) {
	tt := New([]string{"the", "cat"}, Mode{"words", 2})
	typeAt(tt, "thx ", 0, 0.1)
	if tt.Cur != 1 {
		t.Fatalf("space commits the word: cur %d", tt.Cur)
	}
	tt.Backspace(at(1))
	if tt.Cur != 0 || tt.Typed[0] != "thx" {
		t.Fatalf("backspace goes back into a wrong word: cur %d %q", tt.Cur, tt.Typed[0])
	}
	tt.Backspace(at(1.1))
	tt.Type('e', at(1.2))
	if tt.Typed[0] != "the" {
		t.Errorf("typed %q", tt.Typed[0])
	}
	if r := tt.Result(at(2)); !near(r.Accuracy, 60) {
		t.Errorf("accuracy %v, want 60 (x and the space were wrong)", r.Accuracy)
	}
	// A right word stays committed.
	typeAt(tt, " c", 2, 0.1)
	tt.Backspace(at(3))
	tt.Backspace(at(3.1))
	if tt.Cur != 1 {
		t.Errorf("backspace must not go back into a right word: cur %d", tt.Cur)
	}
}

func TestDeleteWord(t *testing.T) {
	tt := New([]string{"the", "cat"}, Mode{"words", 2})
	typeAt(tt, "thx ca", 0, 0.1)
	tt.DeleteWord(at(1))
	if tt.Cur != 1 || tt.Typed[1] != "" {
		t.Fatalf("clears the word: cur %d %q", tt.Cur, tt.Typed[1])
	}
	tt.DeleteWord(at(1.1))
	if tt.Cur != 0 || tt.Typed[0] != "" {
		t.Errorf("then the previous wrong word: cur %d %q", tt.Cur, tt.Typed[0])
	}
}

func TestLeadingSpaceIgnored(t *testing.T) {
	tt := New([]string{"the"}, Mode{"words", 1})
	tt.Type(' ', at(0))
	if tt.Started() || tt.Cur != 0 {
		t.Error("a space on an empty word does nothing, not even start the clock")
	}
}

func TestPauseExcludesTime(t *testing.T) {
	tt := New([]string{"the", "cat"}, Mode{"time", 30})
	tt.Type('t', at(0))
	tt.Pause(at(1))
	tt.Type('h', at(10))
	if got := tt.Result(at(11)).Seconds; got != 2 {
		t.Errorf("seconds %v, want 2", got)
	}
}

func TestTimeModeEnds(t *testing.T) {
	tt := New([]string{"the", "cat"}, Mode{"time", 15})
	tt.Type('t', at(0))
	if tt.Done(at(14.9)) || tt.Remaining(at(14)) != time.Second {
		t.Fatal("not done before 15 s")
	}
	if !tt.Done(at(15)) || tt.Remaining(at(16)) != 0 {
		t.Error("done at 15 s")
	}
	tt.Type('h', at(16))
	if tt.Typed[0] != "t" || tt.Result(at(20)).Seconds != 15 {
		t.Error("nothing changes once done, and the time stays capped")
	}
}

func TestTypeNonASCIIIsWrong(t *testing.T) {
	tt := New([]string{"cafe"}, Mode{"words", 1})
	tt.Type('é', at(0))
	if r := tt.Result(at(1)); r.Accuracy != 0 || r.Incorrect != 1 {
		t.Errorf("result %+v", r)
	}
}

func TestExtraLettersCapped(t *testing.T) {
	tt := New([]string{"cat"}, Mode{"words", 1})
	for i := range 20 {
		tt.Type('z', at(float64(i)/10))
	}
	if len(tt.Typed[0]) != len("cat")+maxExtra {
		t.Errorf("typed %d letters", len(tt.Typed[0]))
	}
}

func TestAppendAndNeedsMore(t *testing.T) {
	tt := New(make([]string, 10), Mode{"time", 30})
	if !tt.NeedsMore() {
		t.Error("10 words left: needs more")
	}
	tt.Append(make([]string, 50))
	if tt.NeedsMore() || len(tt.Typed) != 60 {
		t.Error("60 words: enough")
	}
	w := New(make([]string, 10), Mode{"words", 25})
	if !w.NeedsMore() {
		t.Error("a words test with too few words asks for more")
	}
	w.Append(make([]string, 30))
	if w.NeedsMore() || len(w.Words) != 25 || len(w.Typed) != 25 {
		t.Errorf("filled up to its 25 words: %d", len(w.Words))
	}
}
