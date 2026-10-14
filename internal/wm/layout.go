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

func (t Tree) Arrange(area image.Rectangle, tabBarHeight int) Arrangement {
	var a Arrangement
	var place func(n Node, r image.Rectangle, visible bool)
	place = func(n Node, r image.Rectangle, visible bool) {
		switch n := n.(type) {
		case Window:
			a.Windows = append(a.Windows, Placement{ID: n.ID, Rect: r, Visible: visible})
		case *Group:
			switch n.Layout {
			case SplitH:
				e := edges(r.Min.X, r.Dx(), n.Children)
				for i, c := range n.Children {
					place(c.Node, image.Rect(e[i], r.Min.Y, e[i+1], r.Max.Y), visible)
				}
			case SplitV:
				e := edges(r.Min.Y, r.Dy(), n.Children)
				for i, c := range n.Children {
					place(c.Node, image.Rect(r.Min.X, e[i], r.Max.X, e[i+1]), visible)
				}
			case Tabbed:
				bar := min(tabBarHeight, r.Dy())
				tb := TabBar{Rect: image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+bar), Active: n.Focus}
				for _, c := range n.Children {
					tb.Tabs = append(tb.Tabs, firstWindow(c.Node))
				}
				if visible {
					a.TabBars = append(a.TabBars, tb)
				}
				content := image.Rect(r.Min.X, r.Min.Y+bar, r.Max.X, r.Max.Y)
				for i, c := range n.Children {
					place(c.Node, content, visible && i == n.Focus)
				}
			}
		}
	}
	if t.root != nil {
		place(t.root, area, true)
	}
	return a
}
