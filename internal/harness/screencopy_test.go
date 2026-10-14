package harness

import (
	"errors"
	"image"
	"slices"
	"testing"

	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cwsc "github.com/stubbedev/tilde/internal/protocol/client/wlrscreencopy"
	"github.com/stubbedev/tilde/internal/testclient"
)

type copyFrame struct {
	size          image.Point
	stride        uint32
	ready, failed bool
	damage        image.Rectangle
	bufferDone    bool
}

func (f *copyFrame) Buffer(_ *cwsc.ZwlrScreencopyFrameV1, _ cw.WlShmFormat, w, h, stride uint32) error {
	f.size, f.stride = image.Pt(int(w), int(h)), stride
	return nil
}

func (f *copyFrame) Flags(*cwsc.ZwlrScreencopyFrameV1, cwsc.ZwlrScreencopyFrameV1Flags) error {
	return nil
}

func (f *copyFrame) Ready(*cwsc.ZwlrScreencopyFrameV1, uint32, uint32, uint32) error {
	f.ready = true
	return nil
}

func (f *copyFrame) Failed(*cwsc.ZwlrScreencopyFrameV1) error {
	f.failed = true
	return nil
}

func (f *copyFrame) Damage(_ *cwsc.ZwlrScreencopyFrameV1, x, y, w, h uint32) error {
	f.damage = f.damage.Union(image.Rect(int(x), int(y), int(x+w), int(y+h)))
	return nil
}

func (f *copyFrame) LinuxDmabuf(*cwsc.ZwlrScreencopyFrameV1, uint32, uint32, uint32) error {
	return nil
}

func (f *copyFrame) BufferDone(*cwsc.ZwlrScreencopyFrameV1) error {
	f.bufferDone = true
	return nil
}

func rgbAt(pix []byte, stride, x, y int) [3]byte {
	i := y*stride + x*4
	return [3]byte{pix[i+2], pix[i+1], pix[i]}
}

func screenshot(t *testing.T, c *testclient.Client, mgr *cwsc.ZwlrScreencopyManagerV1, region image.Rectangle, damage bool) (*copyFrame, *cwsc.ZwlrScreencopyFrameV1, []byte) {
	t.Helper()
	f := &copyFrame{}
	var frame *cwsc.ZwlrScreencopyFrameV1
	var err error
	if region == (image.Rectangle{}) {
		frame, err = mgr.CaptureOutput(f, 0, c.Outputs[0])
	} else {
		frame, err = mgr.CaptureOutputRegion(f, 0, c.Outputs[0], int32(region.Min.X), int32(region.Min.Y), int32(region.Dx()), int32(region.Dy()))
	}
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the buffer description", func() bool { return f.bufferDone })
	wl, pix := c.Target(f.size)
	if damage {
		err = frame.CopyWithDamage(wl)
	} else {
		err = frame.Copy(wl)
	}
	if err = errors.Join(err, c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	return f, frame, pix
}

func TestScreencopyCapturesOutputsAndRegions(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("shot", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	settle(t, h)
	mgr, err := cwsc.NewZwlrScreencopyManagerV1(c.Display, 3)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("zwlr_screencopy_manager_v1", mgr)

	f, _, pix := screenshot(t, c, mgr, image.Rectangle{}, false)
	c.DispatchUntil("the full capture", func() bool { return f.ready || f.failed })
	stride := int(f.stride)
	if f.failed || f.size != image.Pt(160, 100) || rgbAt(pix, stride, 20, 20) != [3]byte{0xff, 0, 0} || rgbAt(pix, stride, 140, 80) != [3]byte{0xff, 0xff, 0} {
		t.Fatalf("capture %+v: %v and %v", *f, rgbAt(pix, stride, 20, 20), rgbAt(pix, stride, 140, 80))
	}

	f, _, pix = screenshot(t, c, mgr, image.Rect(100, 10, 140, 30), false)
	c.DispatchUntil("the region capture", func() bool { return f.ready || f.failed })
	if f.failed || f.size != image.Pt(40, 20) || rgbAt(pix, int(f.stride), 5, 5) != [3]byte{0, 0xff, 0} {
		t.Fatalf("region capture %+v: %v", *f, rgbAt(pix, int(f.stride), 5, 5))
	}

	quiesce(t, h, c, w)
	f, _, _ = screenshot(t, c, mgr, image.Rectangle{}, true)
	settle(t, h)
	if err := c.Display.Roundtrip(); err != nil || f.ready {
		t.Fatalf("a damage capture was ready without damage (%v)", err)
	}
	if err := errors.Join(w.Surface.Attach(c.Buffer(w.Logical, 1, quadrants), 0, 0), w.Surface.Damage(0, 0, 10, 10), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the damage capture", func() bool { return f.ready })
	if !image.Rect(0, 0, 10, 10).In(f.damage) || f.damage == image.Rect(0, 0, 160, 100) {
		t.Fatalf("a damage capture reported %v", f.damage)
	}
}

func TestADestroyedWaitingScreencopyIsNeverPainted(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("shot", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	mgr, err := cwsc.NewZwlrScreencopyManagerV1(c.Display, 3)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("zwlr_screencopy_manager_v1", mgr)
	gone, frame, gonePix := screenshot(t, c, mgr, image.Rectangle{}, true)
	settle(t, h)
	if err := errors.Join(frame.Destroy(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(w.Surface.Attach(c.Buffer(w.Logical, 1, quadrants), 0, 0), w.Surface.Damage(0, 0, 10, 10), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	settle(t, h)
	c.Sync()
	if gone.ready || gone.failed || slices.ContainsFunc(gonePix, func(b byte) bool { return b != 0 }) {
		t.Fatalf("a destroyed screencopy was completed (%+v) or painted", *gone)
	}
}
