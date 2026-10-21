package ui

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	c, warn, err := ParseConfig([]byte(`{"theme": "dark-magic", "typing_fx": true}`))
	if err != nil || warn != "" || c.Theme != "dark-magic" || !c.TypingFX || c.Background != "stars" {
		t.Fatalf("simple config: %+v %q %v", c, warn, err)
	}
	// A typo is reported without blocking the rest.
	c, warn, err = ParseConfig([]byte(`{"theme": "abyss", "themme": "x"}`))
	if err != nil || !strings.Contains(warn, "themme") || c.Theme != "abyss" {
		t.Fatalf("unknown key: %+v %q %v", c, warn, err)
	}
	// Syntax error: the line is reported.
	_, _, err = ParseConfig([]byte("{\n  \"theme\": \"abyss\",\n  \"typing_fx\": tru\n}"))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("syntax error: %v", err)
	}
}

func TestFocusBorder(t *testing.T) {
	if d := DefaultConfig(); d.FocusBorder != 1 {
		t.Fatalf("default %v, want 1", d.FocusBorder)
	}
	c, warn, err := ParseConfig([]byte(`{"focus_border": 3}`))
	if err != nil || warn != "" || c.FocusBorder != 3 {
		t.Fatalf("%+v %q %v", c.FocusBorder, warn, err)
	}
	if c, _, _ := ParseConfig([]byte(`{"focus_border": -2}`)); c.FocusBorder != 0 {
		t.Fatalf("negative: %v", c.FocusBorder)
	}
	if c, _, _ := ParseConfig([]byte(`{"focus_border": 40}`)); c.FocusBorder != 8 {
		t.Fatalf("capped at 8: %v", c.FocusBorder)
	}
}

func TestPaneOpacity(t *testing.T) {
	if a := DefaultConfig().paneAlpha(0.88); a != 0.7 {
		t.Fatalf("default: %v, want 0.7", a)
	}
	c, warn, err := ParseConfig([]byte(`{"pane_opacity": 0}`))
	if err != nil || warn != "" || c.paneAlpha(0.88) != 0.88 {
		t.Fatalf("0: %v %q %v, want the theme's opacity", c.paneAlpha(0.88), warn, err)
	}
	if c, _, _ := ParseConfig([]byte(`{"pane_opacity": 50}`)); c.paneAlpha(0.88) != 0.5 {
		t.Fatalf("50%%: %v", c.paneAlpha(0.88))
	}
	if c, _, _ := ParseConfig([]byte(`{"pane_opacity": 150}`)); c.paneAlpha(0.88) != 1 {
		t.Fatalf("capped at 100: %v", c.paneAlpha(0.88))
	}
	if c, _, _ := ParseConfig([]byte(`{"pane_opacity": -5}`)); c.paneAlpha(0.88) != 0.88 {
		t.Fatalf("negative: %v", c.paneAlpha(0.88))
	}
}

// The focus border layers: a solid line of the chosen thickness, then
// glows. At 2, it looks like the original.
func TestFocusBorderLayers(t *testing.T) {
	if l := focusBorderLayers(0); len(l) != 0 {
		t.Fatalf("0: %+v", l)
	}
	one := focusBorderLayers(1)
	if len(one) != 2 || one[0] != (borderLayer{0, 1, 1}) || one[1].inset != -1 || one[1].width != 1 {
		t.Fatalf("1: %+v", one)
	}
	two := focusBorderLayers(2)
	want := []borderLayer{{0, 2, 1}, {-1, 1, 0.35}, {-2, 1, 0.14}}
	if len(two) != 3 || two[0] != want[0] || two[1] != want[1] || two[2] != want[2] {
		t.Fatalf("2: %+v", two)
	}
}

// The border's resting intensity (in %) and how long it takes to dim (in
// seconds) are bounded.
func TestFocusBorderRest(t *testing.T) {
	if d := DefaultConfig(); d.FocusRest != 50 || d.FocusFade != 0.35 {
		t.Fatalf("defaults %v%% and %vs, want 50%% and 0.35s", d.FocusRest, d.FocusFade)
	}
	c, warn, err := ParseConfig([]byte(`{"focus_border_rest": 50, "focus_border_fade": 0.8}`))
	if err != nil || warn != "" || c.FocusRest != 50 || c.FocusFade != 0.8 {
		t.Fatalf("%v %v %q %v", c.FocusRest, c.FocusFade, warn, err)
	}
	c, _, _ = ParseConfig([]byte(`{"focus_border_rest": 140, "focus_border_fade": 60}`))
	if c.FocusRest != 100 || c.FocusFade != 10 {
		t.Fatalf("capped at 100%% and 10s: %v %v", c.FocusRest, c.FocusFade)
	}
	c, _, _ = ParseConfig([]byte(`{"focus_border_rest": -5, "focus_border_fade": -1}`))
	if c.FocusRest != 0 || c.FocusFade != 0 {
		t.Fatalf("negative: %v %v", c.FocusRest, c.FocusFade)
	}
}

// The flash of a finished agent: on by default, two flashes of half a
// second each. The count and the length of a flash are bounded.
func TestAgentFlashConfig(t *testing.T) {
	if d := DefaultConfig(); !d.AgentFlash || d.AgentFlashCount != 2 || d.AgentFlashDuration != 0.5 {
		t.Fatalf("defaults %v, %d flashes of %vs, want true, 2 of 0.5s", d.AgentFlash, d.AgentFlashCount, d.AgentFlashDuration)
	}
	c, warn, err := ParseConfig([]byte(`{"agent_flash": false, "agent_flash_count": 4, "agent_flash_duration": 0.3}`))
	if err != nil || warn != "" || c.AgentFlash || c.AgentFlashCount != 4 || c.AgentFlashDuration != 0.3 {
		t.Fatalf("%v %d %v %q %v", c.AgentFlash, c.AgentFlashCount, c.AgentFlashDuration, warn, err)
	}
	c, _, _ = ParseConfig([]byte(`{"agent_flash_count": 50, "agent_flash_duration": 60}`))
	if c.AgentFlashCount != 10 || c.AgentFlashDuration != 5 {
		t.Fatalf("capped at 10 flashes of 5s: %d %v", c.AgentFlashCount, c.AgentFlashDuration)
	}
	c, _, _ = ParseConfig([]byte(`{"agent_flash_count": 0, "agent_flash_duration": 0}`))
	if c.AgentFlashCount != 1 || c.AgentFlashDuration != 0.1 {
		t.Fatalf("at least 1 flash of 0.1s: %d %v", c.AgentFlashCount, c.AgentFlashDuration)
	}
}

// The border starts at full intensity, stays bright for a moment, then dims
// down to its resting intensity.
func TestFocusGlow(t *testing.T) {
	if k := focusGlow(30, 1.5, 0); k != 1 {
		t.Errorf("when focus is taken: %v, want 1", k)
	}
	if k := focusGlow(30, 1.5, 150*time.Millisecond); k < 0.95 {
		t.Errorf("after 150ms: %v, want still bright", k)
	}
	prev := float32(1)
	for ms := 100; ms < 1500; ms += 100 {
		k := focusGlow(30, 1.5, time.Duration(ms)*time.Millisecond)
		if k >= prev || k <= 0.3 {
			t.Fatalf("at %dms: %v after %v, want a decrease staying above 0.3", ms, k, prev)
		}
		prev = k
	}
	for _, since := range []time.Duration{1500 * time.Millisecond, time.Minute} {
		if k := focusGlow(30, 1.5, since); math.Abs(float64(k)-0.3) > 1e-6 {
			t.Errorf("after %v: %v, want 0.3", since, k)
		}
	}
	if k := focusGlow(30, 0, 0); math.Abs(float64(k)-0.3) > 1e-6 {
		t.Errorf("without dimming: %v, want 0.3 right away", k)
	}
	if k := focusGlow(100, 1.5, 500*time.Millisecond); k != 1 {
		t.Errorf("at 100%%: %v, the border should stay full", k)
	}
}
