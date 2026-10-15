package harness

import (
	"errors"
	"image"
	"runtime"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cwlr "github.com/stubbedev/tilde/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/testclient"
)

func area(r render.Region) int {
	n := 0
	for _, rect := range r {
		n += rect.Dx() * rect.Dy()
	}
	return n
}

func presentCount(h *Harness) uint64 {
	var n uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		n = b.Outputs()[0].Presents()
		return nil
	})
	return n
}

func settle(t *testing.T, h *Harness) {
	t.Helper()
	presents := presentCount(h)
	for deadline := time.Now().Add(clientTimeout); ; {
		time.Sleep(50 * time.Millisecond)
		now := presentCount(h)
		if now == presents {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the output never stopped presenting")
		}
		presents = now
	}
}

func quiesce(t *testing.T, h *Harness, c *testclient.Client, w *testclient.Window) {
	t.Helper()
	w.Stop()
	for range 5 {
		settle(t, h)
		before := presentCount(h)
		if err := c.Display.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		settle(t, h)
		if presentCount(h) == before {
			return
		}
	}
	t.Fatal("the client kept redrawing")
}

type frameDone struct{ done *bool }

func (f frameDone) Done(*cw.WlCallback, uint32) error {
	*f.done = true
	return nil
}

func TestOnlyDamageIsRepainted(t *testing.T) {
	h := Start(t, desktop)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("damage", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.MoveTo(b.Outputs()[0], 0.9, 0.9), p.Frame())
	})
	settle(t, h)

	presents := presentCount(h)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.Move(40, 0), p.Frame())
	})
	h.Eventually("the cursor move presented", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Presents() > presents
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if d := b.Outputs()[0].LastDamage(); area(d) == 0 || area(d) > 2*compositor.DefaultCursorSize*compositor.DefaultCursorSize {
			t.Errorf("a cursor move repainted %v", d)
		}
		return nil
	})
	settle(t, h)
	presents = presentCount(h)
	update := c.Buffer(w.Size, 1, testclient.Fill(0x10, 0x20, 0x30))
	for _, err := range []error{w.Surface.Attach(update, 0, 0), w.Surface.DamageBuffer(100, 50, 10, 20), w.Surface.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	c.Sync()
	h.Eventually("the partial update presented", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Presents() > presents
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		if d := o.LastDamage(); len(d) != 1 || d[0] != image.Rect(100, 50, 110, 70) {
			t.Errorf("a 10x20 update at (100,50) repainted %v", d)
		}
		if got := pixel(o.Snapshot(), 105, 60); got != [3]byte{0x30, 0x20, 0x10} {
			t.Errorf("the damaged area shows %v", got)
		}
		if got := pixel(o.Snapshot(), 300, 300); got == [3]byte{0x30, 0x20, 0x10} {
			t.Error("undamaged pixels changed")
		}
		presents = o.Presents()
		return nil
	})

	done := false
	if _, err := w.Surface.Frame(frameDone{&done}); err != nil {
		t.Fatal(err)
	}
	if err := w.Surface.Commit(); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the frame callback of an unchanged commit", func() bool { return done })
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if n := b.Outputs()[0].Presents(); n != presents {
			t.Errorf("an unchanged commit presented %d frames", n-presents)
		}
		return nil
	})
}

func TestIdleDesktopNeverWakes(t *testing.T) {
	h := Start(t, desktop)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("idle", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	settle(t, h)
	before := h.Wakeups()
	time.Sleep(300 * time.Millisecond)
	if woke := h.Wakeups() - before; woke > 1 {
		t.Fatalf("an idle desktop woke the loop %d times in 300ms", woke-1)
	}
}

func TestSteadyStateFramesDoNotAllocate(t *testing.T) {
	h := StartUntraced(t, desktop)
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	w := c.Window("steady", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[0]
		move := func() {
			if err := errors.Join(b.Pointer().Move(2, 1), b.Pointer().Frame()); err != nil {
				t.Fatal(err)
			}
		}
		frame := func() {
			if err := comp.Frame(o); err != nil {
				t.Fatal(err)
			}
		}
		for range 100 {
			move()
			frame()
			frame()
		}
		moving, idle := allocsPerRun(100, move, frame), allocsPerRun(100, func() {}, frame)
		if moving != 0 || idle != 0 {
			t.Errorf("cursor frames allocated %d times each and unchanged frames %d times", moving, idle)
		}
		presented := func() {
			if err := comp.Presented(o, backend.Presentation{Time: backend.Now(), Refresh: desktop.Interval(), Seq: 1}); err != nil {
				t.Fatal(err)
			}
		}
		if n := allocsPerRun(100, func() {}, presented); n != 0 {
			t.Errorf("a presentation allocated %d times", n)
		}
		return nil
	})
}

func TestPointerMotionDoesNotAllocate(t *testing.T) {
	h := StartUntraced(t, desktop)
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	w := c.Window("steady", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		motions := [2]backend.Event{backend.PointerMotion{DX: 2, DY: 1}, backend.PointerMotion{DX: -2, DY: -1}}
		var frame backend.Event = backend.PointerFrame{}
		next := 0
		move := func() {
			if err := errors.Join(comp.Input(b.Pointer(), motions[next]), comp.Input(b.Pointer(), frame)); err != nil {
				t.Fatal(err)
			}
			next ^= 1
		}
		for range 100 {
			move()
		}
		if n := allocsPerRun(100, func() {}, move); n != 0 {
			t.Errorf("a pointer motion allocated %d times", n)
		}
		return nil
	})
}

func allocsPerRun(runs uint64, prepare, frame func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	var before, after runtime.MemStats
	var total uint64
	for range runs {
		prepare()
		runtime.ReadMemStats(&before)
		frame()
		runtime.ReadMemStats(&after)
		total += after.Mallocs - before.Mallocs
	}
	return total / runs
}

func TestUndamagedFrameCallbacksArePacedByRefresh(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("idle", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	quiesce(t, h, c, w)
	const span = 250 * time.Millisecond
	callbacks := 0
	for start := time.Now(); time.Since(start) < span; callbacks++ {
		var done bool
		if _, err := w.Surface.Frame(frameDone{&done}); err != nil {
			t.Fatal(err)
		}
		if err := w.Surface.Commit(); err != nil {
			t.Fatal(err)
		}
		c.DispatchUntil("the frame callback", func() bool { return done })
	}
	if limit := int(span/small.Interval()) + 2; callbacks > limit {
		t.Fatalf("%d frame callbacks in %v without damage, want at most %d at %d mHz", callbacks, span, limit, small.RefreshMHz)
	}
}
