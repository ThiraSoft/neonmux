package kata

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
)

// Chapter 6, Numbers and counts: a count before a command, ctrl+a and
// ctrl+x on numbers.

func init() {
	register(
		Def{ID: "count.delete-words", Name: "Delete these three words", Gesture: "d3w", Chapter: 6, Gen: genCountDeleteWords},
		Def{ID: "count.delete-lines", Name: "Delete these three lines", Gesture: "3dd", Chapter: 6, Gen: genCountDeleteLines},
		Def{ID: "count.join-lines", Name: "Join these four lines", Gesture: "4J", Chapter: 6, Gen: genCountJoinLines},
		Def{ID: "count.add", Name: "Add to a number", Gesture: "<C-a>", Chapter: 6, Gen: genCountAdd},
		Def{ID: "count.subtract", Name: "Subtract from a number", Gesture: "<C-x>", Chapter: 6, Gen: genCountSubtract},
		Def{ID: "count.shift-years", Name: "Add 10 to every year", Gesture: "10<C-a>", Chapter: 6, Gen: genCountShiftYears},
	)
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// number is a run of digits that ctrl+a reads as a plain positive decimal.
type number struct {
	l, a, b int
	val     int
}

// numbers lists the numbers that ctrl+a treats simply: 1 to 4 digits, no
// leading zero, not after a letter, a "-" (vim would read it as a minus
// sign) or a ".", and not followed by a "." and a digit. ok is false when
// a 4-digit number is left out (1958 in 1956-1958): a mission about every
// year would not be true.
func numbers(lines []string) (out []number, ok bool) {
	ok = true
	for l, s := range lines {
		for i := 0; i < len(s); {
			if !isDigit(s[i]) {
				i++
				continue
			}
			j := i
			for j < len(s) && isDigit(s[j]) {
				j++
			}
			prev := byte(' ')
			if i > 0 {
				prev = s[i-1]
			}
			good := j-i <= 4 && s[i] != '0' && !isKeyword(prev) && prev != '-' && prev != '.' &&
				!(j+1 < len(s) && s[j] == '.' && isDigit(s[j+1]))
			if good {
				v, _ := strconv.Atoi(s[i:j])
				out = append(out, number{l, i, j, v})
			} else if j-i == 4 {
				ok = false
			}
			i = j
		}
	}
	return out, ok
}

// numberRegion is where ctrl+a picks n: on n, or before it on its line
// with no other digit in between.
func numberRegion(lines []string, n number) func(Pos) bool {
	return func(p Pos) bool {
		if p.Line != n.l || p.Col >= n.b {
			return false
		}
		if p.Col >= n.a {
			return true
		}
		return !strings.ContainsAny(lines[n.l][p.Col:n.a], "0123456789")
	}
}

// wholeNumber reports a number that cw changes exactly.
func wholeNumber(s string, n number) bool {
	return (n.a == 0 || !isKeyword(s[n.a-1])) && (n.b == len(s) || !isKeyword(s[n.b]))
}

func genCountDeleteWords(t Text, r *rand.Rand) (Mission, bool) {
	type run struct{ l, a, b int }
	var runs []run
	for l, s := range t.Lines {
		ws := wordSpans(s)
		for i := 0; i+3 < len(ws); i++ {
			// Three words separated by one space, then a space and a word.
			if ws[i+1][0] == ws[i][1]+1 && ws[i+2][0] == ws[i+1][1]+1 && ws[i+3][0] == ws[i+2][1]+1 &&
				s[ws[i][1]] == ' ' && s[ws[i+1][1]] == ' ' && s[ws[i+2][1]] == ' ' &&
				(ws[i][0] == 0 || s[ws[i][0]-1] == ' ') {
				runs = append(runs, run{l, ws[i][0], ws[i+3][0]})
			}
		}
	}
	if len(runs) == 0 {
		return Mission{}, false
	}
	w := pick(r, runs)
	target := edit(t.Lines, w.l, w.a, w.b, "")
	phrase := strings.TrimSpace(t.Lines[w.l][w.a:w.b])
	return editing(t.Lines, target, fmt.Sprintf("Delete %q.", phrase), &Span{w.l, w.a, w.b - 1},
		navThen(t.Lines, Pos{0, 0}, at(w.l, w.a), "d3w", "delete three words"),
		navThen(t.Lines, Pos{0, 0}, at(w.l, w.a), "3dw", "three times: delete a word"))
}

func genCountDeleteLines(t Text, r *rand.Rand) (Mission, bool) {
	var ks []int
	for _, p := range paragraphs(t.Lines) {
		for k := max(p[0], 1); k+3 <= p[1]; k++ {
			ks = append(ks, k)
		}
	}
	if len(ks) == 0 {
		return Mission{}, false
	}
	k := pick(r, ks)
	target := slices.Concat(t.Lines[:k], t.Lines[k+3:])
	return editing(t.Lines, target, fmt.Sprintf("Delete lines %d to %d.", k+1, k+3), &Span{k, 0, len(t.Lines[k])},
		navThen(t.Lines, Pos{0, 0}, onLines(k, k+1), "3dd", "delete three lines"),
		Recipe{{fmt.Sprintf(":%d,%dd<CR>", k+1, k+3), "delete a range of lines"}})
}

func genCountJoinLines(t Text, r *rand.Rand) (Mission, bool) {
	var ks []int
	for _, k := range blocks(t.Lines) {
		ok := true
		for i := k + 1; i < k+4; i++ {
			if strings.HasPrefix(t.Lines[i], ")") {
				ok = false
			}
		}
		if ok {
			ks = append(ks, k)
		}
	}
	if len(ks) == 0 {
		return Mission{}, false
	}
	k := pick(r, ks)
	target := slices.Concat(t.Lines[:k], []string{strings.Join(t.Lines[k:k+4], " ")}, t.Lines[k+4:])
	return editing(t.Lines, target, fmt.Sprintf("Join lines %d to %d into one.", k+1, k+4), &Span{k, 0, len(t.Lines[k])},
		navThen(t.Lines, Pos{0, 0}, onLines(k, k+1), "4J", "join four lines"))
}

func uniqueNumbers(t Text) []number {
	ns, _ := numbers(t.Lines)
	return slices.DeleteFunc(slices.Clone(ns), func(n number) bool {
		c := 0
		for _, m := range ns {
			if m.val == n.val {
				c++
			}
		}
		return c > 1
	})
}

func genCountAdd(t Text, r *rand.Rand) (Mission, bool) {
	return genCountChange(t, r, 1)
}

func genCountSubtract(t Text, r *rand.Rand) (Mission, bool) {
	return genCountChange(t, r, -1)
}

func genCountChange(t Text, r *rand.Rand, sign int) (Mission, bool) {
	ns := uniqueNumbers(t)
	if sign < 0 {
		ns = slices.DeleteFunc(ns, func(n number) bool { return n.val < 2 })
	}
	if len(ns) == 0 {
		return Mission{}, false
	}
	n := pick(r, ns)
	d := 2 + r.IntN(19)
	key, verb, instr := "<C-a>", "add the count to the number", "Add %d to %d."
	if sign < 0 {
		d = min(d, n.val)
		key, verb, instr = "<C-x>", "subtract the count from the number", "Subtract %d from %d."
	}
	val := strconv.Itoa(n.val + sign*d)
	target := edit(t.Lines, n.l, n.a, n.b, val)
	cands := []Recipe{navThen(t.Lines, Pos{0, 0}, numberRegion(t.Lines, n), strconv.Itoa(d)+key, verb+" under or after the cursor")}
	if wholeNumber(t.Lines[n.l], n) {
		cands = append(cands, navThen(t.Lines, Pos{0, 0}, at(n.l, n.a), "cw"+val+"<Esc>", "change the number and type the result"))
	}
	return editing(t.Lines, target, fmt.Sprintf(instr, d, n.val), &Span{n.l, n.a, n.b}, cands...)
}

func genCountShiftYears(t Text, r *rand.Rand) (Mission, bool) {
	ns, ok := numbers(t.Lines)
	if !ok {
		return Mission{}, false
	}
	var years []number
	for _, n := range ns {
		if n.b-n.a == 4 && n.val >= 1000 && n.val <= 2089 {
			years = append(years, n)
		} else if n.b-n.a == 4 {
			return Mission{}, false // a year that would not be shifted
		}
	}
	if len(years) < 2 || len(years) > 4 {
		return Mission{}, false
	}
	buf := slices.Clone(t.Lines)
	from := Pos{0, 0}
	var recipe Recipe
	for _, y := range years {
		nav, ok := Shortest(buf, from, numberRegion(buf, y))
		if !ok {
			return Mission{}, false
		}
		recipe = append(recipe, nav...)
		recipe = append(recipe, Step{"10<C-a>", "add 10 to the number under or after the cursor"})
		buf = edit(buf, y.l, y.a, y.b, strconv.Itoa(y.val+10))
		from = Pos{y.l, y.b - 1} // ctrl+a leaves the cursor on the last digit
	}
	return editing(t.Lines, buf, "Add 10 to every year.", nil, recipe)
}
