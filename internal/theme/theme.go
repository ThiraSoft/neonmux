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

	Mono bool // the 256-color cube turns to grays too

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
		BG: hex("#020a04"), FG: hex("#d2e4d6"), Cursor: hex("#00ff66"), Selection: hex("#0c3d1c"),
		ANSI: hexes("#06140a", "#3dff7a", "#00ff66", "#b8ff5c", "#1fd18a", "#5affb4", "#00e5a0", "#b4c6b8",
			"#1d4a2a", "#6dff9b", "#5cff99", "#d6ff8f", "#4ff0b0", "#8cffc9", "#55ffc8", "#eef6f0"),
		Accent: hex("#00ff66"), Accent2: hex("#b8ff5c"), Warn: hex("#e6ff00"), Dim: hex("#2d6b40"),
		Border: hex("#0f3a1d"), BarBG: hex("#010702"), BarFG: hex("#7dffa0"),
		PaneAlpha: 0.86, Background: "rain",
	},
	{
		Name: "amber", Label: "Amber CRT",
		BG: hex("#110b02"), FG: hex("#eadfcc"), Cursor: hex("#ffb000"), Selection: hex("#4a3008"),
		ANSI: hexes("#1f1504", "#ff7b3a", "#ffc85a", "#ffe08a", "#d99a2b", "#ff9f45", "#ffd27a", "#cfc4b2",
			"#4d3610", "#ff9a63", "#ffd88a", "#fff0b8", "#f0b450", "#ffbb75", "#ffe4a8", "#f6efe3"),
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
	{
		// Monochrome: grays only, ANSI hues told apart by brightness.
		Name: "mono-dark", Label: "Mono Dark", Mono: true,
		BG: hex("#0e0e0e"), FG: hex("#d4d4d4"), Cursor: hex("#f2f2f2"), Selection: hex("#3a3a3a"),
		ANSI: hexes("#1e1e1e", "#9a9a9a", "#c4c4c4", "#e0e0e0", "#7c7c7c", "#acacac", "#b8b8b8", "#d4d4d4",
			"#4a4a4a", "#b4b4b4", "#dadada", "#f0f0f0", "#969696", "#c6c6c6", "#d0d0d0", "#ffffff"),
		Accent: hex("#f2f2f2"), Accent2: hex("#9a9a9a"), Warn: hex("#ffffff"), Dim: hex("#5c5c5c"),
		Border: hex("#262626"), BarBG: hex("#080808"), BarFG: hex("#bdbdbd"),
		PaneAlpha: 0.95, Background: "topo",
	},
	{
		Name: "mono-light", Label: "Mono Light", Mono: true,
		BG: hex("#f4f4f4"), FG: hex("#1c1c1c"), Cursor: hex("#111111"), Selection: hex("#cfcfcf"),
		ANSI: hexes("#1c1c1c", "#5a5a5a", "#3a3a3a", "#6e6e6e", "#4a4a4a", "#626262", "#545454", "#8a8a8a",
			"#7a7a7a", "#4a4a4a", "#2a2a2a", "#5e5e5e", "#3c3c3c", "#525252", "#464646", "#9a9a9a"),
		Accent: hex("#111111"), Accent2: hex("#5e5e5e"), Warn: hex("#000000"), Dim: hex("#8c8c8c"),
		Border: hex("#d2d2d2"), BarBG: hex("#e6e6e6"), BarFG: hex("#2e2e2e"),
		PaneAlpha: 1, Background: "none",
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
		r, g, b := levels[i/36], levels[(i/6)%6], levels[i%6]
		if t.Mono {
			y := uint8((299*int(r) + 587*int(g) + 114*int(b)) / 1000)
			r, g, b = y, y, y
		}
		t.palette[16+i] = color.RGBA{r, g, b, 255}
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
