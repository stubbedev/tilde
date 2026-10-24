package harness

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/golden"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func TestBordersAndTabBars(t *testing.T) {
	decor := compositor.DefaultDecor()
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Decor = decor }, tall)
	c := testclient.Connect(t, h.Socket())
	gray := testclient.Fill(0x60, 0x60, 0x60)
	left := c.Window("left", gray)
	c.DispatchUntil("left to map", left.Mapped)
	if left.Logical != image.Pt(158, 118) {
		t.Fatalf("a lone window is %v, want the output less a 1px border", left.Logical)
	}
	right := c.Window("right", gray)
	c.DispatchUntil("right to map", right.Mapped)
	active := [3]byte{decor.ActiveBorder.R, decor.ActiveBorder.G, decor.ActiveBorder.B}
	inactive := [3]byte{decor.InactiveBorder.R, decor.InactiveBorder.G, decor.InactiveBorder.B}
	h.Eventually("the focused window's border", showing(80, 100, active))
	h.Eventually("the other window's border", showing(79, 100, inactive))

	h.dispatch("makegroup tab")
	third := c.Window("third", gray)
	c.DispatchUntil("third to map", third.Mapped)
	if want := image.Pt(78, 98); third.Logical != want {
		t.Fatalf("a tabbed window is %v, want %v below the 20px tab strip", third.Logical, want)
	}
	tabActive := [3]byte{decor.Tab.Active.Background.R, decor.Tab.Active.Background.G, decor.Tab.Active.Background.B}
	h.Eventually("the active tab on the right half", showing(158, 2, tabActive))
	golden.Assert(t, "decor", h.Rendered(0))
}

var tall = backend.Mode{Size: image.Pt(160, 120), RefreshMHz: 60000, Preferred: true}
