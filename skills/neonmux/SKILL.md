---
name: neonmux
description: Use when the user asks to do something in neonmux (open, arrange, look at, type into or test something in the window or its panes), when a change to neonmux must be checked in the real app, or when you need another terminal to run and watch a program. Covers the CLI that drives the window (state, key, action, focus, shot, wait) and the panes (capture, send-keys), on Linux and macOS.
---

# Driving neonmux

neonmux is a terminal multiplexer with its own window. When you run inside one of its panes, `NEONMUX_SOCK`, `NEONMUX_PANE` and `NEONMUX_BIN` are set. Everything goes through the `neonmux` CLI, the same way on Linux and macOS: no system screenshot tool, no xdotool, no AppleScript.

If `neonmux state` answers "unknown command", the installed binary predates these commands: update neonmux, then restart its window (`neonmux restart` keeps the running programs).

## Look

```sh
neonmux state | jq .                 # the whole window as JSON
neonmux state | jq -r '.panes[] | [.n, .id, .program, .dir, .focused] | @tsv'
neonmux capture 2                    # text of pane 2's screen
neonmux shot                         # PNG of the window as drawn, prints its path and size
neonmux shot -p 2 /tmp/p.png         # one pane only
```

Open the PNG to check a rendering. In `state`, each pane has `n` (its number in the bar, absent in the garage), `id` (`#3`, stable), `key` (terminals), `kind` (term, files, image, audio, browser…), `title`, `program`, `dir`, `rect` [x, y, w, h] in window pixels, `cells`, `cursor`, `focused`, `floating`, `garage`, `workspace`, `mode` (COPY…). At the top level: `workspace`, `layout`, `zoomed`, `mode` (leader, resize), `palette` (title, query, selection, items), `explorer`, `toasts`, `mod`, `theme`.

## Act

```sh
neonmux key mod+enter                # new terminal; "mod" is alt on Linux, cmd on macOS
neonmux key "text:ls -la" enter      # types into the focused pane
neonmux key mod+p "text:zoom" enter  # the palette
neonmux key ctrl+space "["           # leader, then a key
neonmux action pane.zoom             # an action without its shortcut
neonmux actions                      # every action, its shortcut, its label
neonmux focus 2                      # number, key, name, program or #id; out of the garage, across workspaces
neonmux send-keys 2 "text:make" enter  # straight to the pane's program, neonmux shortcuts bypassed
neonmux open file.png ~/dir          # each file in the right kind of pane
neonmux float -c br htop             # a program in a floating pane
```

`key` goes through the window like the keyboard: shortcuts first, then the focused pane. It returns once every key is typed (about 30 ms each), so the next command sees the result. One invalid token fails the whole sequence before anything is typed. Tokens: combos (`ctrl+shift+v`, `mod+shift+h`, `enter`, `escape`, `tab`, `up`, `pageup`, `f5`, `space`, `backspace`), `text:…`, `wait:500ms` (29 s at most per wait; a request that has not finished after about a minute drops its remaining keys rather than typing them late).

`send-keys` targets a pane's program (vim, a shell) without neonmux intercepting anything. Prefer it to type into a given terminal without moving the focus.

## Wait

```sh
neonmux wait 2 'PASS|FAIL'           # until pane 2's screen matches (regexp), prints the line
neonmux wait -t 60 2 '\$ *$'         # -t: 60, 60s, 500ms
neonmux wait -gone 2 'Compiling'     # until it goes away
```

Use `wait` rather than `sleep`. On timeout, the screen is printed on stderr. A new shell can take a while to show its prompt (heavy rc files): give `-t` some room.

## Show the user an image

```sh
neonmux open /tmp/diagram.png        # opens an image pane in the window
```

Use `neonmux open <file>` to show the user an image. Do not use `kitten icat` or terminal graphics escape codes: captured agent output does not show them.

## Recipes

- Run and watch a program next to you: `neonmux key mod+enter`, then `neonmux state` for the new pane's number (the `focused` one), `neonmux send-keys <n> "text:go test ./..." enter`, `neonmux wait -t 120 <n> '^(ok|FAIL)'`, `neonmux capture <n>`.
- Check a change to neonmux's interface: build it, start a separate test window (below), drive it through `NEONMUX_WINDOW_SOCK`, `shot`, look at the PNG, then close it.
- Do not type into the user's panes without a reason: check with `state` which one has the focus before a `key`. For a trial, open your own pane or a test window.

## A test window that leaves the user's alone

From the neonmux repository:

```sh
go build -o neonmux .
D="$(dirname "$NEONMUX_SOCK")"       # the daemon's private directory, where windows put their socket
env -u NEONMUX_PANE -u NEONMUX_SOCK NEONMUX_MUTE=1 setsid -f sh -c "cd /tmp && exec $PWD/neonmux -no-server >/tmp/nm-test.log 2>&1" </dev/null
P=$(pgrep -f "^$PWD/neonmux -no-server")
export NEONMUX_WINDOW_SOCK="$D/gui-$P.sock"
# … state, key, shot …
kill $P
```

On macOS, `setsid` does not exist: `nohup … &` does the job. On Linux, started from a pane, the window may lack `XAUTHORITY` (X11 "Authorization required"): take it, with `DISPLAY` and `WAYLAND_DISPLAY`, from the environment of a graphical process (`/proc/<pid>/environ`).

## Without a window socket at hand

The window commands find the open window by themselves (the newest `gui-<pid>.sock` next to the daemon socket whose process is alive). With several windows, set `NEONMUX_WINDOW_SOCK`. `capture`, `send-keys` and `ls` also work with the daemon alone, by terminal key or program name.
