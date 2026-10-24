package harness

import (
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/fourcc"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

func scanouts(h *Harness) uint64 {
	var n uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		n = b.Outputs()[0].Scanouts()
		return nil
	})
	return n
}

var primaryPlane = gpu.ScanoutTarget{Device: 0xe201, Formats: []gpu.FormatModifier{
	{Format: fourcc.XRGB8888, Modifier: fourcc.Linear},
	{Format: fourcc.ARGB8888, Modifier: fourcc.Linear},
	{Format: fourcc.XRGB8888, Modifier: 7},
	{Format: fourcc.XRGB8888, Modifier: fourcc.Invalid},
}}

type scanoutScene struct {
	h *Harness
	c *testclient.Client
	w *testclient.Window
}

func fullscreenScanout(t *testing.T, configure func(*compositor.Config)) *scanoutScene {
	t.Helper()
	h := StartGLES(t, configure, small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.Outputs()[0].AllowScanout(primaryPlane)
		return nil
	})
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("game", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	c.DispatchUntil("the pointer to enter", func() bool { return in.Serial != 0 })
	if err := errors.Join(in.Pointer.SetCursor(in.Serial, nil, 0, 0), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.dispatch("fullscreen")
	c.DispatchUntil("the fullscreen configure", func() bool { return w.Logical == small.Size })
	quiesce(t, h, c, w)
	if n := scanouts(h); n != 0 {
		t.Fatalf("%d shm frames scanned out", n)
	}
	return &scanoutScene{h: h, c: c, w: w}
}

func (s *scanoutScene) show(t *testing.T, name string, b *testclient.DMABUF) {
	t.Helper()
	before := scanouts(s.h)
	if err := errors.Join(s.w.Surface.Attach(b.WL, 0, 0), s.w.Surface.DamageBuffer(0, 0, 160, 100), s.w.Surface.Commit(), s.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	s.h.Eventually(name+" scanned out", func(b *headless.Backend, _ *compositor.Compositor) bool { return b.Outputs()[0].Scanouts() == before+1 })
}

func TestFullscreenDMABUFsScanOutDirectly(t *testing.T) {
	s := fullscreenScanout(t, func(*compositor.Config) {})
	h, c := s.h, s.c
	show := func(name string, b *testclient.DMABUF) { t.Helper(); s.show(t, name, b) }
	first := c.DMABUF(image.Pt(160, 100), testclient.Fill(0xff, 0, 0))
	show("the first dmabuf", first)
	second := c.DMABUF(image.Pt(160, 100), testclient.Fill(0, 0xff, 0))
	show("the second dmabuf", second)
	c.DispatchUntil("the first dmabuf released once off screen", func() bool { return first.Released })
	settle(t, h)
	c.Sync()
	if second.Released {
		t.Fatal("the dmabuf on screen was released")
	}

	h.dispatch("fullscreen")
	other := c.Window("beside", quadrants)
	c.DispatchUntil("a second window to map", other.Mapped)
	before := scanouts(h)
	c.DispatchUntil("the scanned-out dmabuf released after a composited frame", func() bool { return second.Released })
	settle(t, h)
	if n := scanouts(h); n != before {
		t.Fatalf("a window sharing the output scanned out %d frames", n-before)
	}
}

func TestDestroyingAReplacedBufferStillOnScreenSendsNoRelease(t *testing.T) {
	s := fullscreenScanout(t, func(*compositor.Config) {})
	gone := s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0, 0, 0xff))
	s.show(t, "the dmabuf", gone)
	next := s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0xff, 0xff, 0))
	before := scanouts(s.h)
	if err := errors.Join(s.w.Surface.Attach(next.WL, 0, 0), s.w.Surface.DamageBuffer(0, 0, 160, 100), s.w.Surface.Commit(), gone.WL.Destroy(), s.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	s.h.Eventually("the next dmabuf scanned out", func(b *headless.Backend, _ *compositor.Compositor) bool { return b.Outputs()[0].Scanouts() == before+1 })
	settle(t, s.h)
	s.c.Sync()
	if gone.Released {
		t.Fatal("a destroyed buffer was released")
	}
}

func TestAFullscreenSurfaceIsOfferedThePlaneFormats(t *testing.T) {
	s := fullscreenScanout(t, func(*compositor.Config) {})
	var game *surface.Surface
	var got gpu.ScanoutTarget
	s.h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		game = c.Seat().PointerFocus()
		got = c.SurfaceScanout(game)
		return nil
	})
	want := []gpu.FormatModifier{primaryPlane.Formats[0], primaryPlane.Formats[2], primaryPlane.Formats[3]}
	if game == nil || got.Device != primaryPlane.Device || !slices.Equal(got.Formats, want) {
		t.Fatalf("a fullscreen surface is offered %+v, want device %#x and the opaque formats %+v", got, primaryPlane.Device, want)
	}

	s.h.dispatch("fullscreen")
	other := s.c.Window("beside", quadrants)
	s.c.DispatchUntil("a second window to map", other.Mapped)
	s.h.Eventually("the plane formats withdrawn from a tiled surface", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return len(c.SurfaceScanout(game).Formats) == 0
	})
}
