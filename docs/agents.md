# Agents and the garage

How neonmux follows your agents, and how to run many of them without showing them all.

neonmux knows what your agents are doing. Claude Code (2.1.295 or later) reports its own state to the terminal through the [Program Status Protocol](https://superlogical.com/rex/docs/build/program-status) (OSC 7501): ready, working along with what it is doing, blocked on a permission or a question, done or failed. neonmux implements this protocol, so there is nothing to set up. Any program that speaks it (Homebrew, Terraform…) benefits as well.

- **Header**: in place of the terminal size, an icon and the agent's state with how long it has been in it ("Running the tests · 42s", "permission: Bash · 4m", "done · 3m"). Its changes since the last commit come first (`+120 −34`), read in the background without ever locking the git index.
- **Bottom bar**: the same icon after each agent's name.
- **When an agent gets blocked** out of your sight, its pane pulses like a notification and a toast says so. When it finishes, a quieter toast ("[2] claude: done", or "failed"), and if its pane is on screen, its border flashes twice so you can tell which one it was (`agent_flash` in the config turns it off or changes the number and length of the flashes). `mod+n` goes to the next one waiting for you, oldest first, and `mod+shift+n` takes you back where you were. Nothing moves on its own.
- **Picker** (`mod+d`): every pane in the palette, agents first from most to least urgent, then the others (terminals, diff, help…), on screen or in the garage. Type part of the project name, the branch or the topic, then `enter`. `mod+w` closes the chosen pane without leaving the list.
- **Agent board** (`mod+shift+d`): a floating pane at the top right that you can keep open. For each agent it shows the state, the directory, the branch and the changes. `enter` goes to the agent, `d` opens its diff, `q` closes.
- **Diff** (`mod+shift+g`, or `neonmux diff [directory]`): the repository's changes since the last commit, new files included, floating over the right half so the agent stays in view. `j k` line, `J K` file, `ctrl+d/u` half page, `enter` opens the editor at that line, `r` reloads, `q` closes. It reloads by itself when an agent in the same repository moves on. `mod+f` enlarges it.
- **Agent on a worktree**: `neonmux agent fix-login "fix the login bug"` runs `claude --worktree fix-login` in a new tile named after the worktree (also from the palette, "New agent on a worktree…"). Claude creates the worktree in `.claude/worktrees/` and offers to keep or delete it when it exits. A shell then stays in that directory. Claude refuses a repository you have never trusted: run `claude` once inside it.

## Prompter

The prompter (`mod+shift+e`, or "Prompter" in the palette) plays the replies of your agents one word at a time, the current word fixed on an anchor with a few words before and after it, so your eyes never have to look for the next one. It follows every agent, garage included, Claude Code and agy (agy needs `sqlite3`), and only keeps what arrives after you open it.

- **Tracks**: each final reply of an agent is a track, in the order they arrive. The header names the agent, the bar shows how far you are in the track. A question Claude Code asks you with its choices is a track too; a permission request is not.
- **Keys**: `space` or `enter` play and pause, `left` and `right` move one word, `up` and `down` change the speed, `n` leaves the track and plays the next one. The buttons under the bar do the same with the mouse.
- **No surprise**: the prompter never moves to the next track by itself. At the end of a track, play reads it again; the next one waits for `n`. A track you left is gone (its agent's terminal still has it).
- **One per device**: the window and each phone have their own queue and position. A disconnect or a restart of the window keeps the queue; closing the prompter drops it.
- Code blocks, tables, markdown and long paths are simplified by default; see the `prompter_*` keys in [configuration](configuration.md).

## Garage

To run many agents without showing them all, neonmux separates the panes that run from the panes that are on screen. A pane **in the garage** keeps everything going: its process runs in the daemon, and its agent state, notifications and git counter stay up to date. It is simply neither drawn nor placed in the layout, and it has no number.

- `mod+m` sends the active pane to the garage, tile or floating.
- **Tiles full**: a new pane (`mod+enter`, `neonmux agent`, an opened file…) sends the active tile to the garage instead of stopping it. One `mod+enter` too many no longer kills an agent at work.
- `mod+n` brings a parked agent that is waiting for you out as a large floating pane in the center, above everything else, without touching the layout. The picker (`mod+d`) does the same for any parked pane, and the board (`mod+shift+d`) for agents. The shortcut of the help, the board or the diff also brings its own pane out if it is in the garage.
- Only one pane comes out at a time: bringing out another one sends the previous one back to the garage. The pane brought out stays in the center until you send it back, even after it has had its answer.
- `mod+shift+n` (or `mod+m`) sends it back to the garage, and the focus returns where you were. `mod+shift+f` keeps it on screen: as a tile if one is free, otherwise as a floating pane where it is.
- **Bottom bar**: the visible panes, then "garage 46 · 3 waiting". The summary pulses when a parked agent is blocked. With everything in the garage, the welcome screen reminds you that `mod+d` finds the panes. A parked agent that gets blocked rings like the others: a toast "[garage] claude · question: …" and first place for `mod+n`, the picker and the board.
- `max_panes` only limits the tiles on screen. In total, garage included, neonmux opens at most 64 panes.
- The garage is saved with the layout: after a restart, parked panes reattach to their session without being shown, at the size they had.

The window still decodes the output of every session, garage included. If many very chatty agents end up costing too much, the HUD (`mod+.`) shows the number of panes and how many are in the garage.
