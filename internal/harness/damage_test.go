package harness

import (
	"errors"
	"image"
	"runtime"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
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

func settle(t *testing.T, h *Harness) {
	t.Helper()
	var presents uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		presents = b.Outputs()[0].Presents()
		return nil
	})
	for deadline := time.Now().Add(clientTimeout); ; {
		time.Sleep(50 * time.Millisecond)
		var now uint64
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			now = b.Outputs()[0].Presents()
			return nil
		})
		if now == presents {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the output never stopped presenting")
		}
		presents = now
	}
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

	var presents uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		presents = b.Outputs()[0].Presents()
		return nil
	})
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
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		presents = b.Outputs()[0].Presents()
		return nil
	})
	update := c.Buffer(w.Size, 1, testclient.Fill(0x10, 0x20, 0x30))
	for _, err := range []error{w.Surface.Attach(update, 0, 0), w.Surface.DamageBuffer(100, 50, 10, 20), w.Surface.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	c.DispatchUntil("the commit", func() bool { return true })
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
	w := c.Window("steady", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[0]
		var before, after runtime.MemStats
		frame := func(move bool) uint64 {
			if move {
				if err := errors.Join(b.Pointer().Move(2, 1), b.Pointer().Frame()); err != nil {
					t.Fatal(err)
				}
			}
			runtime.ReadMemStats(&before)
			if err := comp.Frame(o); err != nil {
				t.Fatal(err)
			}
			runtime.ReadMemStats(&after)
			return after.Mallocs - before.Mallocs
		}
		for range 100 {
			frame(true)
			frame(false)
		}
		var moving, idle uint64
		for range 100 {
			moving += frame(true)
			idle += frame(false)
		}
		if moving != 0 || idle != 0 {
			t.Errorf("100 cursor frames allocated %d times and 100 unchanged frames %d times", moving, idle)
		}
		return nil
	})
}
