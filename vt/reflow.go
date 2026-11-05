package vt

import "math"

// Reflow: when the main screen changes width, the lines broken by an
// automatic wrap are joined back into logical lines, then cut again at the
// new width. The history goes through the same treatment, so a long log
// line stays readable after narrowing then widening a pane. Lines ended by
// a real line feed are never joined.

// joinRow appends a physical row to a logical line. A wrapped row ending
// in a blank cell followed by a wide character is the leftover of a wide
// character that did not fit at the end of the row: that cell is dropped.
func joinRow(line, cells []Cell) []Cell {
	if n := len(line); n > 0 && len(cells) > 0 && cells[0].Attr()&AttrWide != 0 && line[n-1].IsBlank() {
		line = line[:n-1]
	}
	return append(line, cells...)
}

func trimBlank(cells []Cell) []Cell {
	for len(cells) > 0 && cells[len(cells)-1].IsBlank() {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// splitLine cuts cells into rows of cols cells, without ever splitting a
// wide character: one that would straddle the edge goes to the next row.
// emit receives each row (only valid during the call), whether the line
// continues on the next one, and the range [start, start+n) of cells it
// holds.
func splitLine(cells []Cell, cols int, row []Cell, emit func(row []Cell, wrapped bool, start, n int)) {
	start := 0
	for {
		n := min(cols, len(cells)-start)
		if n == cols && start+n < len(cells) && cells[start+n-1].Attr()&AttrWide != 0 {
			n--
		}
		copy(row, cells[start:start+n])
		clear(row[n:])
		emit(row, start+n < len(cells), start, n)
		start += n
		if start >= len(cells) {
			return
		}
	}
}

// reflowMain rewraps the main screen and the history at cols columns and
// returns the new main grid. cur is the main screen cursor (the saved one
// while the alternate screen is active): it stays on the same character.
// Empty rows below the cursor are dropped. The direct placements of the
// main screen stay on their character too.
func (t *Terminal) reflowMain(cols, rows int, cur *cursor) *grid {
	old, oldSB := t.main, t.sb
	sb := newScrollback(oldSB.max)
	row := make([]Cell, cols)
	var line, buf []Cell

	// While rows are produced, a placement's abs holds the index of its
	// new row among all of them; produced counts the history rows. The
	// anchors are made absolute again at the end.
	oldFirst := oldSB.total - int64(oldSB.n)
	anchors := t.mainAnchors()
	var produced int64
	type lineAnchor struct {
		p   *placement
		off int // in the logical line
	}
	var pending []lineAnchor
	var settled []*placement // in the current logical line
	addAnchors := func(abs int64, rowLen int) {
		for _, p := range anchors[abs] {
			pending = append(pending, lineAnchor{p, len(line) - rowLen + p.Col})
		}
	}
	// settle gives a row to the pending anchors it holds, and to all the
	// rest on the last row of the line.
	settle := func(idx int64, start, n int, last bool) {
		kept := pending[:0]
		for _, a := range pending {
			if last || a.off >= start && a.off < start+n {
				a.p.abs, a.p.Col = idx, min(max(a.off-start, 0), cols-1)
				settled = append(settled, a.p)
			} else {
				kept = append(kept, a)
			}
		}
		pending = kept
	}

	// The last rows of the history wait in tail, encoded, before going
	// in: when the text gets shorter, some come back down on the screen so
	// that the cursor keeps its row.
	tail := make([][]byte, 0, rows)
	toHistory := func(enc []byte) {
		produced++
		if len(tail) < rows {
			tail = append(tail, append([]byte(nil), enc...))
			return
		}
		sb.pushEncoded(tail[0])
		recycled := tail[0]
		copy(tail, tail[1:])
		tail[rows-1] = append(recycled[:0], enc...)
	}
	var enc []byte

	// The history first. A logical line still open at its end continues
	// on the screen and is finished there. The encoding drops trailing
	// blanks, but a wrapped line filled the whole width (every line of the
	// history is at the current width, since each resize rewraps it): its
	// blanks are given back, or "hello " + "world" would join as
	// "helloworld".
	for i := 0; i < oldSB.n; i++ {
		raw := oldSB.ring[(oldSB.start+i)%len(oldSB.ring)]
		// Most lines are short and whole: they keep their encoding as is.
		if wrapped, n, _, _, err := lineHeader(raw, math.MaxInt); len(line) == 0 && !wrapped && n <= cols && err == nil {
			for _, p := range anchors[oldFirst+int64(i)] {
				p.abs, p.Col = produced, min(p.Col, cols-1)
			}
			toHistory(raw)
			continue
		}
		var wrapped bool
		buf, wrapped = oldSB.line(i, buf)
		for wrapped && len(buf) < t.cols {
			buf = append(buf, Cell{})
		}
		line = joinRow(line, buf)
		addAnchors(oldFirst+int64(i), len(buf))
		if !wrapped {
			splitLine(trimBlank(line), cols, row, func(r []Cell, w bool, start, n int) {
				settle(produced, start, n, !w)
				enc = encodeLine(enc[:0], r, w)
				toHistory(enc)
			})
			line, settled = line[:0], settled[:0]
		}
	}

	last := cur.y
	for y := len(old.lines) - 1; y > cur.y; y-- {
		if !lineEmpty(old.lines[y]) {
			last = y
			break
		}
	}

	type screenRow struct {
		cells   []Cell
		wrapped bool
	}
	out := make([]screenRow, 0, len(tail)+rows)
	for _, b := range tail {
		cells, w, _ := decodeLine(b, nil, math.MaxInt)
		out = append(out, screenRow{append(cells, make([]Cell, max(0, cols-len(cells)))...), w})
	}
	inHistory := len(out)
	// The index of out[k] among all the rows produced.
	index := func(k int) int64 { return produced - int64(inHistory) + int64(k) }
	push := func(r []Cell, w bool, start, n int) {
		settle(index(len(out)), start, n, !w)
		out = append(out, screenRow{append([]Cell(nil), r...), w})
	}
	curRow, curX := 0, 0
	off := -1 // the cursor in the current logical line
	for y := 0; y <= last; y++ {
		line = joinRow(line, old.lines[y])
		addAnchors(oldSB.total+int64(y), len(old.lines[y]))
		if y == cur.y {
			off = len(line) - len(old.lines[y]) + cur.x
		}
		if old.wrapped[y] && y < last {
			continue
		}
		cells := trimBlank(line)
		if off < 0 {
			splitLine(cells, cols, row, push)
			line, settled = line[:0], settled[:0]
			continue
		}
		// The cursor needs a cell to sit on, even past the end of the text.
		pastEnd := off >= len(cells) && !cur.wrapNext
		for len(cells) <= off {
			if len(cells) < len(line) {
				cells = line[:len(cells)+1]
			} else {
				cells = append(cells, Cell{})
			}
		}
		first := len(out)
		splitLine(cells, cols, row, func(r []Cell, w bool, start, n int) {
			push(r, w, start, n)
			if off >= start && off < start+n {
				curRow, curX = len(out)-1, off-start
			}
		})
		switch {
		case cur.wrapNext && curX < cols-1:
			// The pending wrap is gone: the next character goes right after.
			curX++
			cur.wrapNext = false
		case pastEnd && curX == 0 && curRow > first && off == len(trimBlank(cells)):
			// Just past a line that fills its last row: stay at the end of
			// that row with a pending wrap, as if it had just been typed.
			out = out[:curRow]
			curRow, curX = curRow-1, cols-1
			out[curRow].wrapped = false
			cur.wrapNext = true
			// A placement on the dropped row goes with the cursor.
			for _, p := range settled {
				if p.abs == index(curRow+1) {
					p.abs, p.Col = index(curRow), cols-1
				}
			}
		}
		line, off, settled = line[:0], -1, settled[:0]
	}
	// Placements on the dropped empty rows keep their distance to the text.
	for y := last + 1; y < len(old.lines); y++ {
		for _, p := range anchors[oldSB.total+int64(y)] {
			p.abs, p.Col = index(len(out)-1+y-last), min(p.Col, cols-1)
		}
	}
	for _, ps := range anchors {
		for _, p := range ps {
			p.abs += oldFirst
		}
	}

	// The screen starts after the history, a little earlier if the cursor
	// would otherwise climb (rows taken back from the history), later if
	// the text no longer fits. The cursor's row stays on the screen.
	start := max(min(inHistory, curRow-min(cur.y, rows-1)), len(out)-rows)
	start = max(0, min(start, curRow))
	for _, r := range out[:start] {
		sb.push(r.cells, r.wrapped)
	}
	g := newGrid(cols, rows)
	for y := 0; y < rows && start+y < len(out); y++ {
		copy(g.lines[y], out[start+y].cells)
		g.wrapped[y] = out[start+y].wrapped
	}
	cur.x, cur.y = curX, curRow-start

	// The oldest line keeps its absolute index (AbsBase): only lines
	// evicted from a full history move it forward.
	sb.total += oldSB.total - int64(oldSB.n)
	t.sb = sb
	return g
}

// mainAnchors returns the direct placements of the main screen by anchor
// line. An anchor outside the history and the screen is left out: it is
// pruned after the resize.
func (t *Terminal) mainAnchors() map[int64][]*placement {
	g := t.gfx
	if g == nil || len(g.placements) == 0 {
		return nil
	}
	lo, hi := t.sb.total-int64(t.sb.n), t.sb.total+int64(len(t.main.lines))
	m := make(map[int64][]*placement)
	for _, p := range g.placements {
		if !p.virtual && !p.alt && p.abs >= lo && p.abs < hi {
			m[p.abs] = append(m[p.abs], p)
		}
	}
	return m
}
