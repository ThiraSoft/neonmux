package remote

// Watcher follows one session. Every method may be called under the
// session's read lock and must not block: the watcher only records that
// something happened and builds its frame later, from its own goroutine.
type Watcher interface {
	Changed()               // the screen may have changed (output, resize, scroll of the screen)
	Exited()
	Size() (cols, rows int) // the size this watcher wants when it owns the session
}
