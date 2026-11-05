package mega

// Sprites are drawn facing right, one letter per pixel:
//
//	'.' transparent   'K' outline   'P' main color   'S' main color, shaded
//	'W' highlight     'F' face, light parts          'E' eye   'D' metal
type sprite struct {
	w, h int
	rows []string
}

func spr(rows ...string) sprite {
	w := 0
	for _, r := range rows {
		w = max(w, len(r))
	}
	return sprite{w: w, h: len(rows), rows: rows}
}

// with replaces the last rows of s (legs of another frame).
func (s sprite) with(tail ...string) sprite {
	rows := append([]string(nil), s.rows[:len(s.rows)-len(tail)]...)
	return spr(append(rows, tail...)...)
}

var heroStand = spr(
	"......KK........",
	".....KFK........",
	"....KKKKKKK.....",
	"...KPPPPPPPK....",
	"..KPPWPPPPPPK...",
	"..KPKKKKKKKPK...",
	"..KPKFFFFEFKK...",
	"..KPKKKKKKKPK...",
	"...KPPPPPPPK....",
	"....KKSSSKK.....",
	"...KPPPPPPPK....",
	"..KPPKPPPKPPK...",
	"..KSSKPPPKSSK...",
	"...KKKPPPKKK....",
	"....KSSSSSK.....",
	"....KPPKPPK.....",
	"....KPPKPPK.....",
	"...KPPK.KPPK....",
	"..KSSSK.KSSSK...",
	"..KKKKK.KKKKK...",
)

var heroRun = [2]sprite{
	heroStand.with(
		"....KPPKPPK.....",
		"...KPPK.KPPK....",
		"..KPPK...KPPK...",
		".KSSK.....KSSK..",
		".KKK.......KKK..",
	),
	heroStand.with(
		"....KPPPPK......",
		"....KPPPPK......",
		".....KPPK.......",
		"....KSSSSK......",
		"....KKKKKK......",
	),
}

var heroJump = heroStand.with(
	"...KPPKKPPK.....",
	"..KPPK..KPPK....",
	"..KSSK...KSSK...",
	"..KKK.....KKK...",
	"................",
)

// cannon is the arm held out while shooting, at row 10 of the hero.
var cannon = spr(
	"KKKK.",
	"PPPPK",
	"SSSSK",
	"KKKK.",
)

var walkerSpr = [2]sprite{
	spr(
		".....KKKKKK.....",
		"...KKPPPPPPKK...",
		"..KPPWPPPPPPPK..",
		".KPPPKKKKKKPPPK.",
		".KPPKEEEEEEKPPK.",
		".KPPPKKKKKKPPPK.",
		"..KSSSSSSSSSSK..",
		"...KKKKKKKKKK...",
		"...KDK....KDK...",
		"..KDK......KDK..",
		"..KK........KK..",
		"................",
	),
}

func init() {
	walkerSpr[1] = walkerSpr[0].with(
		"....KDK..KDK....",
		"....KDK..KDK....",
		"....KK....KK....",
		"................",
	)
}

var flyerSpr = [2]sprite{
	spr(
		"KKKKKKK.KKKKKKK.",
		".......K........",
		"....KKKKKKK.....",
		"...KPPPPPPPK....",
		"..KPWPKEEKPPK...",
		"..KPPPPPPPPPK...",
		"...KSSSSSSSK....",
		"....KKKKKKK.....",
	),
	spr(
		"...KKKK.KKKK....",
		".......K........",
		"....KKKKKKK.....",
		"...KPPPPPPPK....",
		"..KPWPKEEKPPK...",
		"..KPPPPPPPPPK...",
		"...KSSSSSSSK....",
		"....KKKKKKK.....",
	),
}

var turretShut = spr(
	"................",
	"................",
	"................",
	"................",
	"................",
	"................",
	".....KKKKKK.....",
	"...KKPPPPPPKK...",
	"..KPPWPPPPPPPK..",
	"..KPPPPPPPPPPK..",
	".KSSSSSSSSSSSSK.",
	".KKKKKKKKKKKKKK.",
	".KDDDDDDDDDDDDK.",
	".KDKDDKDDKDDKDK.",
	".KDDDDDDDDDDDDK.",
	".KKKKKKKKKKKKKK.",
)

var turretOpen = spr(
	"................",
	"................",
	".....KKKKKK.....",
	"...KKPPPPPPKK...",
	"..KPPWPPPPPPPK..",
	"..KPPKKKKKKPPK..",
	"..KPKEEEEEEKPK..",
	"..KPPKKKKKKPPK..",
	"..KSSSSSSSSSSK..",
	"...KK......KK...",
	"...KK......KK...",
	".KKKKKKKKKKKKKK.",
	".KDDDDDDDDDDDDK.",
	".KDKDDKDDKDDKDK.",
	".KDDDDDDDDDDDDK.",
	".KKKKKKKKKKKKKK.",
)

var hopperSpr = spr(
	"...KKKKKK...",
	"..KPPPPPPK..",
	".KPWPPPPPPK.",
	".KPKEKKEKPK.",
	".KPPPPPPPPK.",
	"..KSSSSSSK..",
	"...KKKKKK...",
	"....KDDK....",
	"...KDKKDK...",
	"....KDDK....",
	"...KDKKDK...",
	"....KDDK....",
	"..KKSSSSKK..",
	"..KKKKKKKK..",
)

// hopperSquat is the hopper about to jump, its spring pressed.
var hopperSquat = spr(
	"............",
	"............",
	"............",
	"...KKKKKK...",
	"..KPPPPPPK..",
	".KPWPPPPPPK.",
	".KPKEKKEKPK.",
	".KPPPPPPPPK.",
	"..KSSSSSSK..",
	"...KKKKKK...",
	"...KDKKDK...",
	"....KDDK....",
	"..KKSSSSKK..",
	"..KKKKKKKK..",
)

var guardSpr = spr(
	"........KKKKKKKK........",
	".......KPPPPPPPPK.......",
	"......KPPWPPPPPPPK......",
	"......KPKKKKKKKKPK......",
	"......KPKEEKKEEKPK......",
	"......KPKKKKKKKKPK......",
	".......KPPPPPPPPK.......",
	"........KKSSSSKK........",
	"....KKKKPPPPPPPPKKKK....",
	"...KPPPPPPPPPPPPPPPPK...",
	"..KPPWPPPPKKKKPPPPPPPK..",
	"..KPPPPPPKFFFFKPPPPPPK..",
	"..KSSPPPPKFFFFKPPPPSSK..",
	"..KSSKPPPPKKKKPPPPKSSK..",
	"..KPPKPPPPPPPPPPPPKPPK..",
	"..KPPKKSSSSSSSSSSKKPPK..",
	"..KFFK.KPPPPPPPPK.KFFK..",
	"...KK..KSSSSSSSSK..KK...",
	".......KPPPKKPPPK.......",
	"......KPPPK..KPPPK......",
	"......KPPPK..KPPPK......",
	"......KSSSK..KSSSK......",
	"......KPPPK..KPPPK......",
	".....KPPPPK..KPPPPK.....",
	"....KSSSSSK..KSSSSSK....",
	"....KKKKKKK..KKKKKKK....",
)

var sirenSpr = spr(
	"..........KKKK..........",
	".........KFFFFK.........",
	"..KKK...KPPPPPPK...KKK..",
	".KPPPKKKPPWPPPPPKKKPPPK.",
	"KPPPPPPPPKKKKKKPPPPPPPPK",
	"KSSPPPPPKEEEEEEKPPPPPSSK",
	".KSSSPPPPKKKKKKPPPPSSSK.",
	"..KKSSSPPPPPPPPPPSSSKK..",
	"....KKKSSSSSSSSSSKKK....",
	".......KKPPPPPPKK.......",
	".........KSSSSK.........",
	"..........KFFK..........",
	"...........KK...........",
)
