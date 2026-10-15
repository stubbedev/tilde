package harness

import (
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/golden"
	ccs "github.com/stubbedev/tilde/internal/protocol/client/cursorshape"
	"github.com/stubbedev/tilde/internal/seat"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/testenv"
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
	r.c.Sync()
	r.h.Eventually("the client cursor centred on the pointer", showing(77, 47, [3]byte{0xff, 0, 0}))
	if got := pixel(r.h.Snapshot(0), 85, 55); got == [3]byte{0xff, 0, 0} {
		t.Fatal("the cursor is larger than its 8x8 buffer")
	}
	if err := errors.Join(cursor.Offset(2, 2), cursor.Commit(), r.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.h.Eventually("the cursor moved by its offset, the hotspot with it", showing(85, 55, [3]byte{0xff, 0, 0}))
	if got := pixel(r.h.Snapshot(0), 81, 46); got == [3]byte{0xff, 0, 0} {
		t.Fatal("the cursor kept its old hotspot after a surface offset")
	}
	if err := r.in.Pointer.SetCursor(r.in.Serial, nil, 0, 0); err != nil {
		t.Fatal(err)
	}
	r.c.Sync()
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
	c.Sync()
	h.Eventually("the window under the pointer shape", func(b *headless.Backend, c *compositor.Compositor) bool {
		return pixel(b.Outputs()[0].Snapshot(), 2, 2) == [3]byte{0x40, 0x40, 0x40} && c.Seat().Cursor() == seat.ShapeCursor{Shape: "pointer"}
	})
	golden.Assert(t, "cursor-shape", h.Rendered(0))
	if err := device.SetShape(in.Serial, 99); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(ccs.WpCursorShapeDeviceV1ErrorInvalidShape))
}

var plane = image.Pt(64, 64)

type planeState struct{ presents, frames, images, moves uint64 }

func planeOf(h *Harness) planeState {
	var s planeState
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		s = planeState{o.Presents(), o.Frames(), o.Cursor().Images(), o.Cursor().Moves()}
		return nil
	})
	return s
}

func TestCursorPlaneMovesWithoutRepainting(t *testing.T) {
	h := StartWithCursorPlanes(t, plane, func(*compositor.Config) {}, small)
	h.Eventually("the arrow on the cursor plane", func(b *headless.Backend, c *compositor.Compositor) bool {
		return b.Outputs()[0].Cursor().Visible() && arrowAt(image.Pt(80, 50))(b, c)
	})
	settle(t, h)
	h.Rendered(0)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if got := pixel(b.Outputs()[0].Primary(), 81, 58); got != [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B} {
			t.Errorf("the frame itself drew the cursor: %v", got)
		}
		return nil
	})
	settle(t, h)
	before := planeOf(h)
	for _, at := range [][2]float64{{0.1, 0.1}, {0.3, 0.2}, {0.5, 0.5}} {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], at[0], at[1]), b.Pointer().Frame())
		})
	}
	h.Eventually("the plane back at the centre", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Cursor().Position() == image.Pt(80, 50)
	})
	settle(t, h)
	after := planeOf(h)
	if after.presents != before.presents || after.frames != before.frames || after.images != before.images || after.moves != before.moves+3 {
		t.Fatalf("three pointer moves: before %+v, after %+v", before, after)
	}
}

func TestClientCursorsOnThePlaneAndInTheFrame(t *testing.T) {
	r := inputOn(t, StartWithCursorPlanes(t, plane, func(*compositor.Config) {}, small))
	r.expect("kb_enter win", "enter win 80,50", "frame")
	cursor := r.c.Surface()
	attach := func(size image.Point, b, g, red byte) {
		t.Helper()
		if err := errors.Join(cursor.Attach(r.c.Buffer(size, 0, testclient.Fill(b, g, red)), 0, 0), cursor.DamageBuffer(0, 0, int32(size.X), int32(size.Y)), cursor.Commit()); err != nil {
			t.Fatal(err)
		}
		r.c.Sync()
	}
	attach(image.Pt(8, 8), 0, 0, 0xff)
	if err := r.in.Pointer.SetCursor(r.in.Serial, cursor, 4, 4); err != nil {
		t.Fatal(err)
	}
	r.c.Sync()
	r.h.Eventually("the client cursor on the plane", func(b *headless.Backend, c *compositor.Compositor) bool {
		cur := b.Outputs()[0].Cursor()
		return cur.Visible() && showing(77, 47, [3]byte{0xff, 0, 0})(b, c)
	})
	images := planeOf(r.h).images
	attach(image.Pt(8, 8), 0, 0xff, 0)
	r.h.Eventually("the recoloured cursor on the plane", showing(77, 47, [3]byte{0, 0xff, 0}))
	if planeOf(r.h).images != images+1 {
		t.Fatal("a cursor commit did not load one new image")
	}
	attach(image.Pt(100, 80), 0xff, 0, 0)
	r.h.Eventually("a cursor too big for the plane drawn in the frame", func(b *headless.Backend, c *compositor.Compositor) bool {
		return !b.Outputs()[0].Cursor().Visible() && pixel(b.Outputs()[0].Snapshot(), 150, 90) == [3]byte{0, 0, 0xff}
	})
	if err := r.in.Pointer.SetCursor(r.in.Serial, nil, 0, 0); err != nil {
		t.Fatal(err)
	}
	r.c.Sync()
	r.h.Eventually("the window under a hidden cursor", showing(80, 50, [3]byte{0xff, 0xff, 0}))
}

func TestARefusingCursorPlaneFallsBackToTheFrame(t *testing.T) {
	reports := make(chan error, 4)
	h := StartWithCursorPlanes(t, plane, func(cfg *compositor.Config) { cfg.Report = func(err error) { reports <- err } }, small)
	h.Eventually("the arrow on the cursor plane", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Cursor().Visible()
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.Outputs()[0].Cursor().Refuse(errors.New("plane gone"))
		return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], 0.1, 0.1), b.Pointer().Frame())
	})
	select {
	case err := <-reports:
		if !strings.Contains(err.Error(), "drawing the cursor in the frame on HEADLESS-1: plane gone") {
			t.Fatalf("reported %v", err)
		}
	case <-time.After(clientTimeout):
		t.Fatal("the refusal was not reported")
	}
	h.Eventually("the arrow drawn in the frame", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return !b.Outputs()[0].Cursor().Visible() && arrowAt(image.Pt(16, 10))(b, nil)
	})
}
