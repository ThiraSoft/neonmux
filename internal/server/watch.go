package server

import (
	"sync"

	"github.com/ThiraSoft/neonmux/internal/remote"
)

// sessionOwner wraps the owner stored in sess.owner so that atomic.Value
// always holds values of the same concrete type.
type sessionOwner struct {
	val any // *conn of the window, remote.Watcher, or nil (meaning window)
}

func (se *sess) getOwner() any {
	v := se.owner.Load()
	if v == nil {
		return nil
	}
	return v.(sessionOwner).val
}

func (se *sess) setOwner(o any) {
	se.owner.Store(sessionOwner{val: o})
}

func (se *sess) isWindowOwner() bool {
	o := se.getOwner()
	if o == nil {
		return true
	}
	_, isConn := o.(*conn)
	return isConn
}

func (se *sess) setGuiSize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	se.watchersMu.Lock()
	se.guiCols = cols
	se.guiRows = rows
	se.watchersMu.Unlock()
}

func (se *sess) getGuiSize() (int, int) {
	se.watchersMu.Lock()
	defer se.watchersMu.Unlock()
	return se.guiCols, se.guiRows
}

func (se *sess) addWatcher(w remote.Watcher) (func(), bool) {
	if w == nil || se.exited.Load() {
		return nil, false
	}
	if se.s != nil && se.s.Exited() {
		return nil, false
	}
	se.watchersMu.Lock()
	if se.exited.Load() {
		se.watchersMu.Unlock()
		return nil, false
	}
	if se.watchers == nil {
		se.watchers = make(map[remote.Watcher]struct{})
	}
	se.watchers[w] = struct{}{}
	se.numWatchers.Store(int32(len(se.watchers)))
	se.watchersMu.Unlock()

	w.Changed()

	var once sync.Once
	unwatch := func() {
		once.Do(func() {
			se.watchersMu.Lock()
			delete(se.watchers, w)
			se.numWatchers.Store(int32(len(se.watchers)))
			se.watchersMu.Unlock()
			if se.getOwner() == w {
				se.setOwner(nil)
			}
		})
	}
	return unwatch, true
}

func (se *sess) notifyChanged() {
	if se.numWatchers.Load() == 0 {
		return
	}
	se.watchersMu.Lock()
	ws := make([]remote.Watcher, 0, len(se.watchers))
	for w := range se.watchers {
		ws = append(ws, w)
	}
	se.watchersMu.Unlock()
	for _, w := range ws {
		w.Changed()
	}
}

func (se *sess) notifyExited() {
	se.exitOnce.Do(func() {
		se.exited.Store(true)
		se.watchersMu.Lock()
		ws := make([]remote.Watcher, 0, len(se.watchers))
		for w := range se.watchers {
			ws = append(ws, w)
		}
		clear(se.watchers)
		se.numWatchers.Store(0)
		se.watchersMu.Unlock()
		if _, isWatcher := se.getOwner().(remote.Watcher); isWatcher {
			se.setOwner(nil)
		}
		for _, w := range ws {
			w.Exited()
		}
	})
}

// addWatcher registers w to follow session key and calls w.Changed() once.
// Returns an unwatch function to deregister w, or ok == false if the session
// does not exist or has already exited.
func (s *Server) addWatcher(key string, w remote.Watcher) (unwatch func(), ok bool) {
	se := s.get(key)
	if se == nil {
		return nil, false
	}
	return se.addWatcher(w)
}

// inputFrom writes b to se.s, then applies the ownership rule:
//   - A watcher becomes the owner of se and resizes it to w.Size() if different.
//   - The window becomes owner of all its attached sessions, resizing any session
//     not at its guiCols/guiRows back to them. Only sessions a phone resized
//     can differ, so the scan is skipped while no session is phone-sized.
//
// The owner change and the resize are not atomic: a phone and the window
// typing at the same instant may leave the session at either size, until
// the next keystroke settles it.
func (s *Server) inputFrom(se *sess, owner any, b []byte) {
	if se == nil {
		return
	}
	se.s.Write(b)

	if w, isWatcher := owner.(remote.Watcher); isWatcher {
		s.takeSize(se, w)
		return
	}
	c, _ := owner.(*conn)
	if c == nil || s.phoneSized.Load() == 0 {
		return
	}
	for _, other := range s.list() {
		if other.att.Load() != c {
			continue
		}
		// Cleared before the resize: a phone taking the session meanwhile
		// marks it again after its own resize, so the scan stays on.
		s.clearPhoneSized(other)
		if !other.isWindowOwner() {
			other.setOwner(nil)
		}
		cols, rows := other.getGuiSize()
		tc, tr := sizeOf(other.s.Term)
		if cols > 0 && rows > 0 && (cols != tc || rows != tr) {
			s.resize(other, ResizeReq{Cols: cols, Rows: rows})
		}
	}
}

// takeSize makes watcher w the owner of se and resizes se to w.Size() if
// different: the rule of a watcher's input, also applied when a phone
// selects the session.
func (s *Server) takeSize(se *sess, w remote.Watcher) {
	se.setOwner(w)
	wCols, wRows := w.Size()
	tc, tr := sizeOf(se.s.Term)
	if wCols > 0 && wRows > 0 && (wCols != tc || wRows != tr) {
		s.resize(se, ResizeReq{Cols: wCols, Rows: wRows})
	}
	s.markPhoneSized(se)
}

// markPhoneSized records that a phone resized se: the window's next input
// scans its sessions to take their size back. Called after the resize.
func (s *Server) markPhoneSized(se *sess) {
	if se.phoneSized.CompareAndSwap(false, true) {
		s.phoneSized.Add(1)
	}
}

// clearPhoneSized undoes markPhoneSized once the window took se back or
// the session ended.
func (s *Server) clearPhoneSized(se *sess) {
	if se.phoneSized.CompareAndSwap(true, false) {
		s.phoneSized.Add(-1)
	}
}
