package remote

import (
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

// Watcher follows one session. Every method may be called under the
// session's read lock and must not block: the watcher only records that
// something happened and builds its frame later, from its own goroutine.
type Watcher interface {
	Changed() // the screen may have changed (output, resize, scroll of the screen)
	Exited()
	Size() (cols, rows int) // the size this watcher wants when it owns the session
}

type SessionInfo struct {
	Key    string     `json:"key"`
	Name   string     `json:"name"`
	Cwd    string     `json:"cwd"`
	Garage bool       `json:"garage"`
	Agent  *AgentInfo `json:"agent,omitempty"`
}

type AgentInfo struct {
	State string    `json:"state"`
	Kind  string    `json:"kind"`
	Msg   string    `json:"msg"`
	Since time.Time `json:"since"`
}

// Palette is the window's theme (server.Colors), or the default theme's.
type Palette struct {
	FG, BG, Cursor [3]uint8
	P              [256][3]uint8
}

type Host interface {
	Sessions() []SessionInfo
	Watch(key string, w Watcher) (unwatch func(), ok bool)
	Terminal(key string) *vt.Terminal      // read under its Lock
	Input(key string, w Watcher, b []byte) // w becomes the owner (Task 1 rule)
	Take(key string, w Watcher)            // w becomes the owner, as Input does, without input
	SetSize(key string, w Watcher)         // w.Size() changed; applied if w owns key
	Palette() Palette
}
