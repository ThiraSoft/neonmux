package sfx

import "testing"

func TestEverySoundRenders(t *testing.T) {
	for s := range nSounds {
		pcm := render(defs[s])
		if len(pcm) == 0 || len(pcm)%4 != 0 {
			t.Errorf("sound %d: %d bytes", s, len(pcm))
		}
	}
}

func TestPlayRespectsOff(t *testing.T) {
	n := 0
	SetOutput(func([]byte) { n++ })
	defer SetOutput(nil)
	SetOn(false)
	Play(Jump)
	SetOn(true)
	Play(Jump)
	Play(Jump) // within 30 ms: dropped
	if n != 1 {
		t.Fatalf("%d plays, want 1", n)
	}
}
