package mega

import "testing"

func TestSpritesAreRectangles(t *testing.T) {
	all := map[string]sprite{"heroStand": heroStand, "heroRun0": heroRun[0], "heroRun1": heroRun[1],
		"heroJump": heroJump, "cannon": cannon, "walker0": walkerSpr[0], "walker1": walkerSpr[1],
		"flyer0": flyerSpr[0], "flyer1": flyerSpr[1], "turretShut": turretShut, "turretOpen": turretOpen,
		"hopper": hopperSpr, "hopperSquat": hopperSquat, "guard": guardSpr, "siren": sirenSpr}
	for name, s := range all {
		for i, r := range s.rows {
			if len(r) != s.w {
				t.Errorf("%s row %d: %d wide, want %d", name, i, len(r), s.w)
			}
		}
	}
	if heroStand.h != heroJump.h || heroRun[0].h != heroStand.h {
		t.Error("hero frames differ in height")
	}
}

func TestLevelShape(t *testing.T) {
	l := buildLevel()
	for r := range Rows {
		if len(l.cells[r]) != l.w {
			t.Fatalf("row %d: %d cells, want %d", r, len(l.cells[r]), l.w)
		}
	}
	if len(l.arenas) != 4 {
		t.Fatalf("%d arenas, want 4", len(l.arenas))
	}
	for i := 1; i < len(l.cps); i++ {
		if l.cps[i] <= l.cps[i-1] {
			t.Fatalf("checkpoints out of order: %v", l.cps)
		}
	}
	for _, c := range l.cps {
		if l.top(c) >= Rows {
			t.Errorf("checkpoint %d over a pit", c)
		}
	}
}

// bot plays: it holds right, jumps at walls and edges, and shoots.
type bot struct {
	g    *Game
	keys map[string]bool
}

func (b *bot) tick() {
	g, p := b.g, &b.g.p
	b.keys = map[string]bool{"d": true}
	ahead := cell(p.x + p.w + 6)
	feet := cell(p.y + p.h + 1)
	wall := g.solid(ahead, cell(p.y+p.h-1)) || g.solid(ahead, cell(p.y+2))
	edge := !g.solid(ahead, feet) && !g.solid(ahead, feet+1)
	spikes := g.lv.at(cell(p.x+p.w+3), feet-1) == '^'
	if p.ground && (wall || edge || spikes) {
		g.Key("space", false)
		b.keys["space"] = true
	}
	if !p.ground && p.vy < 0 {
		b.keys["space"] = true
	}
	if g.frame%10 == 0 {
		g.Key("j", false)
	}
	g.SetHeld(func(n string) bool { return b.keys[n] })
}

// TestReachFirstGuardian checks that the first section can be crossed by
// running and jumping: no gap too wide, no step too high.
func TestReachFirstGuardian(t *testing.T) {
	g := New(1)
	b := &bot{g: g}
	for range 60 * 120 {
		b.tick()
		g.p.inv = 10 // the bot only tests the jumps
		g.Update(step)
		if g.st == stIntro || g.boss != nil {
			return
		}
		if g.st == stDead {
			t.Fatalf("bot died at x=%.0f (col %d), y=%.0f", g.p.x, cell(g.p.x), g.p.y)
		}
	}
	t.Fatalf("bot stuck at x=%.0f (col %d)", g.p.x, cell(g.p.x))
}

// TestCrossWholeLevel lets the bot cross every section, the guardians
// beaten by hand once reached.
func TestCrossWholeLevel(t *testing.T) {
	g := New(2)
	b := &bot{g: g}
	beaten := 0
	for range 60 * 600 {
		b.tick()
		g.p.inv = 10
		g.Update(step)
		if g.boss != nil && g.st == stPlay {
			g.arena = beaten
			g.killBoss()
			beaten++
		}
		if g.st == stDead {
			t.Fatalf("bot died at col %d, y=%.0f (%d guardians beaten)", cell(g.p.x), g.p.y, beaten)
		}
		if g.st == stWin {
			for w := range nWeapons {
				if !g.unlocked[w] {
					t.Errorf("weapon %d still locked", w)
				}
			}
			return
		}
	}
	t.Fatalf("bot stuck at col %d after %d guardians", cell(g.p.x), beaten)
}

func TestInventoryNavigation(t *testing.T) {
	g := New(3)
	g.unlocked[fan], g.unlocked[mortar] = true, true
	g.Key("i", false)
	if !g.inv {
		t.Fatal("i does not open the inventory")
	}
	g.Key("l", false) // right: fan
	if g.weapon != fan {
		t.Fatalf("weapon %d, want fan", g.weapon)
	}
	g.Key("j", false) // down: mortar
	if g.weapon != mortar {
		t.Fatalf("weapon %d, want mortar", g.weapon)
	}
	g.Key("q", false) // left: lance, locked
	if g.weapon != mortar || g.invCur != lance {
		t.Fatalf("weapon %d cursor %d, want mortar on lance", g.weapon, g.invCur)
	}
	g.Key("q", false) // nothing further left
	if g.invCur != lance {
		t.Fatalf("cursor %d left the grid", g.invCur)
	}
	g.Key("z", false)
	if g.invCur != pulse || g.weapon != pulse {
		t.Fatal("up does not reach the pulse")
	}
	if !g.Back() || g.inv {
		t.Fatal("escape does not close the inventory")
	}
}

func TestWeaponsSpendEnergy(t *testing.T) {
	g := New(4)
	g.unlocked[lance] = true
	g.weapon = lance
	g.setState(stPlay)
	g.fireReq = true
	g.updatePlayer(true)
	if g.energy[lance] != maxEn-weaponCost[lance] || len(g.shots) != 1 {
		t.Fatalf("energy %d, %d shots", g.energy[lance], len(g.shots))
	}
	g.energy[lance] = 1
	g.shots = nil
	g.fireReq = true
	g.updatePlayer(true)
	if len(g.shots) != 0 {
		t.Fatal("fired without energy")
	}
}

// TestArcadesRun drives every game with random keys for a while: no
// panic, and the drawing-free logic keeps going.
func TestArcadesRun(t *testing.T) {
	names := []string{"z", "q", "s", "d", "space", "j", "i", "h", "l", "k", "enter"}
	for kind := range arcades {
		g := ForBackground(kind, 7)
		held := map[string]bool{}
		g.SetHeld(func(n string) bool { return held[n] })
		for i := range 60 * 120 {
			if i%9 == 0 {
				n := names[(i*7/9)%len(names)]
				held[n] = !held[n]
				g.Key(n, false)
			}
			g.Step(step, 1920, 1080, false)
		}
	}
}

func TestSnakeEatsAndGrows(t *testing.T) {
	g := newSnake(1)
	g.Step(0, 1920, 1080, false)
	h := g.body[0]
	g.food = pt{h.x + 1, h.y}
	n := len(g.body)
	for range g.every * 6 {
		g.tick()
	}
	if g.eaten != 1 || len(g.body) <= n {
		t.Fatalf("eaten %d, length %d (was %d)", g.eaten, len(g.body), n)
	}
}

func TestSnakeNoReverse(t *testing.T) {
	g := newSnake(1)
	g.Step(0, 1920, 1080, false)
	g.Key("q", false) // left while going right: ignored
	if len(g.queue) != 0 {
		t.Fatal("a half turn was queued")
	}
}

func TestLavaRisesAndKills(t *testing.T) {
	g := newLava(1)
	for range 60 * 120 {
		g.Step(step, 1920, 1080, false)
		if g.over {
			return
		}
	}
	t.Fatal("standing still, the lava never reached the hero")
}

func TestLavaLedgesReachable(t *testing.T) {
	g := newLava(3)
	for i := 1; i < len(g.ledges); i++ {
		if gap := g.ledges[i-1].y - g.ledges[i].y; gap > 75 {
			t.Fatalf("gap of %.0f px between ledges", gap)
		}
	}
}

func TestStarsWavesAdvance(t *testing.T) {
	g := newStars(1)
	g.inv = 1 << 30
	held := map[string]bool{"j": true}
	g.SetHeld(func(n string) bool { return held[n] })
	for i := range 60 * 240 {
		held["z"], held["s"] = i/60%2 == 0, i/60%2 == 1
		g.Step(step, 1920, 1080, false)
		if g.wave > 3 {
			return
		}
	}
	t.Fatalf("still at wave %d, score %d", g.wave, g.score)
}
