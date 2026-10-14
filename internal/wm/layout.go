package wm

import (
	"image"
	"math"
)

type Placement struct {
	ID      WindowID
	Rect    image.Rectangle
	Visible bool
}

type TabBar struct {
	Rect   image.Rectangle
	Tabs   []WindowID
	Active int
}

type Arrangement struct {
	Windows []Placement
	TabBars []TabBar
}

func edges(lo, span int, children []Child) []int {
	total := 0.0
	for _, c := range children {
		total += c.Ratio
	}
	out := make([]int, len(children)+1)
	out[0] = lo
	acc := 0.0
	for i, c := range children[:len(children)-1] {
		acc += c.Ratio
		out[i+1] = lo + int(math.Round(float64(span)*acc/total))
	}
	out[len(children)] = lo + span
	return out
}

func firstWindow(n Node) WindowID {
	for {
		switch v := n.(type) {
		case Window:
			return v.ID
		case *Group:
			n = v.Children[v.Focus].Node
		}
	}
}

func childRects(g *Group, r image.Rectangle, tabBarHeight int) []image.Rectangle {
	out := make([]image.Rectangle, len(g.Children))
	switch g.Layout {
	case SplitH:
		e := edges(r.Min.X, r.Dx(), g.Children)
		for i := range out {
			out[i] = image.Rect(e[i], r.Min.Y, e[i+1], r.Max.Y)
		}
	case SplitV:
		e := edges(r.Min.Y, r.Dy(), g.Children)
		for i := range out {
			out[i] = image.Rect(r.Min.X, e[i], r.Max.X, e[i+1])
		}
	case Tabbed:
		content := image.Rect(r.Min.X, r.Min.Y+min(tabBarHeight, r.Dy()), r.Max.X, r.Max.Y)
		for i := range out {
			out[i] = content
		}
	}
	return out
}

func (t Tree) rectOf(area image.Rectangle, target *Group) image.Rectangle {
	var find func(g *Group, r image.Rectangle) (image.Rectangle, bool)
	find = func(g *Group, r image.Rectangle) (image.Rectangle, bool) {
		if g == target {
			return r, true
		}
		rects := childRects(g, r, 0)
		for i, c := range g.Children {
			if sub, ok := c.Node.(*Group); ok {
				if found, ok := find(sub, rects[i]); ok {
					return found, true
				}
			}
		}
		return image.Rectangle{}, false
	}
	r, _ := find(t.root, area)
	return r
}

func (t Tree) Arrange(area image.Rectangle, tabBarHeight int) Arrangement {
	var a Arrangement
	var place func(n Node, r image.Rectangle, visible bool)
	place = func(n Node, r image.Rectangle, visible bool) {
		switch n := n.(type) {
		case Window:
			a.Windows = append(a.Windows, Placement{ID: n.ID, Rect: r, Visible: visible})
		case *Group:
			if n.Layout == Tabbed && visible {
				tb := TabBar{Rect: image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+min(tabBarHeight, r.Dy())), Active: n.Focus}
				for _, c := range n.Children {
					tb.Tabs = append(tb.Tabs, firstWindow(c.Node))
				}
				a.TabBars = append(a.TabBars, tb)
			}
			rects := childRects(n, r, tabBarHeight)
			for i, c := range n.Children {
				place(c.Node, rects[i], visible && (n.Layout != Tabbed || i == n.Focus))
			}
		}
	}
	if t.root != nil {
		place(t.root, area, true)
	}
	return a
}
