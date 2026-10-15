package harness

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	ccs "github.com/stubbedev/tilde/internal/protocol/client/cursorshape"
	cel "github.com/stubbedev/tilde/internal/protocol/client/extforeigntoplevellist"
	ceis "github.com/stubbedev/tilde/internal/protocol/client/extimagecapturesource"
	ceic "github.com/stubbedev/tilde/internal/protocol/client/extimagecopycapture"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/testenv"
)

type captureView struct {
	size    image.Point
	formats []cw.WlShmFormat
	done    bool
	stopped bool
}

func (v *captureView) BufferSize(_ *ceic.ExtImageCopyCaptureSessionV1, w, h uint32) error {
	v.size, v.formats, v.done = image.Pt(int(w), int(h)), nil, false
	return nil
}

func (v *captureView) ShmFormat(_ *ceic.ExtImageCopyCaptureSessionV1, f cw.WlShmFormat) error {
	v.formats = append(v.formats, f)
	return nil
}

func (v *captureView) DmabufDevice(*ceic.ExtImageCopyCaptureSessionV1, []byte) error { return nil }

func (v *captureView) DmabufFormat(*ceic.ExtImageCopyCaptureSessionV1, uint32, []byte) error {
	return nil
}

func (v *captureView) Done(*ceic.ExtImageCopyCaptureSessionV1) error {
	v.done = true
	return nil
}

func (v *captureView) Stopped(*ceic.ExtImageCopyCaptureSessionV1) error {
	v.stopped = true
	return nil
}

type capturedFrame struct {
	ready, failed bool
	reason        ceic.ExtImageCopyCaptureFrameV1FailureReason
	damage        image.Rectangle
}

func (f *capturedFrame) Transform(*ceic.ExtImageCopyCaptureFrameV1, cw.WlOutputTransform) error {
	return nil
}

func (f *capturedFrame) Damage(_ *ceic.ExtImageCopyCaptureFrameV1, x, y, w, h int32) error {
	f.damage = f.damage.Union(image.Rect(int(x), int(y), int(x+w), int(y+h)))
	return nil
}

func (f *capturedFrame) PresentationTime(*ceic.ExtImageCopyCaptureFrameV1, uint32, uint32, uint32) error {
	return nil
}

func (f *capturedFrame) Ready(*ceic.ExtImageCopyCaptureFrameV1) error {
	f.ready = true
	return nil
}

func (f *capturedFrame) Failed(_ *ceic.ExtImageCopyCaptureFrameV1, reason ceic.ExtImageCopyCaptureFrameV1FailureReason) error {
	f.failed, f.reason = true, reason
	return nil
}

func grab(t *testing.T, c *testclient.Client, session *ceic.ExtImageCopyCaptureSessionV1, size image.Point) (*capturedFrame, *ceic.ExtImageCopyCaptureFrameV1, []byte) {
	t.Helper()
	f := &capturedFrame{}
	frame, err := session.CreateFrame(f)
	if err != nil {
		t.Fatal(err)
	}
	wl, pix := c.Target(size)
	if err := errors.Join(frame.AttachBuffer(wl), frame.DamageBuffer(0, 0, int32(size.X), int32(size.Y)), frame.Capture(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	return f, frame, pix
}

type captureRig struct {
	t         *testing.T
	h         *Harness
	c         *testclient.Client
	outputs   *ceis.ExtOutputImageCaptureSourceManagerV1
	toplevels *ceis.ExtForeignToplevelImageCaptureSourceManagerV1
	mgr       *ceic.ExtImageCopyCaptureManagerV1
	list      *extWatcher
}

func newCaptureRig(t *testing.T, h *Harness, c *testclient.Client) *captureRig {
	t.Helper()
	r := &captureRig{t: t, h: h, c: c, list: &extWatcher{toplevels: map[*cel.ExtForeignToplevelHandleV1]*extToplevel{}}}
	var err error
	if r.outputs, err = ceis.NewExtOutputImageCaptureSourceManagerV1(c.Display, 1); err != nil {
		t.Fatal(err)
	}
	if r.toplevels, err = ceis.NewExtForeignToplevelImageCaptureSourceManagerV1(c.Display, 1); err != nil {
		t.Fatal(err)
	}
	if r.mgr, err = ceic.NewExtImageCopyCaptureManagerV1(c.Display, 1); err != nil {
		t.Fatal(err)
	}
	list, err := cel.NewExtForeignToplevelListV1(c.Display, 1, r.list)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("ext_output_image_capture_source_manager_v1", r.outputs)
	c.Bind("ext_foreign_toplevel_image_capture_source_manager_v1", r.toplevels)
	c.Bind("ext_image_copy_capture_manager_v1", r.mgr)
	c.Bind("ext_foreign_toplevel_list_v1", list)
	return r
}

func (r *captureRig) session(src *ceis.ExtImageCaptureSourceV1, options ceic.ExtImageCopyCaptureManagerV1Options) (*ceic.ExtImageCopyCaptureSessionV1, *captureView) {
	r.t.Helper()
	view := &captureView{}
	session, err := r.mgr.CreateSession(view, src, options)
	if err != nil {
		r.t.Fatal(err)
	}
	r.c.DispatchUntil("the capture constraints", func() bool { return view.done })
	return session, view
}

func (r *captureRig) outputSession(options ceic.ExtImageCopyCaptureManagerV1Options) (*ceic.ExtImageCopyCaptureSessionV1, *captureView) {
	r.t.Helper()
	src, err := r.outputs.CreateSource(r.c.Outputs[0])
	if err != nil {
		r.t.Fatal(err)
	}
	return r.session(src, options)
}

func (r *captureRig) windowSession() (*ceic.ExtImageCopyCaptureSessionV1, *captureView) {
	r.t.Helper()
	r.c.DispatchUntil("the window listed", func() bool { return len(r.list.order) == 1 })
	src, err := r.toplevels.CreateSource(r.list.order[0])
	if err != nil {
		r.t.Fatal(err)
	}
	return r.session(src, 0)
}

func (r *captureRig) first(session *ceic.ExtImageCopyCaptureSessionV1, size image.Point) []byte {
	r.t.Helper()
	f, frame, pix := grab(r.t, r.c, session, size)
	r.c.DispatchUntil("the first frame", func() bool { return f.ready || f.failed })
	if f.failed || f.damage != (image.Rectangle{Max: size}) {
		r.t.Fatalf("first frame %+v", *f)
	}
	if err := errors.Join(frame.Destroy(), r.c.Display.Flush()); err != nil {
		r.t.Fatal(err)
	}
	return pix
}

func (r *captureRig) waiting(session *ceic.ExtImageCopyCaptureSessionV1, size image.Point) (*capturedFrame, []byte) {
	r.t.Helper()
	f, _, pix := grab(r.t, r.c, session, size)
	settle(r.t, r.h)
	if err := r.c.Display.Roundtrip(); err != nil || f.ready {
		r.t.Fatalf("a frame was ready without damage (%v)", err)
	}
	return f, pix
}

func TestImageCopyCaptureOfOutputsAndWindows(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("captured", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	r := newCaptureRig(t, h, c)

	session, view := r.outputSession(0)
	if view.size != image.Pt(160, 100) || len(view.formats) != 2 {
		t.Fatalf("constraints %+v", *view)
	}
	if pix := r.first(session, view.size); rgbAt(pix, 160*4, 20, 20) != [3]byte{0xff, 0, 0} {
		t.Fatalf("the output frame shows %v", rgbAt(pix, 160*4, 20, 20))
	}
	f, _ := r.waiting(session, view.size)
	if err := errors.Join(w.Surface.Attach(c.Buffer(w.Logical, 1, quadrants), 0, 0), w.Surface.Damage(0, 0, 4, 4), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the second output frame after damage", func() bool { return f.ready })
	if !image.Rect(0, 0, 4, 4).In(f.damage) || f.damage == image.Rect(0, 0, 160, 100) {
		t.Fatalf("the second frame reported damage %v", f.damage)
	}

	wsession, wview := r.windowSession()
	if wview.size != image.Pt(160, 100) {
		t.Fatalf("window constraints %+v", *wview)
	}
	if pix := r.first(wsession, wview.size); rgbAt(pix, 160*4, 140, 80) != [3]byte{0xff, 0xff, 0} {
		t.Fatalf("the window frame shows %v", rgbAt(pix, 160*4, 140, 80))
	}
	if err := errors.Join(w.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the window session stopped", func() bool { return wview.stopped })
}

func TestImageCopyCaptureOfAHiddenWindow(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("hidden", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	r := newCaptureRig(t, h, c)
	h.dispatch("workspace 2")
	settle(t, h)

	session, view := r.windowSession()
	if pix := r.first(session, view.size); rgbAt(pix, 160*4, 140, 80) != [3]byte{0xff, 0xff, 0} {
		t.Fatalf("the hidden window's frame shows %v", rgbAt(pix, 160*4, 140, 80))
	}
	f, pix := r.waiting(session, view.size)
	presents := presentCount(h)
	if err := errors.Join(w.Surface.Attach(c.Buffer(w.Logical, 1, testclient.Fill(0xff, 0, 0)), 0, 0), w.Surface.Damage(0, 0, 160, 100), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("a frame of the hidden window's new content", func() bool { return f.ready || f.failed })
	if f.failed || rgbAt(pix, 160*4, 140, 80) != [3]byte{0, 0, 0xff} {
		t.Fatalf("frame %+v shows %v", *f, rgbAt(pix, 160*4, 140, 80))
	}
	if n := presentCount(h); n != presents {
		t.Fatalf("a hidden window's commit presented %d frames", n-presents)
	}
}

func TestImageCopyCaptureFollowsAPlaneCursor(t *testing.T) {
	h := StartWithCursorPlanes(t, plane, func(*compositor.Config) {}, small)
	h.Eventually("the arrow on the cursor plane", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Cursor().Visible()
	})
	c := testclient.Connect(t, h.Socket())
	settle(t, h)
	r := newCaptureRig(t, h, c)
	session, view := r.outputSession(ceic.ExtImageCopyCaptureManagerV1OptionsPaintCursors)
	r.first(session, view.size)
	f, _ := r.waiting(session, view.size)
	presents := presentCount(h)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], 0.25, 0.2), b.Pointer().Frame())
	})
	c.DispatchUntil("a frame after the plane cursor moved", func() bool { return f.ready || f.failed })
	if f.failed || !image.Pt(80, 50).In(f.damage) || !image.Pt(40, 20).In(f.damage) {
		t.Fatalf("frame %+v does not cover both cursor positions", *f)
	}
	if n := presentCount(h); n != presents {
		t.Fatalf("moving the plane cursor presented %d frames", n-presents)
	}
}

func TestImageCopyCaptureIntoDMABUFs(t *testing.T) {
	h := StartGLES(t, func(*compositor.Config) {}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("captured", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	settle(t, h)
	outputs, err := ceis.NewExtOutputImageCaptureSourceManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := ceic.NewExtImageCopyCaptureManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("ext_output_image_capture_source_manager_v1", outputs)
	c.Bind("ext_image_copy_capture_manager_v1", mgr)
	src, err := outputs.CreateSource(c.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	view := &captureView{}
	session, err := mgr.CreateSession(view, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the constraints", func() bool { return view.done })
	target := c.DMABUF(view.size, testclient.Fill(0, 0, 0))
	f := &capturedFrame{}
	frame, err := session.CreateFrame(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(frame.AttachBuffer(target.WL), frame.Capture(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the dmabuf frame", func() bool { return f.ready || f.failed })
	if f.failed || rgbAt(target.Pix, 160*4, 20, 20) != [3]byte{0xff, 0, 0} || rgbAt(target.Pix, 160*4, 140, 80) != [3]byte{0xff, 0xff, 0} {
		t.Fatalf("dmabuf frame %+v: %v and %v", *f, rgbAt(target.Pix, 160*4, 20, 20), rgbAt(target.Pix, 160*4, 140, 80))
	}
}

type cursorWatch struct{ events []string }

func (w *cursorWatch) Enter(*ceic.ExtImageCopyCaptureCursorSessionV1) error {
	w.events = append(w.events, "enter")
	return nil
}

func (w *cursorWatch) Leave(*ceic.ExtImageCopyCaptureCursorSessionV1) error {
	w.events = append(w.events, "leave")
	return nil
}

func (w *cursorWatch) Position(_ *ceic.ExtImageCopyCaptureCursorSessionV1, x, y int32) error {
	w.events = append(w.events, fmt.Sprintf("position %d,%d", x, y))
	return nil
}

func (w *cursorWatch) Hotspot(_ *ceic.ExtImageCopyCaptureCursorSessionV1, x, y int32) error {
	w.events = append(w.events, fmt.Sprintf("hotspot %d,%d", x, y))
	return nil
}

func TestCursorSessionsFollowThePointer(t *testing.T) {
	theme := testenv.CursorTheme(t)
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Cursor.Theme = theme }, small, small)
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("pointed", testclient.Fill(0x40, 0x40, 0x40))
	c.DispatchUntil("the window to map", w.Mapped)
	c.DispatchUntil("the pointer to enter", func() bool { return in.Serial != 0 })
	quiesce(t, h, c, w)
	r := newCaptureRig(t, h, c)
	src, err := r.outputs.CreateSource(c.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	watch := &cursorWatch{}
	k, err := r.mgr.CreatePointerCursorSession(watch, src, in.Pointer)
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the cursor entering the output", func() bool { return len(watch.events) == 3 })
	if watch.events[0] != "enter" || watch.events[1] != "position 80,50" || !strings.HasPrefix(watch.events[2], "hotspot ") {
		t.Fatalf("cursor events %q", watch.events)
	}
	view := &captureView{}
	session, err := k.GetCaptureSession(view)
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the cursor image constraints", func() bool { return view.done })
	pix := r.first(session, view.size)
	if !slices.ContainsFunc(pix, func(b byte) bool { return b != 0 }) {
		t.Fatalf("the %v cursor frame is empty", view.size)
	}
	f, _ := r.waiting(session, view.size)

	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], 0.25, 0.2), b.Pointer().Frame())
	})
	c.DispatchUntil("the new position", func() bool { return slices.Contains(watch.events, "position 40,20") })
	settle(t, h)
	if err := c.Display.Roundtrip(); err != nil || f.ready {
		t.Fatalf("moving the cursor produced a cursor image frame (%v)", err)
	}
	device, err := c.CursorShape.GetPointer(in.Pointer)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(device.SetShape(in.Serial, ccs.WpCursorShapeDeviceV1ShapeText), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("a frame of the new cursor image", func() bool { return f.ready || f.failed })
	if f.failed {
		t.Fatalf("the cursor image frame failed: %+v", *f)
	}

	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().MoveTo(b.Outputs()[1], 0.5, 0.5), b.Pointer().Frame())
	})
	c.DispatchUntil("the cursor leaving the output", func() bool { return watch.events[len(watch.events)-1] == "leave" })
}

func TestADestroyedWaitingFrameLeavesTheDamageToTheNext(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("captured", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	r := newCaptureRig(t, h, c)
	session, view := r.outputSession(0)
	r.first(session, view.size)

	gone, frame, gonePix := grab(t, c, session, view.size)
	settle(t, h)
	if err := errors.Join(frame.Destroy(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	next, _ := r.waiting(session, view.size)
	if err := errors.Join(w.Surface.Attach(c.Buffer(w.Logical, 1, quadrants), 0, 0), w.Surface.Damage(0, 0, 4, 4), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the next frame after damage", func() bool { return next.ready || next.failed })
	if next.failed || !image.Rect(0, 0, 4, 4).In(next.damage) {
		t.Fatalf("the next frame %+v", *next)
	}
	if gone.ready || gone.failed || slices.ContainsFunc(gonePix, func(b byte) bool { return b != 0 }) {
		t.Fatalf("a destroyed frame was completed (%+v) or painted", *gone)
	}
}

func TestAnImageCopyFrameCapturesOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		wait bool
	}{{"while it waits", true}, {"once it is ready", false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := Start(t, small)
			c := testclient.Connect(t, h.Socket())
			w := c.Window("captured", quadrants)
			c.DispatchUntil("the window to map", w.Mapped)
			quiesce(t, h, c, w)
			r := newCaptureRig(t, h, c)
			session, view := r.outputSession(0)
			var frame *ceic.ExtImageCopyCaptureFrameV1
			if tc.wait {
				r.first(session, view.size)
				f, fr, _ := grab(t, c, session, view.size)
				settle(t, h)
				if err := c.Display.Roundtrip(); err != nil || f.ready {
					t.Fatalf("a frame was ready without damage (%v)", err)
				}
				frame = fr
			} else {
				f, fr, _ := grab(t, c, session, view.size)
				c.DispatchUntil("the frame", func() bool { return f.ready || f.failed })
				frame = fr
			}
			if err := frame.Capture(); err != nil {
				t.Fatal(err)
			}
			c.ExpectProtocolError(uint32(ceic.ExtImageCopyCaptureFrameV1ErrorAlreadyCaptured))
		})
	}
}

func TestACaptureOfAClosedToplevelStops(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("closing", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	r := newCaptureRig(t, h, c)
	c.DispatchUntil("the window listed", func() bool { return len(r.list.order) == 1 })
	handle := r.list.order[0]
	if err := errors.Join(w.Destroy(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	src, err := r.toplevels.CreateSource(handle)
	if err != nil {
		t.Fatal(err)
	}
	view := &captureView{}
	session, err := r.mgr.CreateSession(view, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the session stopped", func() bool { return view.stopped })
	f, _, _ := grab(t, c, session, image.Pt(160, 100))
	c.DispatchUntil("the frame to fail", func() bool { return f.failed || f.ready })
	if f.ready || f.reason != ceic.ExtImageCopyCaptureFrameV1FailureReasonStopped {
		t.Fatalf("a frame of a closed toplevel: %+v", *f)
	}
}
