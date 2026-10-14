// Package wm is the window manager's pure core: the container tree, its
// layout, workspaces and focus, as values with no Wayland, X11 or rendering
// types.
package wm

import (
	"errors"
	"fmt"
	"image"
	"slices"
)

type WindowID uint64

type Layout uint8

const (
	SplitH Layout = iota + 1
	SplitV
	Tabbed
)

func (l Layout) split() bool {
	return l == SplitH || l == SplitV
}

func (l Layout) String() string {
	switch l {
	case SplitH:
		return "h"
	case SplitV:
		return "v"
	case Tabbed:
		return "tab"
	}
	return fmt.Sprintf("Layout(%d)", uint8(l))
}

type CollapsePolicy uint8

const (
	CollapseInvalid CollapsePolicy = iota
	CollapseEmptySplits
	CollapseSingleNodeGroups
)

type ephemerality uint8

const (
	ephemeralOff ephemerality = iota
	ephemeralStaged
	ephemeralActive
)

type Node interface {
	clone() Node
}

type Window struct {
	ID WindowID
}

func (w Window) clone() Node {
	return w
}

type Child struct {
	Node  Node
	Ratio float64
}

type Group struct {
	Layout    Layout
	Children  []Child
	Focus     int
	ephemeral ephemerality
}

func (g *Group) clone() Node {
	c := *g
	c.Children = make([]Child, len(g.Children))
	for i, ch := range g.Children {
		c.Children[i] = Child{Node: ch.Node.clone(), Ratio: ch.Ratio}
	}
	return &c
}

func (g *Group) Ephemeral() bool {
	return g.ephemeral != ephemeralOff
}

func (g *Group) insert(at int, n Node) {
	g.Children = slices.Insert(g.Children, at, Child{Node: n, Ratio: 1})
	if g.ephemeral == ephemeralStaged && len(g.Children) >= 2 {
		g.ephemeral = ephemeralActive
	}
}

func (g *Group) remove(i int) {
	ratio := g.Children[i].Ratio
	g.Children = slices.Delete(g.Children, i, i+1)
	switch {
	case len(g.Children) == 0:
		g.Focus = 0
	case g.Focus > i, g.Focus == i && i > 0:
		g.Focus--
	}
	if count := len(g.Children); count > 0 {
		shift := (1 - ratio) / float64(count)
		for j := range g.Children {
			g.Children[j].Ratio -= shift
		}
	}
}

type Tree struct {
	root *Group
}

var ErrUnknownWindow = errors.New("wm: unknown window")

func (t Tree) Root() *Group {
	return t.root
}

func (t Tree) Empty() bool {
	return t.root == nil
}

func (t Tree) clone() Tree {
	if t.root == nil {
		return t
	}
	g, _ := t.root.clone().(*Group)
	return Tree{root: g}
}

func (t Tree) Windows() []WindowID {
	var out []WindowID
	var walk func(n Node)
	walk = func(n Node) {
		switch n := n.(type) {
		case Window:
			out = append(out, n.ID)
		case *Group:
			for _, c := range n.Children {
				walk(c.Node)
			}
		}
	}
	if t.root != nil {
		walk(t.root)
	}
	return out
}

func (t Tree) Contains(id WindowID) bool {
	return slices.Contains(t.Windows(), id)
}

func (t Tree) path(id WindowID) ([]*Group, []int, bool) {
	var groups []*Group
	var idx []int
	var find func(g *Group) bool
	find = func(g *Group) bool {
		for i, c := range g.Children {
			groups, idx = append(groups, g), append(idx, i)
			switch n := c.Node.(type) {
			case Window:
				if n.ID == id {
					return true
				}
			case *Group:
				if find(n) {
					return true
				}
			}
			groups, idx = groups[:len(groups)-1], idx[:len(idx)-1]
		}
		return false
	}
	if t.root == nil || !find(t.root) {
		return nil, nil, false
	}
	return groups, idx, true
}

func (t Tree) Focused() (WindowID, bool) {
	g := t.root
	for g != nil && len(g.Children) > 0 {
		switch n := g.Children[g.Focus].Node.(type) {
		case Window:
			return n.ID, true
		case *Group:
			g = n
		}
	}
	return 0, false
}

func (t Tree) Focus(id WindowID) (Tree, error) {
	t = t.clone()
	groups, idx, ok := t.path(id)
	if !ok {
		return Tree{}, fmt.Errorf("focus window %d: %w", id, ErrUnknownWindow)
	}
	for i, g := range groups {
		g.Focus = idx[i]
	}
	return t, nil
}

func (t Tree) Insert(id WindowID, rootLayout Layout) (Tree, error) {
	if t.Contains(id) {
		return Tree{}, fmt.Errorf("wm: insert window %d: already in the tree", id)
	}
	t = t.clone()
	if t.root == nil {
		t.root = &Group{Layout: rootLayout}
		t.root.insert(0, Window{ID: id})
		return t, nil
	}
	focused, _ := t.Focused()
	groups, idx, _ := t.path(focused)
	parent, at := groups[len(groups)-1], idx[len(idx)-1]
	parent.insert(at+1, Window{ID: id})
	return t.Focus(id)
}

type Autotile struct {
	Enabled    bool
	Ephemeral  bool
	MinTrigger int
	Divisor    int
}

func (a Autotile) trigger(area image.Rectangle) int {
	return max(a.MinTrigger, area.Dx()/a.Divisor)
}

func (t Tree) Place(id WindowID, area image.Rectangle, at Autotile) (Tree, error) {
	root := SplitH
	if area.Dy() > area.Dx() {
		root = SplitV
	}
	focused, ok := t.Focused()
	if !at.Enabled || !ok {
		return t.Insert(id, root)
	}
	groups, _, _ := t.path(focused)
	parent := groups[len(groups)-1]
	if parent.Layout == SplitH && len(parent.Children) > 1 && t.rectOf(area, parent).Dx()/(len(parent.Children)+1) < at.trigger(area) {
		wrapped, err := t.Wrap(focused, SplitV, at.Ephemeral)
		if err != nil {
			return Tree{}, err
		}
		t = wrapped
	}
	return t.Insert(id, root)
}

func (t Tree) Remove(id WindowID) (Tree, error) {
	t = t.clone()
	groups, idx, ok := t.path(id)
	if !ok {
		return Tree{}, fmt.Errorf("remove window %d: %w", id, ErrUnknownWindow)
	}
	groups[len(groups)-1].remove(idx[len(idx)-1])
	t.collapse(groups, idx, CollapseInvalid)
	return t, nil
}

func (t Tree) Wrap(id WindowID, layout Layout, ephemeral bool) (Tree, error) {
	t = t.clone()
	groups, idx, ok := t.path(id)
	if !ok {
		return Tree{}, fmt.Errorf("wrap window %d: %w", id, ErrUnknownWindow)
	}
	parent, at := groups[len(groups)-1], idx[len(idx)-1]
	g := &Group{Layout: layout}
	g.insert(0, parent.Children[at].Node)
	if ephemeral {
		g.ephemeral = ephemeralStaged
	}
	parent.Children[at].Node = g
	return t, nil
}

func shouldCollapse(g *Group, rootGroup bool, policy CollapsePolicy) bool {
	if len(g.Children) != 1 {
		return false
	}
	child, isGroup := g.Children[0].Node.(*Group)
	switch {
	case rootGroup && !isGroup:
		return false
	case policy == CollapseSingleNodeGroups, g.ephemeral == ephemeralActive:
		return true
	case policy == CollapseEmptySplits && g.Layout.split():
		return true
	case !isGroup:
		return false
	case g.Layout.split() && child.Layout.split():
		return true
	}
	return len(child.Children) == 1 && g.Layout == Tabbed && child.Layout == Tabbed
}

func (t *Tree) collapse(groups []*Group, idx []int, policy CollapsePolicy) {
	for level, g := range slices.Backward(groups) {
		if level == 0 {
			switch {
			case len(g.Children) == 0:
				t.root = nil
			case shouldCollapse(g, true, policy):
				t.root, _ = g.Children[0].Node.(*Group)
			}
			return
		}
		parent, at := groups[level-1], idx[level-1]
		switch {
		case len(g.Children) == 0:
			parent.remove(at)
		case shouldCollapse(g, false, policy):
			parent.Children[at].Node = g.Children[0].Node
		}
		policy = CollapseInvalid
	}
}
