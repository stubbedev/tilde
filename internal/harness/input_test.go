package harness

import (
	"errors"
	"image"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/testclient"
)

type inputRig struct {
	t            *testing.T
	h            *Harness
	c            *testclient.Client
	seat         *cw.WlSeat
	in           *testclient.Input
	w            *testclient.Window
	seen         int
	childSub     *cw.WlSubsurface
	childSurface *cw.WlSurface
}

func startInput(t *testing.T) *inputRig {
	return inputOn(t, Start(t, small))
}

func inputOn(t *testing.T, h *Harness) *inputRig {
	c := testclient.Connect(t, h.Socket())
	seat, in := c.Seat()
	w := c.Window("win", quadrants)
	in.Name(w.Surface, "win")
	c.DispatchUntil("the window to map", w.Mapped)
	return &inputRig{t: t, h: h, c: c, seat: seat, in: in, w: w}
}

func (r *inputRig) inject(f func(b *headless.Backend, o *headless.Output) error) {
	r.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return f(b, b.Outputs()[0])
	})
}

func (r *inputRig) expect(want ...string) {
	r.t.Helper()
	start := r.seen
	r.c.DispatchUntil(strings.Join(want, ", "), func() bool { return len(r.in.Log) >= start+len(want) })
	if got := r.in.Log[start : start+len(want)]; !slices.Equal(got, want) {
		r.t.Fatalf("events %q, want %q (log %q)", got, want, r.in.Log)
	}
	r.seen = start + len(want)
}

func (r *inputRig) moveTo(x, y float64) {
	r.inject(func(b *headless.Backend, o *headless.Output) error {
		if err := b.Pointer().MoveTo(o, x/160, y/100); err != nil {
			return err
		}
		return b.Pointer().Frame()
	})
}

func (r *inputRig) moveOn(output int, x, y float64) {
	r.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[output]
		return errors.Join(b.Pointer().MoveTo(o, x/160, y/100), b.Pointer().Frame())
	})
}

func TestPointerEntersMovesClicksAndScrolls(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.moveTo(40, 50)
	r.expect("motion 40,50", "frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Pointer().Move(10, 5), b.Pointer().Frame())
	})
	r.expect("motion 50,55", "frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Pointer().Button(272, true), b.Pointer().Frame(), b.Pointer().Button(272, false), b.Pointer().Frame())
	})
	r.expect("button 272 1", "frame", "button 272 0", "frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Pointer().Axis(backend.Vertical, backend.Wheel, 15, 120), b.Pointer().Frame())
	})
	r.expect("axis_source 0", "axis_value120 0 120", "axis_relative_direction 0 0", "axis 0 15", "frame")
}

func (r *inputRig) child() {
	r.t.Helper()
	child := r.c.Surface()
	sub, err := r.c.Subcompositor.GetSubsurface(child, r.w.Surface)
	if err != nil {
		r.t.Fatal(err)
	}
	r.in.Name(child, "child")
	r.childSub, r.childSurface = sub, child
	if err := errors.Join(sub.SetPosition(80, 0), sub.SetDesync(), child.Attach(r.c.Buffer(image.Pt(80, 100), 0, quadrants), 0, 0), child.Commit(), r.w.Surface.Commit()); err != nil {
		r.t.Fatal(err)
	}
	r.c.DispatchUntil("the subsurface to show", func() bool { return true })
	r.h.Eventually("the subsurface on the output", showing(81, 50, [3]byte{0, 0, 0xff}))
}

func TestSubsurfacesGrabsAndInputRegions(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.child()
	r.moveTo(40, 50)
	r.expect("motion 40,50", "frame")
	r.moveTo(120, 50)
	r.expect("leave win", "enter child 40,50", "frame")
	r.moveTo(40, 50)
	r.expect("leave child", "enter win 40,50", "frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Pointer().Button(272, true), b.Pointer().Frame())
	})
	r.expect("button 272 1", "frame")
	r.moveTo(120, 50)
	r.expect("motion 120,50", "frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Pointer().Button(272, false), b.Pointer().Frame())
	})
	r.expect("button 272 0", "frame")
	r.moveTo(121, 50)
	r.expect("leave win", "enter child 41,50", "frame")
	empty, err := r.c.Compositor.CreateRegion()
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(r.childSurface.SetInputRegion(empty), r.childSurface.Commit()); err != nil {
		t.Fatal(err)
	}
	r.c.DispatchUntil("the input region to apply", func() bool { return true })
	r.moveTo(122, 50)
	r.expect("leave child", "enter win 122,50", "frame")
}

func TestKeyboardFocusFollowsTheMouse(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		return c.Seat().SetKeyboardFocus(nil)
	})
	r.expect("kb_leave win")
	r.moveTo(10, 10)
	r.expect("motion 10,10", "kb_enter win", "frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Keyboard().Key(30, true), b.Keyboard().Key(30, false))
	})
	r.expect("key 30 1", "key 30 0")
}

func TestTouchGoesToTheSurfaceUnderThePoint(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.child()
	r.inject(func(b *headless.Backend, o *headless.Output) error {
		return errors.Join(b.Touch().Down(1, o, 0.25, 0.5), b.Touch().Down(2, o, 0.75, 0.5), b.Touch().Frame())
	})
	r.expect("down 1 win 40,50", "down 2 child 40,50", "touch_frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Touch().Motion(1, 0.5, 0.5), b.Touch().Frame())
	})
	r.expect("touch_motion 1 80,50", "touch_frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Touch().Up(1), b.Touch().Frame(), b.Touch().Cancel(2))
	})
	r.expect("up 1", "touch_frame", "cancel")
}

func TestLeavingToTheDesktopEndsThePointerFrame(t *testing.T) {
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("win", quadrants)
	in.Name(w.Surface, "win")
	c.DispatchUntil("the window to map", w.Mapped)
	r := &inputRig{t: t, h: h, c: c, in: in, w: w}
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.moveOn(1, 10, 10)
	r.expect("leave win", "frame")
}
