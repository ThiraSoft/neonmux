package fx

import (
	"testing"
	"time"
)

// The echo of a keystroke arrives during the wait: AwaitKick returns
// immediately, without waiting for the deadline.
func TestAwaitKickWokenByOutput(t *testing.T) {
	p := NewPacer(60)
	after := p.Kicks()
	go func() {
		time.Sleep(2 * time.Millisecond)
		p.Kick()
	}()
	start := time.Now()
	if !p.AwaitKick(after, time.Second) {
		t.Fatal("the output was not seen")
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("woke up too late: %v", d)
	}
}

// An output that arrived before the call counts too: no wait at all.
func TestAwaitKickAlreadyArrived(t *testing.T) {
	p := NewPacer(60)
	after := p.Kicks()
	p.Kick()
	start := time.Now()
	if !p.AwaitKick(after, time.Second) {
		t.Fatal("the output that had already arrived was not seen")
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("needless wait: %v", d)
	}
}

// With no output, the wait stops at the deadline.
func TestAwaitKickTimeout(t *testing.T) {
	p := NewPacer(60)
	start := time.Now()
	if p.AwaitKick(p.Kicks(), 20*time.Millisecond) {
		t.Fatal("no output, AwaitKick should have failed")
	}
	if d := time.Since(start); d < 20*time.Millisecond {
		t.Fatalf("deadline not honored: %v", d)
	}
}

// The Kick received during the wait belongs to the current frame: it must
// not arm an extra frame.
func TestKickDuringAwaitSkipsQueue(t *testing.T) {
	p := &Pacer{kick: make(chan struct{}, 1), reconf: make(chan struct{}, 1), woken: make(chan struct{}, 1)}
	go func() {
		time.Sleep(2 * time.Millisecond)
		p.Kick()
	}()
	p.AwaitKick(p.Kicks(), time.Second)
	select {
	case <-p.kick:
		t.Fatal("an extra frame was requested")
	default:
	}
}

// Urgent is single use: the first output consumes the immediate frame,
// the next one goes back through the normal queue.
func TestUrgentIsOneShot(t *testing.T) {
	p := &Pacer{kick: make(chan struct{}, 1), reconf: make(chan struct{}, 1), woken: make(chan struct{}, 1)}
	p.Urgent()
	p.Kick()
	select {
	case <-p.kick:
		t.Fatal("the urgent output went through the queue")
	default:
	}
	p.Kick()
	select {
	case <-p.kick:
	default:
		t.Fatal("the second output should have gone through the queue")
	}
}

// The wall clock goes backwards (NTP when the machine wakes up): the last
// frame then looks dated a few seconds in the future. The next frame must
// not wait longer than the gap imposed by MaxFPS.
func TestPacerSurvivesWallClockJump(t *testing.T) {
	got := make(chan time.Time, 16)
	p := newPacer(60, func() { got <- time.Now() })
	p.FrameDone()
	p.last.Store(time.Now().Add(3 * time.Second).UnixNano())
	start := time.Now()
	p.Kick()
	select {
	case at := <-got:
		if d := at.Sub(start); d > 100*time.Millisecond {
			t.Fatalf("frame delayed by %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame")
	}
}

// WakeAt does schedule a frame at the deadline, and a deadline already
// past triggers one right away.
func TestWakeAt(t *testing.T) {
	got := make(chan time.Time, 16)
	p := newPacer(60, func() { got <- time.Now() })
	for _, d := range []time.Duration{40 * time.Millisecond, -time.Second} {
		start := time.Now()
		p.WakeAt(start.Add(d))
		select {
		case at := <-got:
			if e := at.Sub(start); e < d || e > max(d, 0)+80*time.Millisecond {
				t.Fatalf("deadline %v: frame after %v", d, e)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("deadline %v: no frame", d)
		}
	}
}
