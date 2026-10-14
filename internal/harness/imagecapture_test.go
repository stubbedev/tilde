package harness

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/tilde/internal/compositor"
	cel "github.com/stubbedev/tilde/internal/protocol/client/extforeigntoplevellist"
	ceis "github.com/stubbedev/tilde/internal/protocol/client/extimagecapturesource"
	ceic "github.com/stubbedev/tilde/internal/protocol/client/extimagecopycapture"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/testclient"
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

func TestImageCopyCaptureOfOutputsAndWindows(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("captured", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)

	outputs, err := ceis.NewExtOutputImageCaptureSourceManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	toplevels, err := ceis.NewExtForeignToplevelImageCaptureSourceManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := ceic.NewExtImageCopyCaptureManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	list, err := cel.NewExtForeignToplevelListV1(c.Display, 1, &extWatcher{toplevels: map[*cel.ExtForeignToplevelHandleV1]*extToplevel{}})
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("ext_output_image_capture_source_manager_v1", outputs)
	c.Bind("ext_foreign_toplevel_image_capture_source_manager_v1", toplevels)
	c.Bind("ext_image_copy_capture_manager_v1", mgr)
	c.Bind("ext_foreign_toplevel_list_v1", list)

	src, err := outputs.CreateSource(c.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	view := &captureView{}
	session, err := mgr.CreateSession(view, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the output constraints", func() bool { return view.done })
	if view.size != image.Pt(160, 100) || len(view.formats) != 2 {
		t.Fatalf("constraints %+v", *view)
	}
	f, frame, pix := grab(t, c, session, view.size)
	c.DispatchUntil("the first output frame", func() bool { return f.ready || f.failed })
	if f.failed || f.damage != image.Rect(0, 0, 160, 100) || rgbAt(pix, 160*4, 20, 20) != [3]byte{0xff, 0, 0} {
		t.Fatalf("frame %+v, pixel %v", *f, rgbAt(pix, 160*4, 20, 20))
	}
	if err := errors.Join(frame.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	f, _, _ = grab(t, c, session, view.size)
	settle(t, h)
	if err := c.Display.Roundtrip(); err != nil || f.ready {
		t.Fatalf("a second frame was ready without damage (%v)", err)
	}
	if err := errors.Join(w.Surface.Attach(c.Buffer(w.Logical, 1, quadrants), 0, 0), w.Surface.Damage(0, 0, 4, 4), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the second output frame after damage", func() bool { return f.ready })
	if !image.Rect(0, 0, 4, 4).In(f.damage) || f.damage == image.Rect(0, 0, 160, 100) {
		t.Fatalf("the second frame reported damage %v", f.damage)
	}

	ext := list.Handler().(*extWatcher)
	c.DispatchUntil("the window listed", func() bool { return len(ext.order) == 1 })
	wsrc, err := toplevels.CreateSource(ext.order[0])
	if err != nil {
		t.Fatal(err)
	}
	wview := &captureView{}
	wsession, err := mgr.CreateSession(wview, wsrc, 0)
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the window constraints", func() bool { return wview.done })
	if wview.size != image.Pt(160, 100) {
		t.Fatalf("window constraints %+v", *wview)
	}
	wf, _, wpix := grab(t, c, wsession, wview.size)
	c.DispatchUntil("the window frame", func() bool { return wf.ready || wf.failed })
	if wf.failed || rgbAt(wpix, 160*4, 140, 80) != [3]byte{0xff, 0xff, 0} {
		t.Fatalf("window frame %+v, pixel %v", *wf, rgbAt(wpix, 160*4, 140, 80))
	}
	if err := errors.Join(w.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the window session stopped", func() bool { return wview.stopped })
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
