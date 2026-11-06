// Package sfx is a tiny 8-bit synth: square, triangle and noise voices,
// a few notes each, rendered once and handed to the UI's audio output.
// The games and the window call Play; with no output set (tests,
// screenshots) nothing happens.
package sfx

import (
	"encoding/binary"
	"math"
	"sync"
	"time"
)

// Rate is the sample rate of the rendered sounds, the one of the UI.
const Rate = 44100

// Sound names a sound effect.
type Sound int

const (
	// Window, kept discreet.
	PaneOpen Sound = iota
	PaneClose
	Focus
	Move
	Workspace
	Toggle
	// Games.
	Jump
	Shoot
	BigShot
	Hit
	Boom
	Hurt
	Pickup
	Die
	Win
	Bounce
	Score
	Key
	Miss
	nSounds
)

type wave int

const (
	square wave = iota
	pulse25
	triangle
	noise
)

// note is one tone: from f0 to f1 Hz over dur seconds.
type note struct {
	w      wave
	f0, f1 float64
	dur    float64
}

// The window's sounds are soft and short, the games' a bit louder.
const (
	uiVol   = 0.07
	gameVol = 0.13
)

type def struct {
	vol   float64
	notes []note
}

var defs = [nSounds]def{
	PaneOpen:  {uiVol, []note{{triangle, 660, 660, 0.04}, {triangle, 990, 990, 0.06}}},
	PaneClose: {uiVol, []note{{triangle, 880, 880, 0.04}, {triangle, 587, 587, 0.06}}},
	Focus:     {uiVol * 0.7, []note{{triangle, 1320, 1320, 0.025}}},
	Move:      {uiVol, []note{{triangle, 740, 880, 0.05}}},
	Workspace: {uiVol, []note{{triangle, 523, 523, 0.035}, {triangle, 784, 784, 0.05}}},
	Toggle:    {uiVol, []note{{pulse25, 1046, 1046, 0.03}, {pulse25, 1568, 1568, 0.03}}},

	Jump:    {gameVol, []note{{square, 300, 700, 0.09}}},
	Shoot:   {gameVol * 0.7, []note{{pulse25, 1400, 700, 0.05}}},
	BigShot: {gameVol, []note{{square, 900, 200, 0.16}}},
	Hit:     {gameVol, []note{{noise, 2000, 2000, 0.04}, {square, 400, 200, 0.04}}},
	Boom:    {gameVol, []note{{noise, 800, 100, 0.28}}},
	Hurt:    {gameVol, []note{{square, 500, 150, 0.14}}},
	Pickup:  {gameVol, []note{{pulse25, 988, 988, 0.05}, {pulse25, 1319, 1319, 0.05}, {pulse25, 1976, 1976, 0.08}}},
	Die:     {gameVol, []note{{square, 660, 330, 0.12}, {square, 330, 165, 0.12}, {triangle, 165, 60, 0.25}}},
	Win: {gameVol, []note{{pulse25, 523, 523, 0.08}, {pulse25, 659, 659, 0.08}, {pulse25, 784, 784, 0.08},
		{pulse25, 1046, 1046, 0.22}}},
	Bounce: {gameVol * 0.8, []note{{triangle, 520, 520, 0.04}}},
	Score:  {gameVol, []note{{pulse25, 1175, 1175, 0.04}, {pulse25, 1760, 1760, 0.07}}},
	Key:    {uiVol * 0.6, []note{{noise, 6000, 6000, 0.012}}},
	Miss:   {gameVol * 0.7, []note{{square, 180, 140, 0.07}}},
}

var (
	mu       sync.Mutex
	out      func(pcm []byte)
	on       = true
	rendered [nSounds][]byte
	last     [nSounds]time.Time
)

// SetOutput gives the function that plays 16-bit stereo PCM at Rate.
func SetOutput(f func(pcm []byte)) {
	mu.Lock()
	out = f
	mu.Unlock()
}

// SetOn turns the sound on or off.
func SetOn(v bool) {
	mu.Lock()
	on = v
	mu.Unlock()
}

// On reports whether the sound is on.
func On() bool {
	mu.Lock()
	defer mu.Unlock()
	return on
}

// Play plays s. The same sound twice within 30 ms plays once: a burst of
// events stays a single blip.
func Play(s Sound) {
	if s < 0 || s >= nSounds {
		return
	}
	mu.Lock()
	f := out
	if f == nil || !on || time.Since(last[s]) < 30*time.Millisecond {
		mu.Unlock()
		return
	}
	last[s] = time.Now()
	if rendered[s] == nil {
		rendered[s] = render(defs[s])
	}
	pcm := rendered[s]
	mu.Unlock()
	f(pcm)
}

// render turns a sound into PCM, with a short attack and a decay on each
// note so nothing clicks.
func render(d def) []byte {
	n := 0
	for _, nt := range d.notes {
		n += int(nt.dur * Rate)
	}
	buf := make([]byte, 0, n*4)
	var phase float64
	lfsr := uint16(0xACE1)
	nz := 0.0
	for _, nt := range d.notes {
		m := int(nt.dur * Rate)
		for i := range m {
			t := float64(i) / float64(m)
			f := nt.f0 * math.Pow(nt.f1/nt.f0, t)
			prev := phase
			phase += f / Rate
			phase -= math.Floor(phase)
			var v float64
			switch nt.w {
			case square:
				v = sign(phase < 0.5)
			case pulse25:
				v = sign(phase < 0.25)
			case triangle:
				v = 4*math.Abs(phase-0.5) - 1
			case noise:
				if phase < prev { // one new random value per period
					bit := (lfsr ^ lfsr>>1) & 1
					lfsr = lfsr>>1 | bit<<14
					nz = float64(lfsr&1)*2 - 1
				}
				v = nz
			}
			env := min(1, float64(i)/(0.002*Rate)) * (1 - t*0.8)
			if m-i < 176 {
				env *= float64(m-i) / (0.004 * Rate)
			}
			x := int16(v * env * d.vol * math.MaxInt16)
			buf = binary.LittleEndian.AppendUint16(buf, uint16(x))
			buf = binary.LittleEndian.AppendUint16(buf, uint16(x))
		}
	}
	return buf
}

func sign(b bool) float64 {
	if b {
		return 1
	}
	return -1
}
