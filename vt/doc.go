// Package vt is an xterm-compatible terminal emulator. It turns the output
// of a program, escape sequences included, into a grid of cells, with
// scrollback, the alternate screen and the usual DEC and xterm modes.
//
// It does no I/O and no drawing: feed it the bytes read from a PTY with
// Write, then read the screen back with ScreenLine, ViewLine or ScreenText.
// Its only dependency outside the standard library is go-runewidth. It is
// the emulator behind neonmux.
//
// # Concurrency
//
// Write is meant to be called from the goroutine that reads the PTY. It
// takes the terminal lock in small chunks, so that a renderer can read in
// between: wrap reads with Lock and Unlock (the methods that need it say
// "Lock required"). Concurrent calls to Write must be serialized by the
// caller. The replies the terminal owes the program (cursor position,
// device attributes, colors) go to the function set with SetReply.
//
// # Memory
//
// A cell fits in 12 bytes (the rune and the attributes share a uint32) and
// the history is stored compressed as runs of the same style, so a long
// scrollback stays cheap.
//
// # Snapshots
//
// Snapshot serializes the whole state, down to an escape sequence cut in
// the middle, and LoadSnapshot restores it. neonmux uses it to hand a
// running session from its daemon to a window, and to keep terminals
// across a reboot.
//
// # Program status
//
// The terminal understands OSC 7501, the Program Status Protocol spoken by
// Claude Code, and reports it through ProgramStatus.
//
// # Limitations
//
// Lines are not reflowed on resize. A combining mark is only kept when it
// composes with the previous character into a single one. There is no
// graphics protocol (sixel, kitty) and no kitty keyboard protocol.
package vt
