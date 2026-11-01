# Games

Something to do while the agents work.

Two small games to pass the time while agents work, both in the palette. Their texts are random sections of the English Wikipedia: the last 50 articles stay in a local cache (`~/Library/Caches/neonmux/wikipedia` on macOS, `~/.cache/neonmux/wikipedia` on Linux) so that you can still play offline. Nothing is fetched until a game opens.

- **Game: typing test**: a typing test in the spirit of monkeytype. Pick a duration (15, 30 or 60 seconds) or a number of words (25, 50, 100) with the arrows before the first key, then type. Wrong letters stay marked, `backspace` fixes them, `alt+backspace` deletes the word, `tab` starts over on a new text. At the end: words per minute, accuracy, raw speed and a speed curve.
- **Game: vim kata**: vim missions in nine chapters (moving, typos, operators, text objects, repeat, counts, moving text, visual block and macros, Ex commands), each one a gesture to learn such as `ci(`, `cgn` or `:g`. You play in the real Neovim (`nvim --clean`, version 0.10 or later, found through your login shell), and the score is the number of keys against a par: the shortest known solution for that very text. At the end, the par is broken down gesture by gesture, which is where you learn something. Three stars at the par or below, two up to one and a half times it; the next chapter opens once 5 missions of the current one have a star. The time is a record of its own and never counts in the stars. `:q` gives up and shows the solution.
- A game comes out large in the center. **As soon as you leave it** (`mod+n` toward an agent, `mod+a`, a click on a tile…) it goes to the garage and its clock stops, so it never hides the agent you went to answer. `mod+shift+n` brings it back where it was.
- Records (best speed per mode, stars, keys and time per mission) are kept in `~/.local/state/neonmux/games.json`.
