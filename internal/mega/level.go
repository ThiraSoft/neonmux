// Package mega is a small 8-bit run and gun hidden in the city background:
// typing "mega" over it starts the game. The hero runs on the rooftops in
// front of the city, shoots, and takes a new weapon from each guardian it
// beats. Everything is drawn in big pixels, in the colors of the theme.
package mega

// The level is a grid of 16 pixel tiles, Rows high. Its cells:
//
//	'#' rooftop (solid)   '=' girder (solid)   '^' spikes (deadly)
//	'D' arena gate (solid when closed)        ' ' air
const (
	T    = 16
	Rows = 15
	// ViewH is the height of the view in game pixels: the level fills it.
	ViewH = Rows * T
)

type foeKind int

const (
	walker foeKind = iota
	flyer
	turret
	hopper
)

type spawn struct {
	kind foeKind
	col  int
	row  int // row of the foe's feet (flyers: their height)
}

// arena is a guardian's room, closed by two gates.
type arena struct {
	in, out int // columns of the gates
	boss    int
	floor   int // row of the floor's top
}

type level struct {
	w      int
	cells  [Rows][]byte
	spawns []spawn
	arenas []arena
	cps    []int // checkpoint columns, in order
}

func (l *level) at(c, r int) byte {
	if c < 0 || c >= l.w {
		return '#' // the ends of the level are walls
	}
	if r < 0 || r >= Rows {
		return ' '
	}
	return l.cells[r][c]
}

// top returns the row of the first solid cell of column c, Rows if none.
func (l *level) top(c int) int {
	for r := 0; r < Rows; r++ {
		if b := l.at(c, r); b == '#' || b == '=' {
			return r
		}
	}
	return Rows
}

type builder struct{ l *level }

func (b *builder) x() int { return b.l.w }

// col appends a column whose cells from row top down are rooftop.
func (b *builder) col(top int) {
	for r := 0; r < Rows; r++ {
		c := byte(' ')
		if r >= top {
			c = '#'
		}
		b.l.cells[r] = append(b.l.cells[r], c)
	}
	b.l.w++
}

// floor appends n columns of rooftop h tiles high.
func (b *builder) floor(n, h int) int {
	x := b.x()
	for range n {
		b.col(Rows - h)
	}
	return x
}

// pit appends n empty columns.
func (b *builder) pit(n int) int {
	x := b.x()
	for range n {
		b.col(Rows)
	}
	return x
}

func (b *builder) set(c, r int, v byte) { b.l.cells[r][c] = v }

// girder lays a girder n tiles long at row r from column c.
func (b *builder) girder(c, r, n int) {
	for i := range n {
		b.set(c+i, r, '=')
	}
}

// spikes covers n tiles of the roof from column c.
func (b *builder) spikes(c, n int) {
	for i := range n {
		b.set(c+i, b.l.top(c+i)-1, '^')
	}
}

// foe places a foe standing on column c (flyers: at row r).
func (b *builder) foe(k foeKind, c int, r ...int) {
	row := b.l.top(c) - 1
	if len(r) > 0 {
		row = r[0]
	}
	b.l.spawns = append(b.l.spawns, spawn{k, c, row})
}

func (b *builder) cp() { b.l.cps = append(b.l.cps, b.x()+1) }

// arena appends a guardian's room: a gate, a flat floor, a gate.
func (b *builder) arena(boss int) {
	const h, n = 3, 18
	in := b.x()
	b.floor(n+2, h)
	out := b.x() - 1
	for r := 0; r < Rows-h; r++ {
		b.set(in, r, 'D')
		b.set(out, r, 'D')
	}
	b.l.arenas = append(b.l.arenas, arena{in: in, out: out, boss: boss, floor: Rows - h})
}

// buildLevel lays out the four sections, each ending with a guardian.
func buildLevel() *level {
	b := &builder{l: &level{}}

	// 1: the first rooftops.
	b.cp()
	b.floor(16, 3)
	b.foe(walker, 13)
	x := b.floor(8, 4)
	b.foe(walker, x+5)
	b.pit(3)
	x = b.floor(10, 3)
	b.foe(flyer, x+7, 7)
	x = b.floor(6, 5)
	b.foe(turret, x+3)
	b.floor(5, 3)
	x = b.pit(5)
	b.girder(x+1, 10, 3)
	x = b.floor(12, 3)
	b.foe(walker, x+4)
	b.foe(hopper, x+9)
	b.cp()
	b.floor(6, 3)
	b.arena(0)

	// 2: gaps and drones.
	b.cp()
	b.floor(8, 3)
	b.pit(3)
	x = b.floor(4, 4)
	b.foe(flyer, x+2, 6)
	b.pit(3)
	x = b.floor(5, 5)
	b.foe(hopper, x+3)
	x = b.pit(4)
	b.girder(x+1, 9, 2)
	b.foe(flyer, x+2, 5)
	x = b.floor(10, 3)
	b.foe(walker, x+3)
	b.foe(flyer, x+8, 7)
	x = b.floor(4, 6)
	b.foe(turret, x+2)
	x = b.floor(8, 2)
	b.foe(hopper, x+5)
	b.pit(2)
	x = b.floor(8, 3)
	b.foe(hopper, x+4)
	b.foe(flyer, x+6, 6)
	b.cp()
	b.floor(6, 3)
	b.arena(1)

	// 3: spikes and girders.
	b.cp()
	b.floor(6, 3)
	x = b.floor(12, 3)
	b.girder(x+1, 9, 2)
	b.spikes(x+5, 3)
	b.foe(turret, x+10)
	x = b.pit(7)
	b.girder(x+1, 10, 2)
	b.girder(x+4, 8, 2)
	b.foe(flyer, x+3, 5)
	x = b.floor(12, 4)
	b.foe(walker, x+3)
	b.foe(walker, x+8)
	x = b.floor(4, 6)
	b.foe(turret, x+2)
	x = b.floor(10, 3)
	b.spikes(x+5, 3)
	b.foe(hopper, x+7)
	b.cp()
	b.floor(6, 3)
	b.arena(2)

	// 4: everything at once.
	b.cp()
	b.floor(6, 3)
	x = b.pit(3)
	b.foe(flyer, x+1, 6)
	x = b.floor(6, 4)
	b.foe(turret, x+4)
	x = b.pit(5)
	b.girder(x+1, 9, 3)
	b.foe(flyer, x+3, 5)
	x = b.floor(10, 3)
	b.spikes(x+4, 2)
	b.foe(hopper, x+2)
	b.foe(walker, x+8)
	x = b.floor(6, 5)
	b.foe(turret, x+3)
	b.foe(flyer, x+5, 4)
	x = b.floor(10, 3)
	b.foe(walker, x+3)
	b.foe(hopper, x+6)
	b.foe(walker, x+9)
	b.cp()
	b.floor(6, 3)
	b.arena(3)
	b.floor(12, 3)
	return b.l
}
