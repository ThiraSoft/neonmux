# Persistent sessions

Terminals that outlive the window, restarts and reboots.

Terminals do not live in the window but in a small daemon (`neonmux server`), started automatically on first launch and detached from any terminal.

- **Closing the window** (`mod+q`, the red button, end of session) saves the layout and detaches the terminals. A build, a Claude agent or an nvim with unsaved buffers keep running. When you reopen, each pane reattaches to its session and the screen comes back exactly as it is now, scrollback included.
- **Closing a pane** (`mod+w`, or `exit` in the shell) really ends its process.
- **After a reboot**, the processes are gone, but the daemon has saved every terminal to disk (every 15 s if it changed, and at shutdown when the system sends it SIGTERM). On the next launch, each terminal gets its directory back and the old screen stays on display with a line like `── session restored on Oct 21 at 14:32 ──` below it, then a fresh shell starts. The interactive tools in the `restore_commands` list (nvim, vim, less, htop, btop, lazygit…) are relaunched with the same arguments, and `claude` with `--continue` to pick up the conversation. A build, a server or a script is never relaunched automatically.
- **With `"remote": true`**, the daemon does this restore itself when it starts, and stays up with no terminal and no window as long as remote control is on, so a phone finds your terminals after a reboot. It needs a daemon started at boot (a systemd user service, for example). The relaunched tools then start at boot, not at the next launch, and the restored shells have the daemon's environment, without the variables of your graphical session.
- A notification (bell, OSC 9, `neonmux notify`) received while the window is closed shows up when you reopen it.

### Restarting on a new version

After `./install.sh`, the palette ("Restart neonmux (window and daemon, new version)") or `neonmux restart` relaunches the window **and** the daemon on the new binary, **without interrupting programs**: a build, nvim or a Claude agent keep running during the switch. The daemon replaces itself with `exec`, keeping its PID (it stays the parent of the shells), its PTYs and its open socket, and hands the state of every terminal over to the new binary. Reading from the PTYs goes through `poll()` and stops before reading anything, so not a single byte is lost. Connections that arrive during the switch wait in the kernel queue. The switch takes about fifty milliseconds.

If the connection to the daemon drops for another reason (a crash), the window reconnects by itself and reattaches to its sessions. The ones the new daemon does not know are restored from disk, as after a reboot.

### Forced restart

`neonmux restart --force` is the way out when something is stuck. It goes through neither the daemon nor the window, so it works even when they no longer answer. It finds the daemon through its socket (the kernel knows the PID of the process listening on it, even a frozen one) and the windows through their PID files. It then collects all their descendants before killing anything, to also catch `nohup` and detached jobs. Windows go first, then the daemon, with SIGTERM first and then SIGKILL after a second and a half for whatever resists. Saved sessions and the layout are erased, the config is kept, and a fresh window starts (`--no-launch` to skip it, `-- args` to pass it options).

Run from a pane, it detaches first, since its own terminal is about to be killed: the report goes to `~/.local/state/neonmux/force.log`. The palette also offers "Force restart (kills every terminal, starts from scratch)". A process group is only killed if its leader is one of the targets, so a window started by a script never takes the script down with it.

```sh
neonmux restart          # window + daemon, hot restart
neonmux restart --force  # kill everything and start from scratch, even when everything is stuck
neonmux restart-server   # the daemon alone
neonmux ls               # terminals kept by the daemon: key, state, process, directory
neonmux -fresh           # start without restoring the saved layout
neonmux -no-server       # terminals tied to the window, with no daemon and no restore
```

One-off launches (`-e`, `-demo`, `-screenshot`) do not use the daemon. The data lives in `~/.local/state/neonmux` (or `$XDG_STATE_HOME/neonmux`): `layout.json` for the window, `terms/<key>.snap` for the terminals' content (the compact scrollback format, about 1 MB for 10,000 lines), and `terms/<key>.imgs` for the kitty images a terminal shows, rewritten only when they change.

## Prewarmed terminal

An interactive shell with a heavy setup (nvm, conda, compinit, prompt theme…) can take several seconds to start. So neonmux keeps one shell **in reserve**, already initialized: `mod+enter` or `t` in the explorer adopt it instantly, and a new spare gets ready two seconds later.

- The spare starts in the directory of the focused terminal. If the new terminal must open somewhere else, neonmux types ` cd -- 'directory'` (with a leading space) then `ctrl+l`, hides the pane while it redraws, and none of it stays on screen.
- To keep that `cd` out of your history, enable `setopt HIST_IGNORE_SPACE` in your `.zshrc` (`HISTCONTROL=ignorespace` for bash). The automatic relaunch after a reboot goes through the same mechanism.
- The spare is recycled if a shell init file changes (`.zshrc`, `.zprofile`, `.zshenv`, `.bashrc`…) or once it is more than 30 minutes old. It only serves plain shells: an editor or a command always goes through a normal start.
- In the daemon, the spare is ephemeral. It is never saved to disk and it ends when the window closes, even if the window disappears abruptly. `neonmux ls` shows it with the state `spare`.
- Cost: one idle shell (about ten MB, 0% CPU). `"prewarm": false` turns it off.

Measured with a `.zshrc` that took 1.6 s to start: a command typed right after `mod+enter` runs after 1.8 to 1.9 s cold, and after 0.4 s with the spare (0.3 s of which comes from the measuring script itself).
