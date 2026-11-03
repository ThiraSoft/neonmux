# Configuration

Every key of the config file, the themes and the backgrounds.

`~/.config/neonmux/config.json` (under `$XDG_CONFIG_HOME` if it is set, or `-config`, or `$NEONMUX_CONFIG`). Missing fields keep their default value. Theme, font, font size, background, typing sparks, window decorations and power mode are written back to it when you change them from the app. Only the changed keys are written, and the others (unknown keys included) stay as they are. Launch options (`-theme`, `-font`, `-size`) only apply to that session. As long as the file has a syntax error, the app writes nothing to it and tells you so. If the file does not exist yet, `mod+,` first writes a complete one with every key and its default value. Next to it, `config.reference.jsonc` lists every key with its default value and a line of help (the themes and backgrounds included). The app rewrites it at each launch, so it follows the installed version: copy keys from it, do not edit it. The `_help` key of `config.json` only points to it.

The config is **reloaded as soon as it is saved**, for example after `mod+,` opens it in your editor. Theme, fonts, background, pane opacity, power, decorations, shortcuts, shell and prewarm apply on the fly, and a toast lists what changed ("config reloaded: theme, font"). Only `persist` waits for the next launch, and the `remote` keys for the next start of the daemon. A syntax error leaves the running config untouched and points to the offending line. An unknown key is reported without blocking the rest. `scrollback` is capped at 15,000 lines so that a terminal's snapshot stays small enough for the daemon to carry. The file is watched through kqueue or inotify (fsnotify): nothing wakes up while it does not change.

```json
{
  "theme": "acid",
  "font": "iosevka",
  "font_size": 16,
  "ui_font": "iosevka",
  "ui_font_size": 16,
  "symbols_font": "auto",
  "shell": "",
  "leader": "ctrl+space",
  "mod": "cmd",
  "option_as_meta": false,
  "power": "balanced",
  "background": "stars",
  "animate_background": true,
  "scrollback": 10000,
  "max_panes": 4,
  "copy_on_select": true,
  "cursor_blink": true,
  "typing_fx": false,
  "battery": true,
  "editor": "",
  "browser": "",
  "decorations": true,
  "focus_border": 1,
  "focus_border_alone": false,
  "focus_border_rest": 50,
  "focus_border_fade": 0.35,
  "pane_opacity": 70,
  "persist": true,
  "restore_commands": ["nvim", "vim", "vi", "less", "man", "htop", "btop", "top", "lazygit", "tig", "claude"],
  "prewarm": true,
  "bindings": { "mod+x": "fx.confetti" },
  "agent_flash": true,
  "agent_flash_count": 2,
  "agent_flash_duration": 0.5,
  "prompter_wpm": 300,
  "prompter_context": 0,
  "prompter_blank_ms": 0,
  "prompter_hold": false,
  "prompter_blink": true,
  "prompter_skip_code": true,
  "prompter_short_paths": true,
  "prompter_strip_markdown": true,
  "prompter_pauses": true,
  "prompter_intermediate": false,
  "remote": false,
  "remote_idle_off": 30,
  "remote_port": 8007,
  "remote_page_port": 8006
}
```

- `font`: the terminal font. `iosevka` (the default) is Iosevka Term, built in. `pixel` is the built-in 6×13 bitmap font, scaled up by whole factors. `gomono` is Go Mono, built in. Anything else is the path of a `.ttf`, `.otf` or `.ttc` file. Characters missing from the font are drawn with the pixel font. A font that cannot be loaded falls back to the pixel font with a toast, and that fallback is never saved. The `mod+o` picker lists the built-in fonts and the monospace fonts installed on the machine.
- `font_size`: size of the terminal font, in points (default 16). `mod+=`, `mod+-` and `mod+0` change it.
- `ui_font`: the interface font (pane headers, bottom bar, palette, toasts, file explorer and audio player). It takes the same values as `font`, `iosevka` by default. The help, the diff and the agent board use the terminal font.
- `ui_font_size`: size of the interface font, in points (default 16).
- `symbols_font`: fallback font for Nerd Font icons (the Unicode private use area). With `auto`, an installed Nerd Font Mono is detected. `none` turns it off, and any other value is the path of a font.
- `shell`: the shell of new terminals. Empty, neonmux uses `$SHELL`, otherwise your login shell.
- `editor`: the command that opens files (explorer, `neonmux open`, diff, `mod+,`, `-demo`). Empty, neonmux uses `$EDITOR`, otherwise nvim, otherwise vi.
- `browser`: the browser behind browser panes, a path or a command with its arguments (`"chromium --lang=fr"`). It must belong to the Chromium family. Empty, neonmux looks for Brave, Chromium, Chrome, Edge and Vivaldi in that order (in `PATH`, and in `/Applications` on macOS), then for the Chromium of Playwright's cache.
- `max_panes`: number of tiles on screen, from 1 to 9 (default 4). Past it, a new pane sends the active tile to the garage.
- `typing_fx`: sparks while typing (default `false`, also from the palette).
- `battery`: the battery level in the bottom bar, with a plug while plugged in and the warning color under 20% (default `true`, also from the palette). It reads IOKit's power sources on macOS and `/sys/class/power_supply` on Linux every 5 seconds in the background, which costs about 60 µs per reading and only redraws the bar when the level or the plug changes. It stays hidden on a machine without a battery.
- `decorations`: `false` removes the macOS title bar and its traffic light buttons (also on the fly from the palette, "Window: macOS decorations on/off"). The window can then no longer be moved with the mouse, which changes nothing if aerospace handles placement.
- `focus_border`: thickness in pixels of the focus border, from 0 to 8 (default 1). At 2 you get the original thick border with its two glows. At 0 the focused pane keeps the normal border. Alone on screen, the focused pane has no focus border, since there is nothing to stand out from. Reloaded on the fly.
- `focus_border_alone`: `true` keeps the focus border even when the pane is alone on screen (default `false`). Reloaded on the fly.
- `focus_border_rest`: intensity of the focus border once it has dimmed, as a percentage (default 50). The border lights up fully on every pane change, then dims down to this intensity. It starts from the normal border, so it always stays brighter than that. At 100 it stays full all the time, which is how you turn the effect off. Reloaded on the fly.
- `focus_border_fade`: how long that dimming takes, in seconds, from 0 to 10 (default 0.35). At 0, the border drops to its resting intensity right away. Reloaded on the fly.
- `pane_opacity`: opacity of the background of terminals and other panes, as a percentage (1 to 100). The lower it is, the more the background shows through behind the text. 70 by default. At 0, each theme keeps its own, between 82 and 96%. Reloaded on the fly.
- `persist`: `false` to go back to terminals tied to the window.
- `option_as_meta` stays `false` by default so that option composes characters (`| { } [ ] ~` on an AZERTY Mac).
- `power`: `eco` (30 fps, still background), `balanced` (60 fps, the background animates as long as the window has the focus, even when you type nothing; shown as AUTO in the bottom bar), `max` (120 fps, always animated).
- `bindings`: your own shortcuts, from a key to an action, on top of the defaults. The help lists them under "Your configuration".
- `agent_flash`: `false` stops the border of an agent that finishes from flashing (default `true`). The toast and the icon in the bottom bar stay. Reloaded on the fly.
- `agent_flash_count`: how many times that border flashes, from 1 to 10 (default 2).
- `agent_flash_duration`: how long each flash lasts, in seconds, from 0.1 to 5 (default 0.5). Raising the count keeps the same rhythm: 4 flashes of 0.5 s last 2 seconds in all.
- `prompter_wpm`: reading speed of the prompter, in words per minute, from 100 to 1000 (default 300). `up` and `down` in the prompter change it by 25 and save it.
- `prompter_context`: words shown on each side of the current one, from 0 to 8 (default 0, the word alone). The window applies a change at once; phones get it from the daemon when it starts.
- `prompter_blank_ms`: an empty anchor at the start of each word, in milliseconds, from 0 to 150 (default 0, off). It keeps two words from blending on the retina; 30 to 60 works well. The blank is taken from the word, so the speed stays the same, and it never takes more than half a word. On a phone it lasts at least two frames.
- `prompter_hold`: `true` reads only while play is held: space in the window, a finger on the words on a phone. Releasing pauses. `enter` and the play button still play and pause.
- `prompter_blink`: `false` stops the track number from blinking while replies wait after the current one (default `true`); it stays in color.
- `prompter_skip_code`: code blocks and tables become a short marker such as `[code · 14 lines]` instead of being read word by word.
- `prompter_short_paths`: a long path is read as its file name, a URL as its host.
- `prompter_strip_markdown`: bold, headings, list bullets, links and backticks are removed, only the text is read.
- `prompter_pauses`: a little longer on periods, commas and long words.
- `prompter_intermediate`: `true` reads every text of an agent's turn as one track, not only its final reply.

The window applies `prompter_context`, `prompter_blank_ms`, `prompter_hold` and `prompter_blink` at once, phones get them from the daemon when it starts. The other `prompter_*` switches except the speed are read by the daemon when it starts: restart it (`neonmux restart-server`) after changing them.
- `remote_idle_off`: minutes without a phone before remote control turns itself off (default `30`, `0` never). The old `"30m"` string form still works.
- `remote`, `remote_idle_off`, `remote_port`, `remote_page_port`: remote control from a phone, see [docs/remote.md](remote.md#configuration).

Available themes: `neon-tokyo`, `synthwave`, `matrix`, `amber`, `ice`, `acid`, `dark-magic`, `blood-moon`, `vaporwave`, `abyss`, `mono-dark`, `mono-light`. **Dark Magic** goes for restraint: a black with a faint violet tint, desaturated hues (amethyst, old gold, sage), over a very faint arcane dust.

Backgrounds (`mod+shift+b` goes to the next one, the palette lists them all):

| Background | What you see |
|---|---|
| `stars` (default) | a drifting star field |
| `grid` | the striped sun and the synthwave grid in perspective (shader) |
| `rain` | katakana rain |
| `dust` | a very faint arcane dust |
| `aurora` | northern lights rippling above mountains and a line of fir trees (shader) |
| `topo` | a living topographic map whose contour lines light up as a sweep passes over them (shader) |
| `lava` | a lava lamp in four shades, as on a limited-palette screen (shader) |
| `fire` | the blaze of 90s demos with its embers, in the theme's colors (a cold flame that turns white on Ice) |
| `life` | the Game of Life on an LED panel, revived by a methuselah when it dies down |
| `swarm` | a murmuration of 300 birds that shimmers when the flock turns, now and then split by a bird of prey |
| `circuit` | a randomly drawn printed circuit board, crossed now and then by flashes jumping from via to via |
| `scope` | an XY oscilloscope with persistent phosphor, gliding from one Lissajous figure to the next |
| `city` | a city at night scrolling in parallax, with its katakana signs and its flying cars |
| `none` | a plain backdrop |

With `auto`, each theme gets its own: `city` for Neon Tokyo, `grid` for Synthwave and Vaporwave, `rain` for Matrix, `scope` for Amber CRT, `aurora` for Ice, `lava` for Acid Rave, `dust` for Dark Magic, `fire` for Blood Moon `swarm` for Abyss, `topo` for Mono Dark and `none` for Mono Light. All backgrounds share the same grain: they are computed at the size of the big pixels and then scaled up without smoothing, and their gradients fall into a few flat colors, as on limited-palette machines. Each one costs between 0.3 and 1 ms per frame at 2560×1600, and shaders are only compiled the first time they are shown.
