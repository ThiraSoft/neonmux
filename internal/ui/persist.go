package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/server"
)

// The layout (order and kind of panes, directories, open files) is written
// by the window. The terminals' content is saved by the daemon, its sole
// owner: the two are matched up through the pane key.

type paneState struct {
	Kind  string     `json:"kind"`
	Key   string     `json:"key,omitempty"`
	Path  string     `json:"path,omitempty"`
	Name  string     `json:"name,omitempty"`
	Float *floatGeom `json:"float,omitempty"` // present if the pane floats
	Z     int        `json:"z,omitempty"`     // stacking order of floating panes
	Seen  uint64     `json:"seen,omitempty"`  // last agent state already seen (vt.ProgStatus.Gen)
	// Garage: a parked pane keeps the size of its PTY (columns × rows) and
	// its floating spot. The visitor stays out; Home, Cols and Rows are then
	// the spot and size it gets back when it goes back in.
	Garage  bool       `json:"garage,omitempty"`
	Visitor bool       `json:"visitor,omitempty"`
	Home    *floatGeom `json:"home,omitempty"`
	Cols    int        `json:"cols,omitempty"`
	Rows    int        `json:"rows,omitempty"`
	// WS is the workspace of a pane put away with it (workspace.go).
	WS int `json:"ws,omitempty"`
}

// wsSave is the layout of a hidden workspace. Focus indexes Panes.
type wsSave struct {
	Layout string  `json:"layout"`
	Ratio  float64 `json:"ratio"`
	Ratio2 float64 `json:"ratio2"`
	Focus  int     `json:"focus"`
	Zoomed bool    `json:"zoomed,omitempty"`
}

type layoutState struct {
	Version int     `json:"version"`
	Layout  string  `json:"layout"`
	Ratio   float64 `json:"ratio"`
	Ratio2  float64 `json:"ratio2"`
	Focus   int     `json:"focus"`
	Zoomed  bool    `json:"zoomed"`
	// Fullscreen keeps fullscreen from one launch to the next (neonmux
	// restart after an update, for example).
	Fullscreen bool        `json:"fullscreen,omitempty"`
	Panes      []paneState `json:"panes"`
	// Workspace is the one shown; Layout and the fields above are its own.
	// The hidden ones are in Workspaces, by number.
	Workspace  int            `json:"workspace,omitempty"`
	Workspaces map[int]wsSave `json:"workspaces,omitempty"`
}

func layoutPath() string { return filepath.Join(server.StateDir(), "layout.json") }

func removeLayout() { os.Remove(layoutPath()) }

func (a *App) saveLayout() {
	a.layoutDirty = false
	a.layoutSavedAt = time.Now()
	if a.client == nil {
		return
	}
	data, _ := json.MarshalIndent(a.layoutState(), "", "  ")
	_ = os.MkdirAll(filepath.Dir(layoutPath()), 0o700)
	tmp := layoutPath() + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, layoutPath())
	}
}

// layoutState describes the panes and the layout as they are saved.
func (a *App) layoutState() layoutState {
	st := layoutState{Version: 1, Layout: a.layout, Ratio: a.ratio, Ratio2: a.ratio2, Focus: a.focus, Zoomed: a.zoomed,
		Fullscreen: ebiten.IsFullscreen(), Workspace: a.ws}
	saved := map[Pane]int{} // index of each pane in st.Panes
	for _, p := range a.panes {
		n := len(st.Panes)
		switch p := p.(type) {
		case *TermPane:
			dir := p.dir
			if p.s != nil && p.s.Cwd() != "" {
				dir = p.s.Cwd()
			}
			ps := paneState{Kind: "term", Key: p.key, Path: dir}
			if p.hasProg && p.progSeen {
				ps.Seen = p.prog.Gen
			}
			switch {
			case p.parked:
				ps.Cols, ps.Rows = p.cols, p.rows
			case Pane(p) == a.visitor:
				ps.Cols, ps.Rows = a.visitorCols, a.visitorRows
			}
			st.Panes = append(st.Panes, ps)
		case *ImagePane:
			st.Panes = append(st.Panes, paneState{Kind: "image", Path: p.path})
		case *AudioPane:
			st.Panes = append(st.Panes, paneState{Kind: "audio", Path: p.path})
		case *HelpPane:
			st.Panes = append(st.Panes, paneState{Kind: "help"})
		case *DiffPane:
			st.Panes = append(st.Panes, paneState{Kind: "diff", Path: p.root})
		case *AgentsPane:
			st.Panes = append(st.Panes, paneState{Kind: "agents"})
		}
		if b := p.Base(); len(st.Panes) > n {
			saved[p] = n
			st.Panes[n].Name, st.Panes[n].WS = b.name, b.away
			st.Panes[n].Garage, st.Panes[n].Visitor = b.parked && b.away == 0, p == a.visitor
			if b.floating || b.parked && !b.geom.empty() {
				g := b.geom
				st.Panes[n].Float, st.Panes[n].Z = &g, b.z
			}
			if g := a.visitorGeom; p == a.visitor && !g.empty() {
				st.Panes[n].Home = &g
			}
		}
	}
	for n, w := range a.wss {
		if w.layout == "" || n == a.curWS() {
			continue
		}
		if st.Workspaces == nil {
			st.Workspaces = map[int]wsSave{}
		}
		focus, ok := saved[w.focus]
		if !ok {
			focus = -1
		}
		st.Workspaces[n] = wsSave{Layout: w.layout, Ratio: w.ratio, Ratio2: w.ratio2, Focus: focus, Zoomed: w.zoomed}
	}
	return st
}

// restoreLayout recreates the saved panes. Terminals reattach to their
// session if the daemon still has it, or restore it from disk. Parked
// panes reattach without being shown; those beyond the tile or floating
// pane limits go to the garage rather than being lost.
func (a *App) restoreLayout() bool {
	data, err := os.ReadFile(layoutPath())
	if err != nil {
		return false
	}
	var st layoutState
	if json.Unmarshal(data, &st) != nil || len(st.Panes) == 0 {
		return false
	}
	tiles, floats := 0, 0
	var focused Pane
	sizes := map[*TermPane][2]int{}
	cur := max(st.Workspace, 1)
	byIndex := map[int]Pane{} // panes of hidden workspaces, by index in st.Panes
	for k, ps := range st.Panes {
		if len(a.panes) >= maxTotalPanes {
			break
		}
		var p Pane
		switch ps.Kind {
		case "term":
			t := NewTermPaneAttach(a.newID(), ps.Key, ps.Path, a.paneEnv())
			t.seenGen = ps.Seen
			p = t
		case "image":
			if _, err := os.Stat(ps.Path); err == nil {
				p = NewImagePane(a.newID(), ps.Path)
			}
		case "help":
			p = NewHelpPane(a.newID())
		case "agents":
			p = NewAgentsPane(a.newID())
		case "diff":
			if _, err := os.Stat(ps.Path); err == nil {
				p = NewDiffPane(a.newID(), ps.Path)
			}
		case "audio":
			if _, err := os.Stat(ps.Path); err == nil {
				ap := NewAudioPane(a.newID(), ps.Path)
				ap.paused = true // no surprise music at startup
				p = ap
			}
		}
		if p == nil {
			continue
		}
		b := p.Base()
		b.name = ps.Name
		b.glitchAt = time.Now()
		float := ps.Float != nil && !ps.Float.empty()
		switch {
		case ps.WS > 0 && ps.WS <= maxWorkspaces && ps.WS != cur && !ps.Garage:
			// Put away with its workspace, as it was left.
			b.parked, b.hidden, b.away = true, true, ps.WS
			if float {
				b.floating, b.geom, b.z = true, *ps.Float, ps.Z
			}
			if t, ok := p.(*TermPane); ok {
				sizes[t] = [2]int{ps.Cols, ps.Rows}
			}
			byIndex[k] = p
		case ps.Visitor && a.visitor == nil && !ps.Garage:
			// The visitor stays out, where it was left.
			b.floating, b.geom, b.z = true, a.visitorFloatGeom(), ps.Z
			if float {
				b.geom = *ps.Float
			}
			if ps.Home != nil {
				a.visitorGeom = *ps.Home
			}
			a.visitor, a.visitorCols, a.visitorRows = p, ps.Cols, ps.Rows
		case ps.Garage, float && floats >= maxFloats, !float && tiles >= a.cfg.MaxPanes:
			b.parked, b.hidden = true, true
			if float {
				b.geom = *ps.Float // its spot if it ever floats again
			}
			if t, ok := p.(*TermPane); ok {
				sizes[t] = [2]int{ps.Cols, ps.Rows}
			}
		case float:
			b.floating, b.geom, b.z = true, *ps.Float, ps.Z
			floats++
		default:
			tiles++
		}
		a.zTop = max(a.zTop, b.z)
		if k == st.Focus {
			focused = p
		}
		a.panes = append(a.panes, p)
	}
	if len(a.panes) == 0 {
		return false
	}
	// Visible panes first, then the garage (garage.go).
	slices.SortStableFunc(a.panes, func(x, y Pane) int { return boolInt(x.Base().parked) - boolInt(y.Base().parked) })
	for t, sz := range sizes {
		a.startHidden(t, sz[0], sz[1])
	}
	if st.Workspace > 0 && st.Workspace <= maxWorkspaces {
		a.ws = st.Workspace
	}
	for n, w := range st.Workspaces {
		if n < 1 || n > maxWorkspaces || n == cur || w.Layout == "" {
			continue
		}
		a.wss[n] = wsState{layout: w.Layout, ratio: w.Ratio, ratio2: w.Ratio2, zoomed: w.Zoomed, focus: byIndex[w.Focus]}
	}
	if st.Layout != "" {
		a.layout = st.Layout
	}
	if st.Ratio > 0 {
		a.ratio, a.ratio2 = st.Ratio, st.Ratio2
	}
	a.focus = slices.Index(a.panes, focused)
	if focused == nil || isParked(focused) {
		a.focus = min(0, a.visibleCount()-1)
	}
	if a.visitor != nil {
		a.raise(a.visitor)
	}
	if p := a.focused(); p != nil && isFloat(p) {
		a.raise(p)
	}
	a.zoomed = st.Zoomed
	if st.Fullscreen {
		ebiten.SetFullscreen(true)
	}
	a.relayout()
	return true
}
