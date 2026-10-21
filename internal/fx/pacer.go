// Package fx holds everything that is drawn freely, outside the terminal
// grid: backgrounds, particles, post-processing, and the scheduler that
// decides when a frame must be produced.
package fx

import (
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// Pacer wakes the Ebiten loop only when it is useful.
//
// Ebiten runs in "VsyncOffMinimum" mode: with no event, the loop sleeps in
// glfw.WaitEvents and the app uses 0% CPU. The Pacer triggers a frame when
// a terminal has received data (Kick) or at the rate requested by the
// active animations (SetFPS), and never goes above MaxFPS.
//
// A keystroke is the exception to the cap: the current frame waits a little
// for the echo (AwaitKick), and if it is late, the first output that follows
// triggers a frame right away (Urgent).
type Pacer struct {
	kick    chan struct{}
	reconf  chan struct{}
	woken   chan struct{} // wakes AwaitKick
	wakeAt  atomic.Int64  // next wakeup, in ns since epoch (0: none)
	fps     atomic.Int32
	maxFPS  atomic.Int32
	last    atomic.Int64 // last frame, in ns since epoch
	kicks   atomic.Uint64
	urgent  atomic.Int64 // deadline of the immediate frame armed by Urgent
	waiting atomic.Bool  // AwaitKick in progress: the next Kick belongs to it
	frame   func()       // triggers a frame (replaced in tests)
}

// epoch is the origin of the instants the Pacer keeps. They are measured
// with time.Since, so on the monotonic clock: a system clock that goes
// backwards (NTP when the machine wakes up) does not freeze the display.
var epoch = time.Now()

func now() int64 { return int64(time.Since(epoch)) }

// urgentWindow bounds the effect of Urgent: a later output is no longer
// the answer to the input and goes back through the normal queue.
const urgentWindow = 100 * time.Millisecond

// NewPacer starts the scheduler.
func NewPacer(maxFPS int) *Pacer { return newPacer(maxFPS, scheduleFrame) }

func newPacer(maxFPS int, frame func()) *Pacer {
	p := &Pacer{kick: make(chan struct{}, 1), reconf: make(chan struct{}, 1), woken: make(chan struct{}, 1), frame: frame}
	p.maxFPS.Store(int32(maxFPS))
	go p.run()
	return p
}

// Enable switches Ebiten to on-demand rendering. Along with scheduleFrame,
// it is the only place that touches this deprecated API: if it goes away,
// this is where it gets replaced.
//
// Ebiten offers SetScreenClearedEveryFrame(false) instead, but the loop
// then keeps calling Update and Draw sixty times per second while idle. The
// VsyncOffMinimum mode is the only one that lets the app sleep as long as
// nothing asks for a frame.
//
// Despite its documentation, Ebiten keeps 60 ticks per second in this mode:
// a frame too close to the previous one did not call Update and redrew the
// old state. With SyncWithFPS, every frame gets its Update.
func Enable() {
	//lint:ignore SA1019 only mode with no Update or Draw while idle (see above)
	ebiten.SetFPSMode(ebiten.FPSModeVsyncOffMinimum)
	ebiten.SetTPS(ebiten.SyncWithFPS)
}

// Kick requests a frame as soon as possible (non-blocking, concurrent-safe).
func (p *Pacer) Kick() {
	p.kicks.Add(1)
	if p.waiting.Load() {
		// The current frame is waiting for exactly this output: it will show it.
		select {
		case p.woken <- struct{}{}:
		default:
		}
		return
	}
	if at := p.urgent.Load(); at != 0 && now() < at && p.urgent.CompareAndSwap(at, 0) {
		p.schedule()
		return
	}
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Urgent signals an input: the first output that follows, within
// 100 ms, triggers a frame without waiting for the gap imposed by MaxFPS.
// At most one extra frame per input.
func (p *Pacer) Urgent() {
	p.urgent.Store(now() + int64(urgentWindow))
}

// Kicks returns the number of Kicks received since startup.
func (p *Pacer) Kicks() uint64 { return p.kicks.Load() }

// AwaitKick waits at most d for a Kick to come after the count after, read
// earlier from Kicks. It returns true if one does. Called right after a
// keystroke is sent, it lets the echo into the current frame: macOS does
// not present two frames in the same refresh, and an extra frame for the
// echo would push it back to the next one.
func (p *Pacer) AwaitKick(after uint64, d time.Duration) bool {
	p.waiting.Store(true)
	defer p.waiting.Store(false)
	t := time.NewTimer(d)
	defer t.Stop()
	for p.kicks.Load() == after {
		select {
		case <-p.woken:
		case <-t.C:
			return p.kicks.Load() != after
		}
	}
	return true
}

// SetFPS sets the continuous rate wanted by the animations (0 = none).
func (p *Pacer) SetFPS(n int) {
	n = min(n, int(p.maxFPS.Load()))
	if p.fps.Swap(int32(n)) != int32(n) && n > 0 {
		p.Kick()
	}
}

// WakeAt schedules a frame at time t (cursor blink, toast expiry, clock
// minute). Only the nearest one counts.
func (p *Pacer) WakeAt(t time.Time) {
	if t.IsZero() {
		return
	}
	// An instant already past means "right now"; 0 is reserved for
	// "no deadline".
	at := max(int64(t.Sub(epoch)), 1)
	if p.wakeAt.Swap(at) != at {
		select {
		case p.reconf <- struct{}{}:
		default:
		}
	}
}

// FPS returns the current continuous rate.
func (p *Pacer) FPS() int { return int(p.fps.Load()) }

// SetMaxFPS changes the cap (eco / max mode).
func (p *Pacer) SetMaxFPS(n int) { p.maxFPS.Store(int32(max(1, n))) }

// FrameDone is called on every Update.
func (p *Pacer) FrameDone() { p.last.Store(now()) }

func (p *Pacer) run() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		wait := time.Duration(-1)
		if fps := p.fps.Load(); fps > 0 {
			wait = time.Second / time.Duration(fps)
		}
		if at := p.wakeAt.Load(); at > 0 {
			d := max(time.Duration(at-now()), 0)
			if wait < 0 || d < wait {
				wait = d
			}
		}
		var tc <-chan time.Time
		if wait >= 0 {
			timer.Reset(wait)
			tc = timer.C
		}
		select {
		case <-p.kick:
		case <-p.reconf:
			timer.Stop()
			continue
		case <-tc:
			if at := p.wakeAt.Load(); at > 0 && now() >= at {
				p.wakeAt.CompareAndSwap(at, 0)
			}
		}
		timer.Stop()
		gap := time.Second / time.Duration(max(1, p.maxFPS.Load()))
		// The wait stays bounded by gap, whatever last holds.
		if since := time.Duration(now() - p.last.Load()); since < gap {
			time.Sleep(min(gap-since, gap))
		}
		p.schedule()
	}
}

func (p *Pacer) schedule() {
	if p.frame != nil {
		p.frame()
		return
	}
	scheduleFrame()
}

// scheduleFrame wakes the Ebiten loop for one frame.
func scheduleFrame() {
	//lint:ignore SA1019 counterpart of SetFPSMode, see Enable
	ebiten.ScheduleFrame()
}
