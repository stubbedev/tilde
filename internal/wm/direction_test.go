package wm

import (
	"testing"
)

func nested(t *testing.T) Tree {
	t.Helper()
	tree := build(t, 1, 2, 4)
	tree = on(t)(tree.Wrap(2, SplitV, false))
	tree = on(t)(tree.Focus(2))
	return on(t)(tree.Insert(3, SplitH))
}

func TestNeighbor(t *testing.T) {
	tabs := on(t)(build(t, 1, 2).Wrap(2, Tabbed, false))
	tabs = on(t)(tabs.Insert(3, SplitH))
	tabs = on(t)(tabs.Focus(1))
	cases := []struct {
		name string
		tree Tree
		from WindowID
		d    Direction
		want WindowID
		ok   bool
	}{
		{"right in a split", build(t, 1, 2, 3), 2, Right, 3, true},
		{"left in a split", build(t, 1, 2, 3), 2, Left, 1, true},
		{"up against the edge", build(t, 1, 2, 3), 2, Up, 0, false},
		{"right edge", build(t, 1, 2, 3), 3, Right, 0, false},
		{"out of a vertical group", nested(t), 3, Right, 4, true},
		{"into a vertical group lands on its focused child", nested(t), 4, Left, 3, true},
		{"down within the vertical group", nested(t), 2, Down, 3, true},
		{"into a tab group lands on the active tab", tabs, 1, Right, 3, true},
	}
	for _, c := range cases {
		got, ok, err := c.tree.Neighbor(c.from, c.d)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want || ok != c.ok {
			t.Errorf("%s: %s of %d in %s is %d %t, want %d %t", c.name, c.d, c.from, c.tree, got, ok, c.want, c.ok)
		}
	}
}

func TestMove(t *testing.T) {
	cases := []struct {
		name  string
		tree  func(t *testing.T) Tree
		from  WindowID
		d     Direction
		want  string
		moved bool
	}{
		{"swap with a neighbour", func(t *testing.T) Tree { return build(t, 1, 2, 3) }, 2, Right, "h[1 3 *2]", true},
		{"the workspace edge stops a move", func(t *testing.T) Tree { return build(t, 1, 2, 3) }, 3, Right, "h[1 2 *3]", false},
		{"out of a group to its side", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1, 2).Wrap(2, SplitV, false))
			return on(t)(tree.Insert(3, SplitH))
		}, 3, Left, "h[1 *3 v[*2]]", true},
		{"into a neighbouring group after its focused child", func(t *testing.T) Tree {
			tree := on(t)(build(t, 1, 2).Wrap(2, SplitV, false))
			tree = on(t)(tree.Insert(3, SplitH))
			return on(t)(tree.Focus(1))
		}, 1, Right, "v[2 3 *1]", true},
		{"a lone window replaces its group", func(t *testing.T) Tree {
			return on(t)(build(t, 1, 2).Wrap(2, SplitV, false))
		}, 2, Left, "h[1 *2]", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tree := c.tree(t)
			tree = on(t)(tree.Focus(c.from))
			before := tree.String()
			got, moved, err := tree.Move(c.from, c.d, CollapseEmptySplits)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != c.want || moved != c.moved {
				t.Fatalf("moving %d %s in %s gave %s (moved %t), want %s (moved %t)", c.from, c.d, before, got, moved, c.want, c.moved)
			}
		})
	}
}

func TestFocusCrossesOutputs(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left))
	s = ok(t)(s.AddOutput("DP-2", right))
	s = ok(t)(s.MapWindow(1))
	s = ok(t)(s.FocusDirection(Right))
	if s.FocusedOutput() != "DP-2" {
		t.Fatalf("focus right from the right edge of DP-1 landed on %s", s.FocusedOutput())
	}
	s = ok(t)(s.FocusDirection(Right))
	if s.FocusedOutput() != "DP-2" {
		t.Fatal("focus left the last output")
	}
	s = ok(t)(s.FocusDirection(Left))
	if f, _ := s.Focused(); s.FocusedOutput() != "DP-1" || f != 1 {
		t.Fatalf("focus left came back to %s, window %d", s.FocusedOutput(), f)
	}
}

func TestClosingReturnsToThePreviousFocus(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left))
	for _, id := range []WindowID{1, 2, 3} {
		s = ok(t)(s.MapWindow(id))
	}
	s = ok(t)(s.FocusWindow(1))
	s = ok(t)(s.FocusWindow(3))
	s = ok(t)(s.UnmapWindow(3))
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("closing 3 focused %d, want 1, which had focus before it", f)
	}
	s = ok(t)(s.MoveDirection(Right, CollapseEmptySplits))
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("after a move focus is on %d", f)
	}
}
