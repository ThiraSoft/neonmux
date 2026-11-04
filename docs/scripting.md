# Driving neonmux from a terminal

Open files, send keys, read screens and trigger effects from a shell or a script.

Every shell started by neonmux gets `NEONMUX_SOCK` (the daemon socket), `NEONMUX_PANE` (the pane key) and `NEONMUX_BIN` (the running binary), and the binary's directory is added to `PATH`:

```sh
neonmux open photo.png song.mp3 ~/projects    # each file in the right kind of pane, a directory in the explorer
neonmux notify "build done"                   # pulsing border and badge on this pane
neonmux fx confetti                           # or glitch
neonmux theme synthwave
neonmux rename "api"                          # renames the current pane (empty: automatic name)
neonmux float -c br neonwave                  # neonwave in a floating pane at the bottom right
neonmux diff                                  # diff of the current repository
neonmux agent fix-login "fix the bug"         # Claude on a new worktree
```

To read or drive a pane from another terminal, a script or an agent (the way `tmux capture-pane` and `send-keys` work):

```sh
neonmux ls                                       # pane keys, state, program, directory
neonmux capture nvim                             # the screen of the pane running nvim, as text
neonmux capture 1f3a9c2b7d4e5f60                 # or by its key; with no argument: the current pane
neonmux send-keys nvim escape ":w" enter         # keys for the pane's program
neonmux send-keys api "text:make test" enter wait:2s ctrl+c
```

You designate the pane by its key (`neonmux ls`) or by the name of its foreground program, if it is unique. In `-no-server` mode, `NEONMUX_SOCK` points to the window (`gui-<pid>.sock`, in the daemon's private directory, which only your account can reach) and you can also give the pane's number (the one in the bottom bar). Keys follow the `-script` syntax: a single character, a name (`enter`, `tab`, `escape`, `backspace`, `up`, `down`, `left`, `right`, `home`, `end`, `pageup`, `pagedown`, `space`, `f1`…), a combination (`ctrl+c`, `alt+b`, `shift+tab`) or `text:…` for text. `wait:500ms` adds a pause. `cmd` shortcuts (`super` on Linux) belong to neonmux and are refused. Capture only reads the visible screen, without the scrollback.

## Driving the window itself

`send-keys` and `capture` talk to a pane's program. To drive neonmux as you would with the keyboard and your eyes (shortcuts, palette, layout, what is on screen), these commands go to the window, which answers them itself. They find the open window on their own, from any shell, on Linux and macOS (`NEONMUX_WINDOW_SOCK` picks one by hand):

```sh
neonmux state                        # the window as JSON: panes, focus, workspace, layout, mode, palette, toasts
neonmux actions                      # every action, its shortcut and its label
neonmux action pane.zoom             # runs an action as its shortcut would
neonmux key mod+enter                # types into the window: shortcuts first, then the focused pane
neonmux key mod+p "text:theme" enter # the palette, a search, a choice
neonmux key ctrl+shift+v             # "mod" is cmd on macOS and alt elsewhere, as in the config
neonmux focus 2                      # the focus to a pane: number, key, name, program or #id
neonmux shot                         # PNG of the window as drawn, prints its path and size
neonmux shot -p 2 /tmp/pane.png      # one pane only
neonmux wait 2 '^bonjour'            # waits until the screen of pane 2 matches (-t 10s, -gone)
```

`key` returns once every key has gone through the window (about 30 ms each), so the next command sees the result. A sequence with an unknown key types nothing at all. In `state`, each pane has its number in the bar (`n`, absent in the garage), a stable `id` (`#3`), its terminal key, kind, title, program, directory, rectangle in window pixels (`rect`: x, y, w, h), grid size and cursor. `shot` reads the frame just drawn, so it shows exactly what you see, overlays included, without a screen capture tool.

A program can also trigger an effect without going through the CLI, with a custom escape sequence: `printf '\e]7777;fx=confetti\a'`. Standard notifications are recognized too (bell, iTerm2's OSC 9 and OSC 777), and so is the OSC 52 clipboard, which is very handy for copying from a remote nvim over SSH. Only the active, visible pane can write to the clipboard: a pane that is hidden, in the garage or behind zen mode is ignored.

An example with Claude Code hooks (`~/.claude/settings.json`), to get the cmux behavior:

```json
{
  "hooks": {
    "Notification": [{ "hooks": [{ "type": "command", "command": "neonmux notify 'Claude is waiting for your answer' 2>/dev/null || true" }] }],
    "Stop": [{ "hooks": [{ "type": "command", "command": "neonmux fx confetti 2>/dev/null || true" }] }]
  }
}
```
