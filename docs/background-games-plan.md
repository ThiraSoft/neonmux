# Background games: plan

One small 8-bit game per background. In zen mode (or with no pane on
screen), typing `game` starts the game of the current background. Escape
leaves it. All thirteen are done, in `internal/mega` (registry in `arcade.go`,
shared loop in `base`).

## Shared rules

- One package per game under `internal/` (or `internal/bggames/<name>`),
  pure logic plus a `Draw(P, font, theme, W, H)`, like `internal/mega`.
- Fixed step at 60 Hz, big pixels (`Scale(h)`), theme colors only, the
  background still drawn behind under a light veil.
- Keys: zqsd or arrows, space (jump or action), j (shoot), i (menu if
  any). Held keys come from `megaRun.keys` (layout names).
- Glue: `internal/ui/mega.go` turns into a registry `background -> New()`;
  `megaWatch` starts `games[a.bg.Kind]` on the word `game`.
- Headless bot test for each game (it can be finished or survived), like
  `TestCrossWholeLevel`.
- Optional: a background with a camera implements `pan(dx)` (see city).

## Games

| Background | Game | Outline |
|---|---|---|
| city | Run and gun (done) | Rooftops, 4 guardians, each gives a weapon (PULSE, FAN, LANCE, MORTAR), inventory on i. |
| grid | Grid runner (done) | Pseudo-3D synthwave lane runner: 3 to 5 lanes, q/d change lane, space jumps walls, speed rises, score by distance. |
| rain | Glyph defense (done) | Kana fall in columns; a cannon at the bottom (q/d, j) destroys them before they land. Waves speed up, some glyphs need several hits. |
| stars | Side shooter (done) | Horizontal shoot'em up: ship moves zqsd, j shoots, power-ups (spread, laser, shield), enemy waves, a boss every 3 waves. |
| dust | Firefly (done) | A firefly rises through air currents and thorns; q/d steer, space flutters. Calm, score by height, motes to collect. |
| aurora | Slalom (done) | Downhill ski under the aurora: gates to pass, pines to dodge, bumps to jump (space). Timed runs, best time kept. |
| topo | Contour golf (done) | Mini golf on the contour map: the slopes of the topo field bend the ball. Aim with q/d, power with space held, 9 holes, par. |
| lava | Lava climb (done) | Vertical climb: platforms crumble, the lava rises. Jump higher and higher, height is the score. |
| fire | Ember breakout (done) | Breakout where bricks are embers that light up again if left too long. Paddle q/d, space launches, levels. |
| life | Life puzzle (done) | Puzzles on Conway's life: place N cells, run, the pattern must reach a target or clear a zone. A dozen levels. |
| swarm | Predator (done) | The player is a predator hunting the swarm; boids really flee (reuse the swarm rules). Catch as many as possible in 60 s. |
| circuit | Trace snake (done) | Snake on the circuit traces: the pulse grows with each component eaten, turns only at the trace nodes. |
| scope | Vector tennis (done) | Pong-like on the oscilloscope screen, glowing trail, against an AI that gets better. |

Suggested order: stars, lava, circuit (quick to play, reuse the mega
engine: sprites, bodies, tile collisions), then the others.
