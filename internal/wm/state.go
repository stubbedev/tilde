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
	Area   image.Rectangle
	Active WorkspaceID
}

type Workspace struct {
	ID      WorkspaceID
	Output  string
	Tree    Tree
	home    string
	history []WindowID
}

func (w *Workspace) record() {
	id, ok := w.Tree.Focused()
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
		if s.workspaces[i].Tree.Contains(id) {
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
	if w == nil || !w.Tree.Empty() {
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

func (s State) AddOutput(name string, area image.Rectangle) (State, error) {
	if s.output(name) != nil {
		return State{}, fmt.Errorf("wm: output %s added twice", name)
	}
	s = s.clone()
	s.outputs = append(s.outputs, Output{Name: name, Area: area})
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

func (s State) SetArea(name string, area image.Rectangle) (State, error) {
	if s.output(name) == nil {
		return State{}, fmt.Errorf("set the area of output %s: %w", name, ErrUnknownOutput)
	}
	s = s.clone()
	s.output(name).Area = area
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
	return w.Tree.Focused()
}

func (s State) MapWindow(id WindowID, at Autotile) (State, error) {
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
	focused, _ := w.Tree.Focused()
	tree, err := w.Tree.Remove(id)
	if err != nil {
		return State{}, err
	}
	w.history = slices.DeleteFunc(w.history, func(x WindowID) bool { return x == id })
	if n := len(w.history); focused == id && n > 0 {
		if tree, err = tree.Focus(w.history[n-1]); err != nil {
			return State{}, err
		}
	}
	w.Tree = tree
	s.dropIfIdle(w.ID)
	return s, nil
}

func (s State) FocusWindow(id WindowID) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("focus window %d: %w", id, ErrUnknownWindow)
	}
	tree, err := w.Tree.Focus(id)
	if err != nil {
		return State{}, err
	}
	w.Tree = tree
	w.record()
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
	tree, err := from.Tree.Remove(win)
	if err != nil {
		return State{}, err
	}
	from.Tree = tree
	from.history = slices.DeleteFunc(from.history, func(x WindowID) bool { return x == win })
	from.record()
	to := s.workspace(id)
	if to == nil {
		to = s.create(id, s.focused)
	}
	area := image.Rectangle{}
	if o := s.output(to.Output); o != nil {
		area = o.Area
	}
	if to.Tree, err = to.Tree.Place(win, area, at); err != nil {
		return State{}, err
	}
	to.record()
	return s, nil
}

func (s State) Arrange(tabBarHeight int) Arrangement {
	var all Arrangement
	for _, w := range s.workspaces {
		o := s.output(w.Output)
		if o == nil {
			continue
		}
		a := w.Tree.Arrange(o.Area, tabBarHeight)
		for _, p := range a.Windows {
			p.Visible = p.Visible && o.Active == w.ID
			all.Windows = append(all.Windows, p)
		}
		if o.Active == w.ID {
			all.TabBars = append(all.TabBars, a.TabBars...)
		}
	}
	for _, w := range s.workspaces {
		if s.output(w.Output) != nil {
			continue
		}
		for _, id := range w.Tree.Windows() {
			all.Windows = append(all.Windows, Placement{ID: id})
		}
	}
	return all
}
