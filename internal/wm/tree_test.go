package wm

import (
	"errors"
	"fmt"
	"image"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func describe(n Node) string {
	switch n := n.(type) {
	case Window:
		return fmt.Sprint(n.ID)
	case *Group:
		parts := make([]string, len(n.Children))
		for i, c := range n.Children {
			parts[i] = describe(c.Node)
			if i == n.Focus {
				parts[i] = "*" + parts[i]
			}
		}
		e := ""
		if n.ephemeral != ephemeralOff {
			e = "~"
		}
		return fmt.Sprintf("%s%s[%s]", n.Layout, e, strings.Join(parts, " "))
	}
	return "?"
}

func (t Tree) String() string {
	if t.root == nil {
		return "empty"
	}
	return describe(t.root)
}

func on(t *testing.T) func(Tree, error) Tree {
	return func(tree Tree, err error) Tree {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return tree
	}
}

func build(t *testing.T, ids ...WindowID) Tree {
	t.Helper()
	var tree Tree
	for _, id := range ids {
		tree = on(t)(tree.Insert(id, SplitH))
	}
	return tree
}

func TestInsertGoesAfterTheFocusedWindow(t *testing.T) {
	tree := build(t, 1, 2, 3)
	tree = on(t)(tree.Focus(1))
	tree = on(t)(tree.Insert(4, SplitH))
	if got := tree.String(); got != "h[1 *4 2 3]" {
		t.Fatalf("tree %s", got)
	}
	if _, err := tree.Insert(4, SplitH); err == nil {
		t.Fatal("a window was inserted twice")
	}
	if _, err := tree.Focus(9); !errors.Is(err, ErrUnknownWindow) {
		t.Fatalf("focus of an unknown window: %v", err)
	}
}

func TestRemoveMovesFocusToTheNeighbour(t *testing.T) {
	tree := on(t)(build(t, 1, 2, 3).Focus(2))
	tree = on(t)(tree.Remove(2))
	if got := tree.String(); got != "h[*1 3]" {
		t.Fatalf("removing the focused middle window: %s, want focus on the previous one", got)
	}
	tree = on(t)(on(t)(tree.Focus(1)).Remove(1))
	if got := tree.String(); got != "h[*3]" {
		t.Fatalf("removing the focused first window: %s, want focus on the next one", got)
	}
	tree = on(t)(tree.Remove(3))
	if !tree.Empty() {
		t.Fatalf("tree %s after removing every window", tree)
	}
}

func TestCollapseOnRemoval(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T) Tree
		gone  WindowID
		want  string
	}{
		{"a split keeps a lone window", func(t *testing.T) Tree {
			return on(t)(build(t, 1, 2).Wrap(2, SplitV, false))
		}, 1, "v[*2]"},
		{"a nested split keeps a lone window under the invalid-only rule", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1, 2).Wrap(2, SplitV, false))
			return on(t)(tree.Insert(3, SplitH))
		}, 3, "h[1 *v[*2]]"},
		{"an ephemeral group dissolves", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1, 2).Wrap(2, SplitV, true))
			return on(t)(tree.Insert(3, SplitH))
		}, 3, "h[1 *2]"},
		{"a staged ephemeral group stays until it has held two", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1, 2, 3).Wrap(2, SplitV, true))
			return tree
		}, 3, "h[1 *v~[*2]]"},
		{"the root takes over a lone split of the other direction", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1, 2).Wrap(2, SplitV, false))
			return on(t)(tree.Insert(3, SplitH))
		}, 1, "v[2 *3]"},
		{"an emptied group disappears", func(t *testing.T) Tree {
			return on(t)(build(t, 1, 2, 3).Wrap(3, Tabbed, false))
		}, 3, "h[1 *2]"},
		{"a tab group inside a tab group with one tab collapses", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1).Wrap(1, Tabbed, false))
			tree = on(t)(tree.Wrap(1, Tabbed, false))
			return on(t)(tree.Insert(2, SplitH))
		}, 2, "h[*tab[*1]]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tree := c.build(t)
			before := tree.String()
			got := on(t)(tree.Remove(c.gone)).String()
			if got != c.want {
				t.Fatalf("%s minus %d is %s, want %s", before, c.gone, got, c.want)
			}
		})
	}
}

func TestArrangeRoundsEdgesDeterministically(t *testing.T) {
	tree := build(t, 1, 2, 3)
	a := tree.Arrange(image.Rect(0, 0, 100, 50), 10)
	want := []Placement{
		{1, image.Rect(0, 0, 33, 50), true},
		{2, image.Rect(33, 0, 67, 50), true},
		{3, image.Rect(67, 0, 100, 50), true},
	}
	if !slices.Equal(a.Windows, want) || len(a.TabBars) != 0 {
		t.Fatalf("arranged %v %v", a.Windows, a.TabBars)
	}
	tree = on(t)(tree.Wrap(3, Tabbed, false))
	tree = on(t)(tree.Insert(4, SplitH))
	a = tree.Arrange(image.Rect(0, 0, 100, 50), 10)
	if want := (TabBar{Rect: image.Rect(67, 0, 100, 10), Tabs: []WindowID{3, 4}, Active: 1}); len(a.TabBars) != 1 || a.TabBars[0].Rect != want.Rect || !slices.Equal(a.TabBars[0].Tabs, want.Tabs) || a.TabBars[0].Active != 1 {
		t.Fatalf("tab bars %v, want %v", a.TabBars, want)
	}
	if got := a.Windows[2:]; got[0] != (Placement{3, image.Rect(67, 10, 100, 50), false}) || got[1] != (Placement{4, image.Rect(67, 10, 100, 50), true}) {
		t.Fatalf("tabs placed %v", got)
	}
}

func checkInvariants(t *testing.T, tree Tree, live []WindowID) {
	t.Helper()
	if got := slices.Sorted(slices.Values(tree.Windows())); !slices.Equal(got, slices.Sorted(slices.Values(live))) {
		t.Fatalf("tree %s holds %v, want %v", tree, got, live)
	}
	var check func(g *Group)
	check = func(g *Group) {
		if len(g.Children) == 0 {
			t.Fatalf("tree %s has an empty group", tree)
		}
		if g.Focus < 0 || g.Focus >= len(g.Children) {
			t.Fatalf("tree %s has focus index %d of %d", tree, g.Focus, len(g.Children))
		}
		sum := 0.0
		for _, c := range g.Children {
			sum += c.Ratio
			if sub, ok := c.Node.(*Group); ok {
				check(sub)
			}
		}
		if math.Abs(sum-float64(len(g.Children))) > 1e-9 {
			t.Fatalf("tree %s has ratios summing to %g over %d children", tree, sum, len(g.Children))
		}
	}
	if tree.root != nil {
		check(tree.root)
		if _, ok := tree.Focused(); !ok {
			t.Fatalf("tree %s has no focused window", tree)
		}
	}
	area := image.Rect(3, 7, 1283, 727)
	a := tree.Arrange(area, 0)
	if b := tree.Arrange(area, 0); !slices.Equal(a.Windows, b.Windows) {
		t.Fatalf("arranging %s twice gave different placements", tree)
	}
	covered := 0
	var visible []image.Rectangle
	for _, p := range a.Windows {
		if !p.Visible {
			continue
		}
		if !p.Rect.In(area) {
			t.Fatalf("tree %s placed %d at %v outside %v", tree, p.ID, p.Rect, area)
		}
		for _, other := range visible {
			if other.Overlaps(p.Rect) {
				t.Fatalf("tree %s overlaps %v and %v", tree, other, p.Rect)
			}
		}
		visible = append(visible, p.Rect)
		covered += p.Rect.Dx() * p.Rect.Dy()
	}
	if tree.root != nil && covered != area.Dx()*area.Dy() {
		t.Fatalf("tree %s covers %d of %d pixels", tree, covered, area.Dx()*area.Dy())
	}
}

func TestRandomOperationsKeepTheTreeValid(t *testing.T) {
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 0x7e1de))
		var tree Tree
		var live []WindowID
		next := WindowID(1)
		for step := range 60 {
			var err error
			switch op := rng.IntN(8); {
			case op <= 1 || len(live) == 0:
				tree, err = tree.Insert(next, Layout(1+rng.IntN(2)))
				live = append(live, next)
				next++
			case op == 2:
				i := rng.IntN(len(live))
				tree, err = tree.Remove(live[i])
				live = slices.Delete(live, i, i+1)
			case op == 3:
				tree, err = tree.Focus(live[rng.IntN(len(live))])
			case op >= 6:
				tree, _, err = tree.Move(live[rng.IntN(len(live))], Direction(1+rng.IntN(4)), CollapsePolicy(rng.IntN(3)))
			default:
				tree, err = tree.Wrap(live[rng.IntN(len(live))], Layout(1+rng.IntN(3)), rng.IntN(2) == 0)
			}
			if err != nil {
				t.Fatalf("seed %d step %d: %v", seed, step, err)
			}
			checkInvariants(t, tree, live)
		}
	}
}

func TestAutotileSplitsNarrowColumnsVertically(t *testing.T) {
	at := Autotile{Enabled: true, Ephemeral: true, MinTrigger: 400, Divisor: 3}
	wide := image.Rect(0, 0, 3840, 1080)
	var tree Tree
	for id := range WindowID(5) {
		tree = on(t)(tree.Place(id+1, wide, at))
	}
	if got := tree.String(); got != "h[1 2 *v~[3 4 *5]]" {
		t.Fatalf("five windows on 3840 wide with a 1280 trigger: %s", got)
	}
	narrow := image.Rect(0, 0, 1000, 1200)
	tree = Tree{}
	for id := range WindowID(3) {
		tree = on(t)(tree.Place(id+1, narrow, at))
	}
	if got := tree.String(); got != "v[1 2 *3]" {
		t.Fatalf("a portrait output starts vertical and stays so: %s", got)
	}
	tree = Tree{}
	for id := range WindowID(4) {
		tree = on(t)(tree.Place(id+1, wide, Autotile{}))
	}
	if got := tree.String(); got != "h[1 2 3 *4]" {
		t.Fatalf("without autotile: %s", got)
	}
}
