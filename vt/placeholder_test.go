package vt

import (
	"image"
	"testing"
)

// ph is U+10EEEE, the placeholder character, in UTF-8.
const ph = "\U0010EEEE"

// mark returns the diacritic that stands for n.
func mark(n int) string { return string(rowColumnDiacritics[n]) }

type phCell struct {
	image, placement uint32
	row, col         int
	ok               bool
}

func placeholderAt(term *Terminal, x, y int) phCell {
	term.Lock()
	defer term.Unlock()
	var p phCell
	p.image, p.placement, p.row, p.col, p.ok = term.ScreenLine(y)[x].Placeholder()
	return p
}

func TestPlaceholderDiacriticsTable(t *testing.T) {
	if len(rowColumnDiacritics) != 297 {
		t.Fatalf("got %d diacritics, want 297", len(rowColumnDiacritics))
	}
	for i, r := range rowColumnDiacritics {
		if i > 0 && r <= rowColumnDiacritics[i-1] {
			t.Fatalf("table not sorted at %d", i)
		}
	}
	// Every diacritic composes with a placeholder, even those go-runewidth
	// gives a width of 1 (U+0592 and others).
	for i := range rowColumnDiacritics {
		term := New(4, 1, 0)
		feed(term, "\x1b[38;5;1m"+ph+mark(i)+mark(i))
		if got, want := placeholderAt(term, 0, 0), (phCell{1, 0, i, i, true}); got != want {
			t.Fatalf("diacritic %d (%U): got %+v, want %+v", i, rowColumnDiacritics[i], got, want)
		}
		if x, _ := cursorPos(term); x != 1 {
			t.Fatalf("diacritic %d (%U) moved the cursor to %d", i, rowColumnDiacritics[i], x)
		}
	}
	// Away from a placeholder, such a mark still prints as before.
	term := New(4, 1, 0)
	feed(term, "a\u0592")
	if got := lineText(term, 0); got != "a\u0592" {
		t.Fatalf("got %q", got)
	}
}

func TestPlaceholderDiacritics(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "\x1b[38;2;0;0;42m"+ph+"̅̍")
	if got, want := placeholderAt(term, 0, 0), (phCell{42, 0, 0, 1, true}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if x, _ := cursorPos(term); x != 1 {
		t.Fatalf("cursor at %d, want 1", x)
	}
}

func TestPlaceholderInference(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "\x1b[38;5;42m"+ph+mark(2)+mark(0)+ph+ph)
	for x := 0; x < 3; x++ {
		if got, want := placeholderAt(term, x, 0), (phCell{42, 0, 2, x, true}); got != want {
			t.Fatalf("cell %d: got %+v, want %+v", x, got, want)
		}
	}

	// A row alone keeps the column from the left when the row is the same,
	// restarts at 0 otherwise.
	term = New(10, 3, 10)
	feed(term, "\x1b[38;5;42m"+ph+mark(2)+mark(4)+ph+mark(2)+ph+mark(3))
	for x, want := range []phCell{{42, 0, 2, 4, true}, {42, 0, 2, 5, true}, {42, 0, 3, 0, true}} {
		if got := placeholderAt(term, x, 0); got != want {
			t.Fatalf("cell %d: got %+v, want %+v", x, got, want)
		}
	}

	// Another image on the left: nothing is inferred.
	term = New(10, 3, 10)
	feed(term, "\x1b[38;5;42m"+ph+mark(2)+mark(4)+"\x1b[38;5;43m"+ph)
	if got, want := placeholderAt(term, 1, 0), (phCell{43, 0, 0, 0, true}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPlaceholderIDHighByte(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "\x1b[38;2;0;0;42m"+ph+mark(0)+mark(0)+mark(3)+ph)
	for x := 0; x < 2; x++ {
		if got, want := placeholderAt(term, x, 0), (phCell{3<<24 | 42, 0, 0, x, true}); got != want {
			t.Fatalf("cell %d: got %+v, want %+v", x, got, want)
		}
	}
}

func TestPlaceholderExtraMarksIgnored(t *testing.T) {
	term := New(10, 3, 10)
	// A fourth mark, a mark out of the table and a high byte past 255 are
	// all ignored.
	feed(term, "\x1b[38;5;42m"+ph+mark(1)+"́"+mark(2)+mark(256)+mark(5)+mark(9))
	if got, want := placeholderAt(term, 0, 0), (phCell{5<<24 | 42, 0, 1, 2, true}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPlaceholderPlacementFromUnderline(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "\x1b[38;5;42m\x1b[58;2;0;0;7m"+ph+mark(0)+mark(0)+"\x1b[58;5;9m"+ph)
	if got, want := placeholderAt(term, 0, 0), (phCell{42, 7, 0, 0, true}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// Another placement on the left: nothing is inferred.
	if got, want := placeholderAt(term, 1, 0), (phCell{42, 9, 0, 0, true}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPlaceholderRepeat(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "\x1b[38;5;42m"+ph+mark(1)+mark(0)+"\x1b[2b")
	for x := 0; x < 3; x++ {
		if got, want := placeholderAt(term, x, 0), (phCell{42, 0, 1, x, true}); got != want {
			t.Fatalf("cell %d: got %+v, want %+v", x, got, want)
		}
	}
}

func TestPlaceholderNotOnOtherCells(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "éa")
	if got := placeholderAt(term, 0, 0); got.ok {
		t.Fatalf("plain cell read as a placeholder: %+v", got)
	}
	if got := lineText(term, 0); got != "éa" {
		t.Fatalf("got %q, want %q", got, "éa")
	}
}

func TestPlaceholderTextIsBlank(t *testing.T) {
	term := New(10, 3, 10)
	feed(term, "a\x1b[38;5;42m"+ph+mark(1)+mark(1)+ph+"\x1b[0mb")
	if got := term.ScreenText()[0]; got != "a  b" {
		t.Fatalf("got %q, want %q", got, "a  b")
	}
}

func TestPlaceholderInScrollback(t *testing.T) {
	term := New(10, 2, 10)
	feed(term, "\x1b[38;2;1;2;3m\x1b[58;5;7m"+ph+mark(5)+mark(6)+mark(8)+ph+"\x1b[0m\r\n\n\n")
	term.Lock()
	defer term.Unlock()
	if term.HistoryLen() == 0 {
		t.Fatal("nothing in the history")
	}
	first, _ := term.AbsBase()
	line, _ := term.AbsLine(first, nil)
	if len(line) < 2 {
		t.Fatalf("history line has %d cells", len(line))
	}
	for x, want := range []phCell{{8<<24 | 0x010203, 7, 5, 6, true}, {8<<24 | 0x010203, 7, 5, 7, true}} {
		var got phCell
		got.image, got.placement, got.row, got.col, got.ok = line[x].Placeholder()
		if got != want {
			t.Fatalf("cell %d: got %+v, want %+v", x, got, want)
		}
	}
}

func TestVirtualPlacement(t *testing.T) {
	term := New(20, 10, 100)
	feed(term, rgbaCmd("a=t,i=42,q=2", 4, 4))
	feed(term, "\x1b_Ga=p,U=1,i=42,c=10,r=5,q=2\x1b\\")
	term.Lock()
	cols, rows, ok := term.VirtualPlacement(42, 0)
	_, _, other := term.VirtualPlacement(43, 0)
	term.Unlock()
	if cols != 10 || rows != 5 || !ok || other {
		t.Fatalf("got (%d, %d, %v), other image %v", cols, rows, ok, other)
	}
	if ps := visiblePlacements(term, 0); len(ps) != 0 {
		t.Fatalf("virtual placement listed as direct: %+v", ps)
	}

	// By id, and the newest one when no id is given.
	feed(term, "\x1b_Ga=p,U=1,i=42,p=3,c=2,r=1,q=2\x1b\\\x1b_Ga=p,U=1,i=42,p=4,c=6,r=3,q=2\x1b\\")
	term.Lock()
	defer term.Unlock()
	for _, c := range []struct {
		id         uint32
		cols, rows int
		ok         bool
	}{{0, 6, 3, true}, {3, 2, 1, true}, {4, 6, 3, true}, {5, 0, 0, false}} {
		cols, rows, ok := term.VirtualPlacement(42, c.id)
		if cols != c.cols || rows != c.rows || ok != c.ok {
			t.Fatalf("placement %d: got (%d, %d, %v), want (%d, %d, %v)", c.id, cols, rows, ok, c.cols, c.rows, c.ok)
		}
	}
}

func TestPlaceholderSlice(t *testing.T) {
	r := image.Rect
	for _, c := range []struct {
		name                   string
		imgW, imgH, cols, rows int
		row, col               int
		src, dst               image.Rectangle
	}{
		// 8×2 cells of 10×20: an 80×40 box.
		// 100×50 in 80×40: scaled to 80×40, 1.25 image pixel per box pixel.
		{"wide top left", 100, 50, 8, 2, 0, 0, r(0, 0, 12, 25), r(0, 0, 10, 20)},
		{"wide second", 100, 50, 8, 2, 0, 1, r(12, 0, 25, 25), r(0, 0, 10, 20)},
		{"wide bottom right", 100, 50, 8, 2, 1, 7, r(87, 25, 100, 50), r(0, 0, 10, 20)},
		{"wide out of the box", 100, 50, 8, 2, 2, 0, image.Rectangle{}, image.Rectangle{}},
		// 20×80 in 80×40: scaled to 10×40, centered from x=35 to x=45.
		{"tall left empty", 20, 80, 8, 2, 0, 0, image.Rectangle{}, image.Rectangle{}},
		{"tall left half", 20, 80, 8, 2, 0, 3, r(0, 0, 10, 40), r(5, 0, 10, 20)},
		{"tall right half", 20, 80, 8, 2, 1, 4, r(10, 40, 20, 80), r(0, 0, 5, 20)},
		// 200×20 in 80×40: scaled to 80×8, centered from y=16 to y=24.
		{"flat top", 200, 20, 8, 2, 0, 1, r(25, 0, 50, 10), r(0, 16, 10, 20)},
		{"flat bottom", 200, 20, 8, 2, 1, 1, r(25, 10, 50, 20), r(0, 0, 10, 4)},
		// 2×2 in 80×40: scaled up to 40×40, a cell sees less than a pixel
		// but at least one.
		{"tiny", 2, 2, 8, 2, 0, 2, r(0, 0, 1, 1), r(0, 0, 10, 20)},
		{"no image", 0, 50, 8, 2, 0, 0, image.Rectangle{}, image.Rectangle{}},
	} {
		src, dst := PlaceholderSlice(c.imgW, c.imgH, c.cols, c.rows, 10, 20, c.row, c.col)
		if src != c.src || dst != c.dst {
			t.Errorf("%s: got %v %v, want %v %v", c.name, src, dst, c.src, c.dst)
		}
	}
}
