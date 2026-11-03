package ui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ThiraSoft/neonmux/internal/fx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// The reference file: every key of the config with its default value and a
// line of help, next to config.json. JSON has no comments and the app
// rewrites config.json, so the help lives in a file of its own, rewritten
// at each launch to follow the installed version.

const configRefName = "config.reference.jsonc"

// configHelpKey is the key of config.json that points to the reference.
const configHelpKey = "_help"

// configHelp explains each key, in the order of Config. A test checks that
// no key is missing.
var configHelp = map[string]string{
	"theme":              "color theme: THEMES",
	"font":               "terminal font: iosevka, pixel, gomono or the path of a .ttf, .otf or .ttc",
	"font_size":          "terminal font size, in points (mod+= mod+- mod+0)",
	"ui_font":            "interface font (headers, bars, palette), same values as font",
	"ui_font_size":       "interface font size, in points",
	"symbols_font":       "Nerd Font icons: auto (an installed Nerd Font Mono), none or the path of a font",
	"shell":              "shell of new terminals (empty: $SHELL, else the login shell)",
	"leader":             "leader key: every shortcut also exists behind it, then the key alone",
	"mod":                "modifier of the shortcuts: alt, cmd, ctrl, ctrl+shift…",
	"option_as_meta":     "macOS: option sends meta instead of composing characters",
	"power":              "eco (30 fps, still background), balanced (60 fps), max (120 fps, always animated)",
	"background":         "background: auto (the theme's) or one of BACKGROUNDS",
	"animate_background": "animate the background",
	"scrollback":         "lines kept above the screen in each terminal, 15000 at most",
	"max_panes":          "tiles on screen, 1 to 9; past it a new pane sends the active tile to the garage",
	"copy_on_select":     "selecting with the mouse copies",
	"cursor_blink":       "the cursor blinks",
	"typing_fx":          "sparks while typing",
	"battery":            "battery level in the bottom bar (hidden without a battery)",
	"editor":             "command that opens files (empty: $EDITOR, else nvim, else vi)",
	"browser":            "Chromium-family browser of browser panes, a path or a command (empty: found by itself)",
	"decorations":        "macOS title bar",
	"focus_border":       "focus border thickness, in pixels, 0 to 8",
	"focus_border_alone": "keep the focus border when the pane is alone on screen",
	"focus_border_rest":  "focus border intensity once dimmed, in %, 100 to keep it full",
	"focus_border_fade":  "how long that dimming takes, in seconds, 0 to 10",
	"pane_opacity":       "pane background opacity, in %, 0 for the theme's own",
	"persist":            "terminals kept by the daemon, restored after a restart (next launch)",
	"restore_commands":   "programs relaunched in their terminal after a reboot, e.g. [\"nvim\", \"claude\"]",
	"prewarm":            "keep a shell ready for the next terminal",
	"bindings":           "your shortcuts, key to action, e.g. {\"mod+x\": \"fx.confetti\"}; the help lists the actions",

	"agent_flash":          "the border of an agent that finishes flashes",
	"agent_flash_count":    "how many flashes, 1 to 10",
	"agent_flash_duration": "each flash, in seconds, 0.1 to 5",

	"remote":           "remote control from a phone (read when the daemon starts)",
	"remote_idle_off":  "minutes without a phone before remote control turns off, 0 never",
	"remote_port":      "UDP port of remote control (WebTransport)",
	"remote_page_port": "local port of the page tailscale serve publishes",

	"prompter_wpm":            "prompter speed, words per minute, 100 to 1000 (up and down in the prompter)",
	"prompter_context":        "words shown on each side of the current one, 0 to 8",
	"prompter_blank_ms":       "empty anchor at the start of each word, in ms, 0 to 150 (0: off, 40 works well)",
	"prompter_hold":           "read only while space (a finger on the words on a phone) is held",
	"prompter_blink":          "the track number blinks while replies wait after the current one",
	"prompter_skip_code":      "code blocks and tables become a short marker (daemon start)",
	"prompter_short_paths":    "a long path is read as its file name, a URL as its host (daemon start)",
	"prompter_strip_markdown": "markdown marks are removed, only the text is read (daemon start)",
	"prompter_pauses":         "a little longer on punctuation and long words (daemon start)",
	"prompter_intermediate":   "read every text of an agent's turn, not only its final reply (daemon start)",
}

// configReference returns the content of the reference file.
func configReference() []byte {
	var themes, bgs []string
	for _, t := range theme.All {
		themes = append(themes, t.Name)
	}
	bgs = append(bgs, fx.Backgrounds...)
	var b bytes.Buffer
	b.WriteString("// Every key of config.json, with its default value. This file is rewritten\n")
	b.WriteString("// at each launch: copy the keys you want into config.json, not here.\n")
	b.WriteString("// More in docs/configuration.md.\n{\n")
	fields := fieldsOf(DefaultConfig())
	fields = slices.DeleteFunc(fields, func(f configField) bool { return f.key == configHelpKey })
	for i, f := range fields {
		help := configHelp[f.key]
		help = strings.ReplaceAll(help, "THEMES", strings.Join(themes, ", "))
		help = strings.ReplaceAll(help, "BACKGROUNDS", strings.Join(bgs, ", "))
		b.WriteString("  // " + help + "\n")
		k, _ := json.Marshal(f.key)
		b.WriteString("  ")
		b.Write(k)
		b.WriteString(": ")
		if json.Indent(&b, f.val, "  ", "  ") != nil {
			b.Write(f.val)
		}
		if i < len(fields)-1 {
			b.WriteByte(',')
		}
		b.WriteString("\n")
		if i < len(fields)-1 && configGroupEnd(f.key) {
			b.WriteString("\n")
		}
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// configGroupEnd marks the last key of a group, followed by a blank line.
func configGroupEnd(key string) bool {
	switch key {
	case "bindings", "agent_flash_duration", "remote_page_port":
		return true
	}
	return false
}

// WriteConfigReference puts the reference next to the config, if its
// content changed. Errors are ignored: it is only help.
func WriteConfigReference() {
	p := filepath.Join(filepath.Dir(configTarget(ConfigPath())), configRefName)
	data := configReference()
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
		return
	}
	_ = writeConfigFile(p, data)
}
