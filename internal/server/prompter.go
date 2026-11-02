package server

import (
	"encoding/json"
	"time"

	"github.com/ThiraSoft/neonmux/internal/prompt"
	"github.com/ThiraSoft/neonmux/vt"
)

// The prompter: the daemon follows every session (garage included), turns
// the end of an agent's turn into a track and keeps one queue per device:
// "window" for the window, the device id for a phone.

const windowDevice = "window"

var defaultPromptOpts = prompt.Opts{SkipCode: true, ShortPaths: true, StripMarkdown: true, Pauses: true, Context: 3}

func (s *Server) initPrompt(o prompt.Opts, intermediate bool) {
	s.hub = prompt.NewHub(o)
	s.det = prompt.NewDetector(s.hub, prompt.DefaultReaders(), intermediate)
}

// promptSource shows a session to the detector.
type promptSource struct{ se *sess }

func (p promptSource) Key() string                   { return p.se.key }
func (p promptSource) Status() (vt.ProgStatus, bool) { return p.se.s.Term.ProgramStatus() }
func (p promptSource) Pgid() int                     { return p.se.s.ForegroundPgid() }
func (p promptSource) Cwd() string {
	if d := p.se.s.ForegroundCwd(); d != "" {
		return d
	}
	return p.se.s.Cwd()
}

// Agent is the foreground program seen by the last refresh.
func (p promptSource) Agent() string {
	p.se.metaMu.Lock()
	defer p.se.metaMu.Unlock()
	return p.se.lastFg
}

// tickPrompt runs once per loop, after refresh.
func (s *Server) tickPrompt(list []*sess) {
	srcs := make([]prompt.Source, len(list))
	for i, se := range list {
		srcs[i] = promptSource{se}
	}
	s.det.Tick(srcs, time.Now())
}

// promptFromWindow obeys a window that speaks protocol 4 or more.
func (s *Server) promptFromWindow(c *conn, data []byte) {
	if c.proto.Load() < 4 {
		return
	}
	var cmd PromptCmd
	if json.Unmarshal(data, &cmd) != nil {
		return
	}
	switch cmd.Op {
	case "open":
		s.mu.Lock()
		if c.promptCancel == nil {
			c.promptCancel = s.hub.Watch(windowDevice, func() { s.sendPromptQueue(c) })
		}
		s.mu.Unlock()
		s.hub.Open(windowDevice) // notifies the watch: the window gets its queue
	case "close":
		s.hub.Close(windowDevice)
	case "dismiss":
		s.hub.Dismiss(windowDevice, cmd.Seq)
	}
}

func (s *Server) sendPromptQueue(c *conn) {
	c.sendJSON(MsgPromptQueue, "", s.hub.Queue(windowDevice))
}
