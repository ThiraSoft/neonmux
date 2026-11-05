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
// A cell fits in 16 bytes (the rune and the attributes share a uint32, and
// the foreground, background and underline colors take 4 bytes each; see
// [Cell.UL] and [MakeCellUL], with underline color set via SGR 58 and reset
// via SGR 59). The history is stored compressed as runs of the same style,
// so a long scrollback stays cheap.
//
// # Snapshots
//
// Snapshot serializes the whole state, down to an escape sequence cut in
// the middle, and LoadSnapshot restores it. Snapshots carry images and
// their placements. When a snapshot exceeds size limits, [DropGraphics]
// strips the trailing graphics section to keep the text history intact.
// neonmux uses snapshots to hand a running session from its daemon to a
// window, and to keep terminals across a reboot.
//
// # Program status
//
// The terminal understands OSC 7501, the Program Status Protocol spoken by
// Claude Code, and reports it through ProgramStatus.
//
// # Graphics
//
// The terminal supports transmission and query commands of the kitty graphics
// protocol (direct PNG, RGB and RGBA payloads, optional zlib decompression,
// chunked transfers, and responses). Received images are stored as [Image]
// records inspected through [Terminal.Image] (Lock required). A sequence counter,
// [Terminal.GraphicsSeq], changes whenever images or placements change,
// allowing renderers to invalidate caches. [Terminal.PixelSize] reports
// the dimensions in pixels set via [Terminal.SetPixelSize] (thread-safe).
//
// Images are placed at the cursor (a=p, or a=T with the transmission),
// with a size in cells (c, r), a source rectangle (x, y, w, h), an offset
// in the first cell (X, Y), a z-index and placement ids (p). The cursor
// then moves past the image unless C=1. A placement sticks to its line:
// it scrolls with the text into the history and is dropped when that line
// leaves the history. Only full screen scrolls move placements; a scroll
// region that is not the whole screen leaves them in place. The main and
// alternate screens have their own placements. On a resize, a placement
// on the main screen follows its character through the rewrap; one off
// the alternate screen is dropped. Deletion (a=d) supports the a, i, n, r,
// c, p, q, x, y and z targets, an uppercase target also freeing the
// images left unplaced; ED 2, ED 3 and RIS erase the placements they
// cover. An image sent with a=T is freed with its last placement once
// that one scrolls out, is erased or leaves the screen.
// A terminal keeps at most 4096 placements, dropping the oldest first.
// [Terminal.VisiblePlacements] lists the direct placements to draw (Lock
// required). [Terminal.PlacementNatural] reports whether a direct placement
// was created without explicit cell dimensions (Lock required). Virtual
// placements (U=1) are not listed there: they show
// through Unicode placeholders, cells where the program printed U+10EEEE
// with the image id in the foreground color, the placement id in the
// underline color (SGR 58) and up to three diacritics for the row, the
// column and the high byte of the image id. A missing row or column
// follows the placeholder on the left. [Cell.Placeholder] decodes such a
// cell (its Rune is not a real character, and ScreenText shows a space),
// [Terminal.VirtualPlacement] gives the size in cells of the placement it
// refers to (Lock required), and [PlaceholderSlice] the part of the image
// the cell draws, fitted and centered in that size as kitty does.
//
// # Limitations
//
// Only the main screen and its history are reflowed on resize. A
// combining mark is only kept when it composes with the previous
// character into a single one. There is no sixel protocol and no animation.
// The kitty keyboard protocol only tracks flag 1 (disambiguate escape codes).
// Placements keep their z but renderers draw every image above the text
// (z < 0 is not honored), only full-screen scrolls move images (scroll regions
// do not), and text written over an image does not erase it.
package vt
