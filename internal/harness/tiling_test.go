package harness

import (
	"image"
	"testing"

	"github.com/stubbedev/tilde/internal/testclient"
)

func TestWindowsTileSideBySide(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	red := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", red.Mapped)
	if red.Logical != image.Pt(160, 100) {
		t.Fatalf("a lone window is %v, want the whole output", red.Logical)
	}
	blue := c.Window("blue", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("blue to map", blue.Mapped)
	if blue.Logical != image.Pt(80, 100) {
		t.Fatalf("the second window is %v, want half the output", blue.Logical)
	}
	c.DispatchUntil("red to shrink", func() bool { return red.Logical == image.Pt(80, 100) })
	h.Eventually("red on the left", showing(10, 10, [3]byte{0xff, 0, 0}))
	h.Eventually("blue on the right", showing(150, 10, [3]byte{0, 0, 0xff}))
	if err := blue.Destroy(); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("red to grow back", func() bool { return red.Logical == image.Pt(160, 100) })
	h.Eventually("red over the whole output", showing(150, 10, [3]byte{0xff, 0, 0}))
}
