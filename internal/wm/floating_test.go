package wm

import (
	"image"
	"testing"
)

func layerOf(s State, id WindowID) (PlacementLayer, image.Rectangle, bool) {
	for _, p := range s.Arrange(0).Windows {
		if p.ID == id {
			return p.Layer, p.Rect, p.Visible
		}
	}
	return 0, image.Rectangle{}, false
}

func TestFloatingAndFullscreen(t *testing.T) {
	full := image.Rect(0, 0, 1000, 600)
	area := image.Rect(0, 30, 1000, 600)
	s := ok(t)(State{}.AddOutput("DP-1", full, area))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	s = ok(t)(s.ToggleFloating(Autotile{}))
	if layer, rect, visible := layerOf(s, 2); layer != LayerFloating || rect != image.Rect(250, 172, 750, 457) || !visible {
		t.Fatalf("floated window 2: layer %d at %v visible %t", layer, rect, visible)
	}
	if _, rect, _ := layerOf(s, 1); rect != area {
		t.Fatalf("the tiled window did not take the freed space: %v", rect)
	}
	s = ok(t)(s.FocusWindow(1))
	s = ok(t)(s.FocusDirection(Right))
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("tiled focus wandered to %d; hy3 keeps it among tiled windows", f)
	}
	s = ok(t)(s.FocusWindow(2))
	s = ok(t)(s.FocusDirection(Left))
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("focus left from a floating window went to %d", f)
	}
	s = ok(t)(s.MapFloating(3, image.Rect(10, 40, 110, 140)))
	if f, _ := s.Focused(); f != 3 {
		t.Fatalf("a new floating window is not focused: %d", f)
	}
	s = ok(t)(s.ToggleFullscreen())
	if layer, rect, visible := layerOf(s, 3); layer != LayerFullscreen || rect != full || !visible {
		t.Fatalf("fullscreen window 3: layer %d at %v visible %t", layer, rect, visible)
	}
	if _, _, visible := layerOf(s, 1); visible {
		t.Fatal("a window under a fullscreen one is visible")
	}
	s = ok(t)(s.UnmapWindow(3))
	if _, _, visible := layerOf(s, 1); !visible {
		t.Fatal("closing the fullscreen window did not uncover the others")
	}
	s = ok(t)(s.FocusWindow(2))
	s = ok(t)(s.ToggleFloating(Autotile{}))
	if layer, _, _ := layerOf(s, 2); layer != LayerTiled {
		t.Fatal("toggling a floating window did not tile it again")
	}
}

func TestDropAndResizeTiled(t *testing.T) {
	area := image.Rect(0, 0, 900, 600)
	s := ok(t)(State{}.AddOutput("DP-1", area, area))
	for _, id := range []WindowID{1, 2, 3} {
		s = ok(t)(s.MapWindow(id, Autotile{}))
	}
	s = ok(t)(s.Reinsert(3, image.Pt(50, 300), Autotile{}))
	if got := s.workspace(1).Tree.String(); got != "h[*3 1 2]" {
		t.Fatalf("dropping 3 on the left half of 1: %s", got)
	}
	s = ok(t)(s.Reinsert(3, image.Pt(800, 300), Autotile{}))
	if got := s.workspace(1).Tree.String(); got != "h[1 2 *3]" {
		t.Fatalf("dropping 3 on the right half of 2: %s", got)
	}
	s = ok(t)(s.ResizeTiled(1, Right, 150))
	if _, rect, _ := layerOf(s, 1); rect != image.Rect(0, 0, 450, 600) {
		t.Fatalf("growing 1 to the right by 150: %v", rect)
	}
	s = ok(t)(s.ResizeTiled(2, Left, 10000))
	if _, rect, _ := layerOf(s, 1); rect.Dx() != minTile {
		t.Fatalf("a neighbour shrank below the minimum: %v", rect)
	}
	s = ok(t)(s.ResizeTiled(1, Up, 50))
	if _, rect, _ := layerOf(s, 1); rect.Dy() != 600 {
		t.Fatal("a vertical resize without a vertical split changed the window")
	}
}

func TestResizeFocused(t *testing.T) {
	area := image.Rect(0, 0, 900, 600)
	s := ok(t)(State{}.AddOutput("DP-1", area, area))
	for _, id := range []WindowID{1, 2, 3} {
		s = ok(t)(s.MapWindow(id, Autotile{}))
	}
	s = ok(t)(s.FocusWindow(1))
	s = ok(t)(s.ResizeFocused(50, 0))
	if _, rect, _ := layerOf(s, 1); rect.Dx() != 350 {
		t.Fatalf("growing the first window by 50: %v", rect)
	}
	s = ok(t)(s.FocusWindow(3))
	s = ok(t)(s.ResizeFocused(50, 0))
	if _, rect, _ := layerOf(s, 3); rect != image.Rect(550, 0, 900, 600) {
		t.Fatalf("the last window has no right neighbour, so it grows left: %v", rect)
	}
	s = ok(t)(s.ResizeFocused(0, 40))
	if _, rect, _ := layerOf(s, 3); rect.Dy() != 600 {
		t.Fatal("a vertical resize without a vertical split changed the window")
	}
	s = ok(t)(s.ToggleFloating(Autotile{}))
	s = ok(t)(s.ResizeFocused(-10, 20))
	if _, rect, _ := layerOf(s, 3); rect.Size() != image.Pt(440, 320) {
		t.Fatalf("a floating 450x300 window resized by -10,20: %v", rect.Size())
	}
}
