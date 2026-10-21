package vt_test

import (
	"fmt"

	"github.com/ThiraSoft/neonmux/vt"
)

func Example() {
	term := vt.New(20, 3, 100)
	// A program asking for the cursor position (DSR 6) gets its answer here.
	term.SetReply(func(b []byte) { fmt.Printf("reply %q\n", b) })

	term.Write([]byte("hello \x1b[1;31mworld\x1b[0m\r\nsecond line\x1b[6n"))

	for _, line := range term.ScreenText() {
		fmt.Printf("%q\n", line)
	}

	term.Lock()
	c := term.ScreenLine(0)[6]
	term.Unlock()
	fmt.Println(string(c.Rune()), c.Attr()&vt.AttrBold != 0)
	// Output:
	// reply "\x1b[2;12R"
	// "hello world"
	// "second line"
	// ""
	// w true
}
