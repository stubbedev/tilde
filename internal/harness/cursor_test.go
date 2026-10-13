package harness

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/golden"
	ccs "github.com/stubbedev/tilde/internal/protocol/client/cursorshape"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/testenv"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/xcursor"
)

func arrowAt(tip image.Point) func(*headless.Backend, *compositor.Compositor) bool {
	arrow := xcursor.Builtin(compositor.DefaultCursorSize)
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		snap := b.Outputs()[0].Snapshot()
		for y := range arrow.Size.Y {
			for x := range arrow.Size.X {
				p := arrow.Pixels[(y*arrow.Size.X+x)*4:]
				if p[3] == 0xff && pixel(snap, tip.X+x, tip.Y+y) != [3]byte{p[2], p[1], p[0]} {
					return false
				}
			}
		}
		return true
	}
}

func TestBuiltinArrowFollowsThePointer(t *testing.T) {
	h := Start(t, small)
	h.Eventually("the arrow at the pointer", arrowAt(image.Pt(80, 50)))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], 0.1, 0.1), b.Pointer().Frame())
	})
	h.Eventually("the arrow at its new place", arrowAt(image.Pt(16, 10)))
	h.Eventually("the old place cleared", showing(81, 58, [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}))
}

func TestClientCursorSurfaceAndHiding(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	cursor := r.c.Surface()
	if err := errors.Join(cursor.Attach(r.c.Buffer(image.Pt(8, 8), 0, testclient.Fill(0, 0, 0xff)), 0, 0), cursor.Commit(), r.in.Pointer.SetCursor(r.in.Serial, cursor, 4, 4)); err != nil {
		t.Fatal(err)
	}
	r.c.DispatchUntil("the cursor request", func() bool { return true })
	r.h.Eventually("the client cursor centred on the pointer", showing(77, 47, [3]byte{0xff, 0, 0}))
	if got := pixel(r.h.Snapshot(0), 85, 55); got == [3]byte{0xff, 0, 0} {
		t.Fatal("the cursor is larger than its 8x8 buffer")
	}
	if err := r.in.Pointer.SetCursor(r.in.Serial, nil, 0, 0); err != nil {
		t.Fatal(err)
	}
	r.c.DispatchUntil("the hide request", func() bool { return true })
	r.h.Eventually("the window under a hidden cursor", showing(80, 50, [3]byte{0xff, 0xff, 0}))
}

func TestCursorShapeFromTheThemeAtFractionalScale(t *testing.T) {
	theme := testenv.CursorTheme(t)
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Cursor.Theme = theme }, scaled)
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		return c.SetOutputScale(b.Outputs()[0], 180)
	})
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("shape", testclient.Fill(0x40, 0x40, 0x40))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], 0.5, 0.5), b.Pointer().Frame())
	})
	c.DispatchUntil("the pointer to enter", func() bool { return in.Serial != 0 })
	device, err := c.CursorShape.GetPointer(in.Pointer)
	if err != nil {
		t.Fatal(err)
	}
	if err := device.SetShape(in.Serial, ccs.WpCursorShapeDeviceV1ShapePointer); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the shape request", func() bool { return true })
	h.Eventually("the window under the pointer shape", func(b *headless.Backend, c *compositor.Compositor) bool {
		return pixel(b.Outputs()[0].Snapshot(), 2, 2) == [3]byte{0x40, 0x40, 0x40} && c.Seat().Cursor().Shape() == "pointer"
	})
	golden.Assert(t, "cursor-shape", h.Rendered(0))
	if err := device.SetShape(in.Serial, 99); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := c.Display.Roundtrip(); !errors.As(err, &pe) || pe.Code != uint32(ccs.WpCursorShapeDeviceV1ErrorInvalidShape) {
		t.Fatalf("an invalid shape = %v, want invalid_shape", err)
	}
}
