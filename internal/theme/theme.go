// Package theme defines the color palettes and the visual mood.
package theme

import (
	"image/color"
	"strconv"
)

// Theme describes a complete mood: terminal colors, UI colors and effect
// settings.
type Theme struct {
	Name  string
	Label string

	BG, FG, Cursor, Selection color.RGBA
	ANSI                      [16]color.RGBA

	Accent, Accent2, Warn, Dim, Border, BarBG, BarFG color.RGBA

	PaneAlpha  float32 // opacity of the pane background: lets the background show through
	Background string  // background used in auto mode (fx.Backgrounds)

	palette [256]color.RGBA
}

func hex(s string) color.RGBA {
	v, _ := strconv.ParseUint(s[1:], 16, 32)
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}
}

func hexes(s ...string) [16]color.RGBA {
	var out [16]color.RGBA
	for i := range out {
		out[i] = hex(s[i])
	}
	return out
}

// Default is the theme used when none is set or the name is unknown.
const Default = "acid"

// All lists the available themes, in cycling order.
var All = []*Theme{
	{
		Name: "neon-tokyo", Label: "Neon Tokyo",
		BG: hex("#0b0a1a"), FG: hex("#d8dcff"), Cursor: hex("#ff2e88"), Selection: hex("#3b2a6b"),
		ANSI: hexes("#1b1832", "#ff3b6b", "#3dffa8", "#ffd166", "#4d8bff", "#c77dff", "#2de2e6", "#c8ccf0",
			"#4a4570", "#ff6b8f", "#7dffc4", "#ffe08a", "#82adff", "#dba8ff", "#7ff3f5", "#ffffff"),
		Accent: hex("#ff2e88"), Accent2: hex("#00f0ff"), Warn: hex("#ffd400"), Dim: hex("#5a5585"),
		Border: hex("#2a2550"), BarBG: hex("#120f28"), BarFG: hex("#b9bdf0"),
		PaneAlpha: 0.88, Background: "city",
	},
	{
		Name: "synthwave", Label: "Synthwave '84",
		BG: hex("#1a1029"), FG: hex("#f4e9ff"), Cursor: hex("#36f9f6"), Selection: hex("#4d2a6e"),
		ANSI: hexes("#2a1f3d", "#fe4450", "#72f1b8", "#fede5d", "#6d77ff", "#ff7edb", "#36f9f6", "#e6dcf5",
			"#57476f", "#ff6e7a", "#9ff6cf", "#fff09a", "#9aa1ff", "#ffa6e9", "#8dfbf9", "#ffffff"),
		Accent: hex("#ff7edb"), Accent2: hex("#36f9f6"), Warn: hex("#fede5d"), Dim: hex("#6b5a86"),
		Border: hex("#3a2753"), BarBG: hex("#150c22"), BarFG: hex("#e0d0f5"),
		PaneAlpha: 0.84, Background: "grid",
	},
	{
		Name: "matrix", Label: "Matrix",
		BG: hex("#020a04"), FG: hex("#9dffb0"), Cursor: hex("#00ff66"), Selection: hex("#0c3d1c"),
		ANSI: hexes("#06140a", "#3dff7a", "#00ff66", "#b8ff5c", "#1fd18a", "#5affb4", "#00e5a0", "#a8f5bc",
			"#1d4a2a", "#6dff9b", "#5cff99", "#d6ff8f", "#4ff0b0", "#8cffc9", "#55ffc8", "#e6ffe9"),
		Accent: hex("#00ff66"), Accent2: hex("#b8ff5c"), Warn: hex("#e6ff00"), Dim: hex("#2d6b40"),
		Border: hex("#0f3a1d"), BarBG: hex("#010702"), BarFG: hex("#7dffa0"),
		PaneAlpha: 0.86, Background: "rain",
	},
	{
		Name: "amber", Label: "Amber CRT",
		BG: hex("#110b02"), FG: hex("#ffc85a"), Cursor: hex("#ffb000"), Selection: hex("#4a3008"),
		ANSI: hexes("#1f1504", "#ff7b3a", "#ffc85a", "#ffe08a", "#d99a2b", "#ff9f45", "#ffd27a", "#ffdca0",
			"#4d3610", "#ff9a63", "#ffd88a", "#fff0b8", "#f0b450", "#ffbb75", "#ffe4a8", "#fff6e0"),
		Accent: hex("#ffb000"), Accent2: hex("#ff7b3a"), Warn: hex("#ffe08a"), Dim: hex("#7a5520"),
		Border: hex("#3a2708"), BarBG: hex("#0b0701"), BarFG: hex("#ffc85a"),
		PaneAlpha: 0.94, Background: "scope",
	},
	{
		Name: "ice", Label: "Ice Protocol",
		BG: hex("#0d1220"), FG: hex("#c9d6ea"), Cursor: hex("#7aa2f7"), Selection: hex("#24324f"),
		ANSI: hexes("#1a2236", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b6cf",
			"#3b4766", "#ff9bab", "#b9f27c", "#f2c88a", "#9cbcff", "#d0b6ff", "#a6deff", "#e4ecfa"),
		Accent: hex("#7dcfff"), Accent2: hex("#bb9af7"), Warn: hex("#e0af68"), Dim: hex("#4e5c7d"),
		Border: hex("#1f2a44"), BarBG: hex("#090d18"), BarFG: hex("#a9b6cf"),
		PaneAlpha: 0.9, Background: "aurora",
	},
	{
		Name: "acid", Label: "Acid Rave",
		BG: hex("#0a0a0c"), FG: hex("#e8e8f0"), Cursor: hex("#c6ff00"), Selection: hex("#3a1f5c"),
		ANSI: hexes("#18181e", "#ff3d71", "#c6ff00", "#ffea00", "#7c4dff", "#e040fb", "#00e5ff", "#cfcfdb",
			"#3c3c4a", "#ff7096", "#dcff5c", "#fff35c", "#a383ff", "#ec7dfd", "#5cefff", "#ffffff"),
		Accent: hex("#c6ff00"), Accent2: hex("#e040fb"), Warn: hex("#ffea00"), Dim: hex("#55556a"),
		Border: hex("#24242e"), BarBG: hex("#060608"), BarFG: hex("#cfcfdb"),
		PaneAlpha: 0.9, Background: "lava",
	},
	{
		// Sober and elegant: a barely purple black, desaturated hues.
		Name: "dark-magic", Label: "Dark Magic",
		BG: hex("#0b0a0f"), FG: hex("#c9c5d6"), Cursor: hex("#c9a96e"), Selection: hex("#2a2440"),
		ANSI: hexes("#16141d", "#c4746e", "#8fae8b", "#c9a96e", "#7d8fc9", "#a68bd6", "#7fb2b5", "#bdb9c9",
			"#3b3748", "#d68c86", "#a5c4a1", "#dcc08a", "#97a8dc", "#bba4e6", "#98c9cc", "#e8e5f0"),
		Accent: hex("#a68bd6"), Accent2: hex("#c9a96e"), Warn: hex("#d68c86"), Dim: hex("#5a556b"),
		Border: hex("#1f1c28"), BarBG: hex("#08070b"), BarFG: hex("#a8a3b8"),
		PaneAlpha: 0.96, Background: "dust",
	},
	{
		Name: "blood-moon", Label: "Blood Moon",
		BG: hex("#0d0507"), FG: hex("#ead8d8"), Cursor: hex("#ff2a3d"), Selection: hex("#4a1219"),
		ANSI: hexes("#1c0c0f", "#ff2a3d", "#7ee08a", "#ffb347", "#a066ff", "#ff4d8d", "#ff8a6b", "#d8c4c4",
			"#4d2a2f", "#ff5c6c", "#a3f0ab", "#ffcb7d", "#bc94ff", "#ff7fab", "#ffab94", "#fff0f0"),
		Accent: hex("#ff2a3d"), Accent2: hex("#ff8a3d"), Warn: hex("#ffcb47"), Dim: hex("#6b3a40"),
		Border: hex("#2e1216"), BarBG: hex("#080304"), BarFG: hex("#d8b4b8"),
		PaneAlpha: 0.88, Background: "fire",
	},
	{
		Name: "vaporwave", Label: "Vaporwave",
		BG: hex("#1a1033"), FG: hex("#f2eaff"), Cursor: hex("#01cdfe"), Selection: hex("#45307a"),
		ANSI: hexes("#2b1d4f", "#ff6b9d", "#05ffa1", "#fffb96", "#01cdfe", "#ff71ce", "#94fffa", "#e8dcff",
			"#5c4a8a", "#ff94b8", "#7affc9", "#fffcc0", "#67e1ff", "#ff9fe0", "#c2fffc", "#ffffff"),
		Accent: hex("#ff71ce"), Accent2: hex("#01cdfe"), Warn: hex("#fffb96"), Dim: hex("#7a68a8"),
		Border: hex("#35255e"), BarBG: hex("#120a26"), BarFG: hex("#e0d4ff"),
		PaneAlpha: 0.82, Background: "grid",
	},
	{
		Name: "abyss", Label: "Abyss",
		BG: hex("#040a12"), FG: hex("#b9d6e8"), Cursor: hex("#00e5ff"), Selection: hex("#0f3048"),
		ANSI: hexes("#0a1824", "#ff6b81", "#00ffc8", "#ffd56b", "#2d8cff", "#a78bfa", "#00e5ff", "#a9c7d9",
			"#26425a", "#ff91a2", "#5cffd9", "#ffe39a", "#66adff", "#c4b1ff", "#66f0ff", "#e6f5ff"),
		Accent: hex("#00e5ff"), Accent2: hex("#00ffc8"), Warn: hex("#ffd56b"), Dim: hex("#3a5a72"),
		Border: hex("#0c2233"), BarBG: hex("#02060b"), BarFG: hex("#9cc0d6"),
		PaneAlpha: 0.9, Background: "swarm",
	},
}

func init() {
	for _, t := range All {
		t.buildPalette()
	}
}

// Get returns a theme by name, or the default one.
func Get(name string) *Theme {
	for _, t := range All {
		if t.Name == name {
			return t
		}
	}
	if name != Default {
		return Get(Default)
	}
	return All[0]
}

// Next returns the next theme (or the previous one if delta < 0).
func Next(cur *Theme, delta int) *Theme {
	for i, t := range All {
		if t == cur {
			return All[(i+delta+len(All))%len(All)]
		}
	}
	return All[0]
}

// buildPalette computes the 256 xterm colors: 16 from the theme, the
// 6×6×6 cube, then the gray ramp.
func (t *Theme) buildPalette() {
	copy(t.palette[:16], t.ANSI[:])
	levels := [6]uint8{0, 95, 135, 175, 215, 255}
	for i := 0; i < 216; i++ {
		t.palette[16+i] = color.RGBA{levels[i/36], levels[(i/6)%6], levels[i%6], 255}
	}
	for i := 0; i < 24; i++ {
		v := uint8(8 + i*10)
		t.palette[232+i] = color.RGBA{v, v, v, 255}
	}
}

// Palette returns the color at index i.
func (t *Theme) Palette(i uint8) color.RGBA { return t.palette[i] }

// RGB returns a color as a triplet (OSC queries).
func RGB(c color.RGBA) [3]uint8 { return [3]uint8{c.R, c.G, c.B} }

// Mix linearly interpolates two colors.
func Mix(a, b color.RGBA, t float32) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(float32(x) + (float32(y)-float32(x))*t) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), l(a.A, b.A)}
}
