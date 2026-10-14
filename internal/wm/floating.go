package wm

import (
	"fmt"
	"image"
)

func centered(area image.Rectangle, size image.Point) image.Rectangle {
	corner := area.Min.Add(area.Size().Sub(size).Div(2))
	return image.Rectangle{Min: corner, Max: corner.Add(size)}
}

func (s State) MapFloating(id WindowID, rect image.Rectangle) (State, error) {
	if id == 0 {
		return State{}, ErrReservedWindow
	}
	if s.windowWorkspace(id) != nil {
		return State{}, fmt.Errorf("wm: map floating window %d: already mapped", id)
	}
	s = s.clone()
	w := s.activeWorkspace()
	if w == nil {
		return State{}, fmt.Errorf("map floating window %d: %w", id, ErrUnknownOutput)
	}
	w.Floating = append(w.Floating, Floating{ID: id, Rect: rect})
	w.floatFocus = true
	w.record()
	return s, nil
}

func (s State) Rect(id WindowID) (image.Rectangle, bool) {
	for _, p := range s.Arrange(0).Windows {
		if p.ID == id {
			return p.Rect, true
		}
	}
	return image.Rectangle{}, false
}

func (s State) ToggleFloating(at Autotile) (State, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil
	}
	s = s.clone()
	w := s.windowWorkspace(id)
	o := s.output(w.Output)
	if i := w.floatingIndex(id); i >= 0 {
		if err := w.remove(id); err != nil {
			return State{}, err
		}
		tree, err := w.Tree.Place(id, o.Area, at)
		if err != nil {
			return State{}, err
		}
		w.Tree, w.floatFocus = tree, false
		w.record()
		return s, nil
	}
	if err := w.remove(id); err != nil {
		return State{}, err
	}
	w.Floating = append(w.Floating, Floating{ID: id, Rect: centered(o.Area, o.Area.Size().Div(2))})
	w.floatFocus = true
	w.record()
	return s, nil
}

func (s State) SetFloatingRect(id WindowID, rect image.Rectangle) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("place floating window %d: %w", id, ErrUnknownWindow)
	}
	i := w.floatingIndex(id)
	if i < 0 {
		return State{}, fmt.Errorf("wm: window %d is not floating", id)
	}
	w.Floating[i].Rect = rect
	return s, nil
}

func (s State) Floats(id WindowID) bool {
	w := s.windowWorkspace(id)
	return w != nil && w.floatingIndex(id) >= 0
}

func (s State) SetFullscreen(id WindowID, on bool) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil {
		return State{}, fmt.Errorf("fullscreen window %d: %w", id, ErrUnknownWindow)
	}
	switch {
	case on:
		w.Fullscreen = id
		if err := w.focus(id); err != nil {
			return State{}, err
		}
	case w.Fullscreen == id:
		w.Fullscreen = 0
	}
	return s, nil
}

func (s State) ToggleFullscreen() (State, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil
	}
	w := s.windowWorkspace(id)
	return s.SetFullscreen(id, w.Fullscreen != id)
}

func (s State) Fullscreen(id WindowID) bool {
	w := s.windowWorkspace(id)
	return w != nil && w.Fullscreen == id
}

func (s State) Reinsert(id WindowID, at image.Point, auto Autotile) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil || !w.Tree.Contains(id) {
		return State{}, fmt.Errorf("drop window %d: %w", id, ErrUnknownWindow)
	}
	o := s.output(w.Output)
	tree, err := w.Tree.Remove(id)
	if err != nil {
		return State{}, err
	}
	if tree, err = tree.InsertAt(id, o.Area, at, auto); err != nil {
		return State{}, err
	}
	w.Tree = tree
	w.record()
	return s, nil
}

func (s State) ResizeTiled(id WindowID, d Direction, delta int) (State, error) {
	s = s.clone()
	w := s.windowWorkspace(id)
	if w == nil || !w.Tree.Contains(id) {
		return State{}, fmt.Errorf("resize window %d: %w", id, ErrUnknownWindow)
	}
	tree, err := w.Tree.Resize(id, s.output(w.Output).Area, d, delta)
	if err != nil {
		return State{}, err
	}
	w.Tree = tree
	return s, nil
}
