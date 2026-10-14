package wm

import (
	"errors"
	"fmt"
	"image"
	"maps"
	"slices"
)

type WorkspaceID int

type Output struct {
	Name   string
	Full   image.Rectangle
	Area   image.Rectangle
	Active WorkspaceID
}

type Floating struct {
	ID   WindowID
	Rect image.Rectangle
}

type Workspace struct {
	ID         WorkspaceID
	Output     string
	Tree       Tree
	Floating   []Floating
	Fullscreen WindowID
	floatFocus bool
	home       string
	history    []WindowID
}

func (w *Workspace) floatingIndex(id WindowID) int {
	return slices.IndexFunc(w.Floating, func(f Floating) bool { return f.ID == id })
}

func (w *Workspace) contains(id WindowID) bool {
	return w.floatingIndex(id) >= 0 || w.Tree.Contains(id)
}

func (w *Workspace) empty() bool {
	return w.Tree.Empty() && len(w.Floating) == 0
}

func (w *Workspace) focused() (WindowID, bool) {
	if w.floatFocus && len(w.Floating) > 0 {
		return w.Floating[len(w.Floating)-1].ID, true
	}
	return w.Tree.Focused()
}

func (w *Workspace) focus(id WindowID) error {
	if i := w.floatingIndex(id); i >= 0 {
		f := w.Floating[i]
		w.Floating = append(slices.Delete(w.Floating, i, i+1), f)
		w.floatFocus = true
		w.record()
		return nil
	}
	tree, err := w.Tree.Focus(id)
	if err != nil {
		return err
	}
	w.Tree, w.floatFocus = tree, false
	w.record()
	return nil
}

func (w *Workspace) remove(id WindowID) error {
	focused, _ := w.focused()
	if i := w.floatingIndex(id); i >= 0 {
		w.Floating = slices.Delete(w.Floating, i, i+1)
	} else {
		tree, err := w.Tree.Remove(id)
		if err != nil {
			return err
		}
		w.Tree = tree
	}
	if w.Fullscreen == id {
		w.Fullscreen = 0
	}
	w.history = slices.DeleteFunc(w.history, func(x WindowID) bool { return x == id })
	if focused != id {
		return nil
	}
	w.floatFocus = w.Tree.Empty() && len(w.Floating) > 0
	if n := len(w.history); n > 0 {
		return w.focus(w.history[n-1])
	}
	return nil
}

func (w *Workspace) record() {
	id, ok := w.focused()
	if !ok {
		return
	}
	if n := len(w.history); n > 0 && w.history[n-1] == id {
		return
	}
	w.history = append(slices.DeleteFunc(w.history, func(x WindowID) bool { return x == id }), id)
}

type State struct {
	outputs    []Output
	workspaces []Workspace
	focused    string
	remembered map[string]WorkspaceID
}

var (
	ErrUnknownOutput    = errors.New("wm: unknown output")
	ErrInvalidWorkspace = errors.New("wm: invalid workspace")
)

func (s State) clone() State {
	c := State{
		outputs:    slices.Clone(s.outputs),
		workspaces: make([]Workspace, len(s.workspaces)),
		focused:    s.focused,
		remembered: make(map[string]WorkspaceID, len(s.remembered)),
	}
	maps.Copy(c.remembered, s.remembered)
	for i, w := range s.workspaces {
		w.history = slices.Clone(w.history)
		w.Floating = slices.Clone(w.Floating)
		c.workspaces[i] = w
	}
	return c
}

func (s State) Outputs() []Output {
	return slices.Clone(s.outputs)
}

func (s State) Workspaces() []Workspace {
	return slices.Clone(s.workspaces)
}

func (s State) FocusedOutput() string {
	return s.focused
}

func (s *State) output(name string) *Output {
	for i := range s.outputs {
		if s.outputs[i].Name == name {
			return &s.outputs[i]
		}
	}
	return nil
}

func (s *State) workspace(id WorkspaceID) *Workspace {
	for i := range s.workspaces {
		if s.workspaces[i].ID == id {
			return &s.workspaces[i]
		}
	}
	return nil
}

func (s State) Workspace(id WorkspaceID) (Workspace, bool) {
	w := s.workspace(id)
	if w == nil {
		return Workspace{}, false
	}
	return *w, true
}

func (s *State) windowWorkspace(id WindowID) *Workspace {
	for i := range s.workspaces {
		if s.workspaces[i].contains(id) {
			return &s.workspaces[i]
		}
	}
	return nil
}

func (s *State) create(id WorkspaceID, output string) *Workspace {
	s.workspaces = append(s.workspaces, Workspace{ID: id, Output: output})
	slices.SortFunc(s.workspaces, func(a, b Workspace) int { return int(a.ID) - int(b.ID) })
	return s.workspace(id)
}

func (s *State) firstUnused() WorkspaceID {
	id := WorkspaceID(1)
	for s.workspace(id) != nil {
		id++
	}
	return id
}

func (s *State) dropIfIdle(id WorkspaceID) {
	w := s.workspace(id)
	if w == nil || !w.empty() {
		return
	}
	if o := s.output(w.Output); o != nil && o.Active == id {
		return
	}
	s.workspaces = slices.DeleteFunc(s.workspaces, func(x Workspace) bool { return x.ID == id })
}

func (s *State) activate(id WorkspaceID) {
	w := s.workspace(id)
	o := s.output(w.Output)
	if o == nil {
		return
	}
	prev := o.Active
	o.Active = id
	s.focused = o.Name
	if prev != id {
		s.dropIfIdle(prev)
	}
}

func (s State) AddOutput(name string, full, area image.Rectangle) (State, error) {
	if s.output(name) != nil {
		return State{}, fmt.Errorf("wm: output %s added twice", name)
	}
	s = s.clone()
	s.outputs = append(s.outputs, Output{Name: name, Full: full, Area: area})
	for i := range s.workspaces {
		w := &s.workspaces[i]
		if w.home == name || w.Output == "" {
			w.Output, w.home = name, ""
		}
	}
	o := s.output(name)
	if id, ok := s.remembered[name]; ok && s.workspace(id) != nil && s.workspace(id).Output == name {
		o.Active = id
	} else if w := s.firstOn(name); w != nil {
		o.Active = w.ID
	} else {
		o.Active = s.create(s.firstUnused(), name).ID
	}
	delete(s.remembered, name)
	for _, other := range s.outputs {
		if other.Name != name {
			if w := s.workspace(other.Active); w == nil || w.Output != other.Name {
				s.fixActive(other.Name)
			}
		}
	}
	if s.focused == "" {
		s.focused = name
	}
	return s, nil
}

func (s *State) firstOn(output string) *Workspace {
	for i := range s.workspaces {
		if s.workspaces[i].Output == output {
			return &s.workspaces[i]
		}
	}
	return nil
}

func (s *State) fixActive(output string) {
	o := s.output(output)
	if w := s.firstOn(output); w != nil {
		o.Active = w.ID
		return
	}
	o.Active = s.create(s.firstUnused(), output).ID
}

func (s State) RemoveOutput(name string) (State, error) {
	if s.output(name) == nil {
		return State{}, fmt.Errorf("remove output %s: %w", name, ErrUnknownOutput)
	}
	s = s.clone()
	gone := *s.output(name)
	s.outputs = slices.DeleteFunc(s.outputs, func(o Output) bool { return o.Name == name })
	if s.focused == name {
		s.focused = ""
		if len(s.outputs) > 0 {
			s.focused = s.outputs[0].Name
		}
	}
	s.remembered[name] = gone.Active
	for i := range s.workspaces {
		w := &s.workspaces[i]
		if w.Output != name {
			continue
		}
		if w.home == "" {
			w.home = name
		}
		w.Output = s.focused
	}
	if s.focused != "" {
		s.dropIdleOn(s.focused)
	}
	return s, nil
}

func (s *State) dropIdleOn(output string) {
	for _, w := range slices.Clone(s.workspaces) {
		if w.Output == output {
			s.dropIfIdle(w.ID)
		}
	}
}

func (s State) SetGeometry(name string, full, area image.Rectangle) (State, error) {
	if s.output(name) == nil {
		return State{}, fmt.Errorf("set the geometry of output %s: %w", name, ErrUnknownOutput)
	}
	s = s.clone()
	o := s.output(name)
	o.Full, o.Area = full, area
	return s, nil
}

func (s State) FocusOutput(name string) (State, error) {
	if s.output(name) == nil {
		return State{}, fmt.Errorf("focus output %s: %w", name, ErrUnknownOutput)
	}
	s = s.clone()
	s.focused = name
	return s, nil
}

func (s State) activeWorkspace() *Workspace {
	o := s.output(s.focused)
	if o == nil {
		return nil
	}
	return s.workspace(o.Active)
}

func (s State) Focused() (WindowID, bool) {
	w := s.activeWorkspace()
	if w == nil {
		return 0, false
	}
	return w.focused()
}

var ErrReservedWindow = errors.New("wm: window id 0 is reserved")

func (s State) MapWindow(id WindowID, at Autotile) (State, error) {
	if id == 0 {
		return State{}, ErrReservedWindow
	}
	if s.windowWorkspace(id) != nil {
		return State{}, fmt.Errorf("wm: map window %d: already mapped", id)
	}
	s = s.clone()
	w := s.activeWorkspace()
	if w == nil {
		if len(s.outputs) > 0 {
			return State{}, fmt.Errorf("wm: map window %d: no active workspace", id)
		}
		w = s.firstOn("")
		if w == nil {
			w = s.create(s.firstUnused(), "")
		}
	}
	area := image.Rectangle{}
	if o := s.output(w.Output); o != nil {
		area = o.Area
	}
	tree, err := w.Tree.Place(id, area, at)
	if err != nil {
		return State{}, err
	}
	w.Tree = tree
	w.record()
	return s, nil
}

func (s State) UnmapWindow(id WindowID) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("unmap window %d: %w", id, ErrUnknownWindow)
	}
	if err := w.remove(id); err != nil {
		return State{}, err
	}
	s.dropIfIdle(w.ID)
	return s, nil
}

func (s State) FocusWindow(id WindowID) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("focus window %d: %w", id, ErrUnknownWindow)
	}
	if err := w.focus(id); err != nil {
		return State{}, err
	}
	s.activate(w.ID)
	return s, nil
}

func (s State) WorkspaceOf(id WindowID) (WorkspaceID, bool) {
	w := s.windowWorkspace(id)
	if w == nil {
		return 0, false
	}
	return w.ID, true
}

func (s State) SwitchWorkspace(id WorkspaceID) (State, error) {
	if id < 1 {
		return State{}, fmt.Errorf("switch to workspace %d: %w", id, ErrInvalidWorkspace)
	}
	if s.output(s.focused) == nil {
		return State{}, fmt.Errorf("switch to workspace %d: %w", id, ErrUnknownOutput)
	}
	s = s.clone()
	if s.workspace(id) == nil {
		s.create(id, s.focused)
	}
	s.activate(id)
	return s, nil
}

func (s State) relative(offset int) (WorkspaceID, error) {
	o := s.output(s.focused)
	if o == nil {
		return 0, fmt.Errorf("step %+d workspaces: %w", offset, ErrUnknownOutput)
	}
	at := slices.IndexFunc(s.workspaces, func(w Workspace) bool { return w.ID == o.Active })
	n := len(s.workspaces)
	return s.workspaces[((at+offset)%n+n)%n].ID, nil
}

func (s State) RelativeWorkspace(offset int) (State, error) {
	id, err := s.relative(offset)
	if err != nil {
		return State{}, err
	}
	return s.SwitchWorkspace(id)
}

func (s State) MoveToWorkspace(id WorkspaceID, at Autotile) (State, error) {
	if id < 1 {
		return State{}, fmt.Errorf("move to workspace %d: %w", id, ErrInvalidWorkspace)
	}
	win, ok := s.Focused()
	if !ok {
		return s, nil
	}
	s = s.clone()
	from := s.windowWorkspace(win)
	if from.ID == id {
		return s, nil
	}
	fromOutput := from.Output
	floating := -1
	var rect image.Rectangle
	if i := from.floatingIndex(win); i >= 0 {
		floating, rect = i, from.Floating[i].Rect
	}
	if err := from.remove(win); err != nil {
		return State{}, err
	}
	to := s.workspace(id)
	if to == nil {
		to = s.create(id, s.focused)
	}
	var area image.Rectangle
	if o := s.output(to.Output); o != nil {
		area = o.Area
	}
	if floating >= 0 {
		if src := s.output(fromOutput); src != nil {
			rect = rect.Add(area.Min.Sub(src.Area.Min))
		}
		to.Floating = append(to.Floating, Floating{ID: win, Rect: rect})
		to.floatFocus = true
		to.record()
		return s, nil
	}
	tree, err := to.Tree.Place(win, area, at)
	if err != nil {
		return State{}, err
	}
	to.Tree, to.floatFocus = tree, false
	to.record()
	return s, nil
}

type PlacementLayer uint8

const (
	LayerTiled PlacementLayer = iota
	LayerFloating
	LayerFullscreen
)

func (s State) Arrange(tabBarHeight int) Arrangement {
	var all Arrangement
	for _, w := range s.workspaces {
		o := s.output(w.Output)
		if o == nil {
			for _, id := range w.Tree.Windows() {
				all.Windows = append(all.Windows, Placement{ID: id})
			}
			for _, f := range w.Floating {
				all.Windows = append(all.Windows, Placement{ID: f.ID, Rect: f.Rect, Layer: LayerFloating})
			}
			continue
		}
		shown := o.Active == w.ID
		covered := w.Fullscreen != 0
		a := w.Tree.Arrange(o.Area, tabBarHeight)
		for _, p := range a.Windows {
			if p.ID == w.Fullscreen {
				continue
			}
			p.Visible = p.Visible && shown && !covered
			all.Windows = append(all.Windows, p)
		}
		if shown && !covered {
			all.TabBars = append(all.TabBars, a.TabBars...)
		}
		for _, f := range w.Floating {
			if f.ID != w.Fullscreen {
				all.Windows = append(all.Windows, Placement{ID: f.ID, Rect: f.Rect, Visible: shown && !covered, Layer: LayerFloating})
			}
		}
		if covered {
			all.Windows = append(all.Windows, Placement{ID: w.Fullscreen, Rect: o.Full, Visible: shown, Layer: LayerFullscreen})
		}
	}
	return all
}
