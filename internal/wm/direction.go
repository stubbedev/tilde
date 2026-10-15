package wm

import (
	"fmt"
	"image"
	"slices"
)

type Direction uint8

const (
	Left Direction = iota + 1
	Right
	Up
	Down
)

func (d Direction) vertical() bool {
	return d == Up || d == Down
}

func (d Direction) forward() bool {
	return d == Right || d == Down
}

func (d Direction) String() string {
	switch d {
	case Left:
		return "left"
	case Right:
		return "right"
	case Up:
		return "up"
	case Down:
		return "down"
	}
	return fmt.Sprintf("Direction(%d)", uint8(d))
}

func along(l Layout, d Direction) bool {
	if l == SplitV {
		return d.vertical()
	}
	return !d.vertical()
}

func (t Tree) breakout(groups []*Group, idx []int, d Direction) int {
	for level, g := range slices.Backward(groups) {
		i := idx[level]
		atEdge := (d.forward() && i == len(g.Children)-1) || (!d.forward() && i == 0)
		if along(g.Layout, d) && !atEdge {
			return level
		}
	}
	return -1
}

func step(i int, d Direction) int {
	if d.forward() {
		return i + 1
	}
	return i - 1
}

func (t Tree) Neighbor(id WindowID, d Direction) (WindowID, bool, error) {
	groups, idx, ok := t.path(id)
	if !ok {
		return 0, false, fmt.Errorf("look %s of window %d: %w", d, id, ErrUnknownWindow)
	}
	level := t.breakout(groups, idx, d)
	if level < 0 {
		return 0, false, nil
	}
	n := groups[level].Children[step(idx[level], d)].Node
	for {
		switch v := n.(type) {
		case Window:
			return v.ID, true, nil
		case *Group:
			switch {
			case v.Layout == Tabbed:
				n = v.Children[v.Focus].Node
			case along(v.Layout, d) && d.forward():
				n = v.Children[0].Node
			case along(v.Layout, d):
				n = v.Children[len(v.Children)-1].Node
			default:
				n = v.Children[v.Focus].Node
			}
		}
	}
}

func (t Tree) groupPath(target *Group) ([]*Group, []int) {
	var groups []*Group
	var idx []int
	var find func(g *Group) bool
	find = func(g *Group) bool {
		groups = append(groups, g)
		if g == target {
			idx = append(idx, 0)
			return true
		}
		for i, c := range g.Children {
			if sub, ok := c.Node.(*Group); ok {
				idx = append(idx, i)
				if find(sub) {
					return true
				}
				idx = idx[:len(idx)-1]
			}
		}
		groups = groups[:len(groups)-1]
		return false
	}
	if t.root == nil || !find(t.root) {
		return nil, nil
	}
	return groups, idx
}

func (t *Tree) collapseAt(g *Group, policy CollapsePolicy) {
	groups, idx := t.groupPath(g)
	if groups != nil {
		t.collapse(groups, idx, policy)
	}
}

func (t Tree) Move(id WindowID, d Direction, policy CollapsePolicy) (Tree, bool, error) {
	original := t
	t = t.clone()
	groups, idx, ok := t.path(id)
	if !ok {
		return Tree{}, false, fmt.Errorf("move window %d %s: %w", id, d, ErrUnknownWindow)
	}
	level := t.breakout(groups, idx, d)
	if level < 0 {
		return original, false, nil
	}
	leaf := len(groups) - 1
	parent := groups[leaf]
	g, j := groups[level], step(idx[level], d)
	var target *Group
	var at int
	switch n := g.Children[j].Node.(type) {
	case Window:
		if level == leaf {
			i := idx[leaf]
			g.Children[i], g.Children[j] = g.Children[j], g.Children[i]
			g.Focus = j
			t.collapseAt(g, policy)
			return t, true, nil
		}
		target, at = g, j
		if !d.forward() {
			at = j + 1
		}
	case *Group:
		target = n
		for {
			c, after := target.Focus, true
			if along(target.Layout, d) {
				c, after = 0, false
				if !d.forward() {
					c, after = len(target.Children)-1, true
				}
			}
			if _, isWindow := target.Children[c].Node.(Window); isWindow {
				at = c
				if after {
					at = c + 1
				}
				break
			}
			target, _ = target.Children[c].Node.(*Group)
		}
	}
	if len(parent.Children) == 1 && leaf > 0 && target == groups[leaf-1] {
		target.Children[idx[leaf-1]].Node = Window{ID: id}
	} else {
		parent.remove(idx[leaf])
		target.insert(at, Window{ID: id})
		t.collapseAt(parent, CollapseInvalid)
		t.collapseAt(target, policy)
	}
	moved, err := t.Focus(id)
	return moved, true, err
}

type neighbourOutput struct {
	name string
	gap  int
}

func adjacent(from, to image.Rectangle, d Direction) (int, bool) {
	switch d {
	case Left:
		return from.Min.X - to.Max.X, to.Max.X <= from.Min.X && to.Min.Y < from.Max.Y && from.Min.Y < to.Max.Y
	case Right:
		return to.Min.X - from.Max.X, to.Min.X >= from.Max.X && to.Min.Y < from.Max.Y && from.Min.Y < to.Max.Y
	case Up:
		return from.Min.Y - to.Max.Y, to.Max.Y <= from.Min.Y && to.Min.X < from.Max.X && from.Min.X < to.Max.X
	case Down:
		return to.Min.Y - from.Max.Y, to.Min.Y >= from.Max.Y && to.Min.X < from.Max.X && from.Min.X < to.Max.X
	}
	return 0, false
}

func (s State) outputToward(from string, d Direction) (string, bool) {
	o := s.output(from)
	if o == nil {
		return "", false
	}
	var best *neighbourOutput
	for _, other := range s.outputs {
		if other.Name == from {
			continue
		}
		if gap, ok := adjacent(o.Area, other.Area, d); ok && (best == nil || gap < best.gap) {
			best = &neighbourOutput{other.Name, gap}
		}
	}
	if best == nil {
		return "", false
	}
	return best.name, true
}

func (s State) nearest(id WindowID, d Direction) (WindowID, bool) {
	var from image.Rectangle
	var visible []Placement
	for _, p := range s.Arrange(0).Windows {
		if p.ID == id {
			from = p.Rect
		} else if p.Visible {
			visible = append(visible, p)
		}
	}
	c := from.Min.Add(from.Size().Div(2))
	best, bestScore := WindowID(0), -1
	for _, p := range visible {
		pc := p.Rect.Min.Add(p.Rect.Size().Div(2))
		var along, across int
		switch d {
		case Left:
			along, across = from.Min.X-p.Rect.Min.X, pc.Y-c.Y
		case Right:
			along, across = p.Rect.Max.X-from.Max.X, pc.Y-c.Y
		case Up:
			along, across = from.Min.Y-p.Rect.Min.Y, pc.X-c.X
		case Down:
			along, across = p.Rect.Max.Y-from.Max.Y, pc.X-c.X
		}
		if along <= 0 {
			continue
		}
		if across < 0 {
			across = -across
		}
		if score := along + 2*across; bestScore < 0 || score < bestScore {
			best, bestScore = p.ID, score
		}
	}
	return best, bestScore >= 0
}

func (s State) FocusDirection(d Direction) (State, error) {
	if w := s.activeWorkspace(); w != nil {
		if id, ok := w.focused(); ok {
			next, found := s.nearest(id, d)
			if w.floatingIndex(id) < 0 {
				var err error
				if next, found, err = w.Tree.Neighbor(id, d); err != nil {
					return State{}, err
				}
			}
			if found {
				return s.FocusWindow(next)
			}
		}
	}
	name, ok := s.outputToward(s.focused, d)
	if !ok {
		return s, nil
	}
	return s.FocusOutput(name)
}

func (s State) MoveDirection(d Direction, policy CollapsePolicy) (State, error) {
	w := s.activeWorkspace()
	if w == nil {
		return s, nil
	}
	id, ok := w.focused()
	if !ok || w.floatingIndex(id) >= 0 {
		return s, nil
	}
	tree, moved, err := w.Tree.Move(id, d, policy)
	if err != nil || !moved {
		return s, err
	}
	s = s.clone()
	active := s.activeWorkspace()
	active.Tree = tree
	active.record()
	return s, nil
}

func (t Tree) InsertAt(id WindowID, area image.Rectangle, at image.Point, auto Autotile) (Tree, error) {
	var target Placement
	found := false
	for _, p := range t.Arrange(area, 0).Windows {
		if p.Visible && at.In(p.Rect) {
			target, found = p, true
		}
	}
	if !found {
		return t.Place(id, area, auto)
	}
	groups, _, _ := t.path(target.ID)
	parent := groups[len(groups)-1]
	mid := target.Rect.Min.Add(target.Rect.Size().Div(2))
	before := (parent.Layout == SplitH && at.X < mid.X) || (parent.Layout == SplitV && at.Y < mid.Y)
	focused, err := t.Focus(target.ID)
	if err != nil {
		return Tree{}, err
	}
	placed, err := focused.Place(id, area, auto)
	if err != nil || !before {
		return placed, err
	}
	groups, idx, _ := placed.path(id)
	g, i := groups[len(groups)-1], idx[len(idx)-1]
	g.Children[i-1], g.Children[i] = g.Children[i], g.Children[i-1]
	g.Focus = i - 1
	return placed, nil
}

const minTile = 32

func (t Tree) Resize(id WindowID, area image.Rectangle, d Direction, delta int) (Tree, bool, error) {
	t = t.clone()
	groups, idx, ok := t.path(id)
	if !ok {
		return Tree{}, false, fmt.Errorf("resize window %d: %w", id, ErrUnknownWindow)
	}
	want := SplitH
	if d.vertical() {
		want = SplitV
	}
	for level, g := range slices.Backward(groups) {
		i := idx[level]
		j := step(i, d)
		if g.Layout != want || j < 0 || j >= len(g.Children) {
			continue
		}
		r := t.rectOf(area, g)
		span := r.Dx()
		if d.vertical() {
			span = r.Dy()
		}
		if span <= 0 {
			return t, false, nil
		}
		perPixel := float64(len(g.Children)) / float64(span)
		floor := minTile * perPixel
		dr := float64(delta) * perPixel
		dr = min(dr, max(0, g.Children[j].Ratio-floor))
		dr = max(dr, -max(0, g.Children[i].Ratio-floor))
		g.Children[i].Ratio += dr
		g.Children[j].Ratio -= dr
		return t, true, nil
	}
	return t, false, nil
}
