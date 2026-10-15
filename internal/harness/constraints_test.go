package harness

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cpc "github.com/stubbedev/tilde/internal/protocol/client/pointerconstraints"
	crp "github.com/stubbedev/tilde/internal/protocol/client/relativepointer"
	cvp "github.com/stubbedev/tilde/internal/protocol/client/wlrvirtualpointer"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wire"
)

type constraintLog struct{ events []string }

func (l *constraintLog) Locked(*cpc.ZwpLockedPointerV1) error {
	l.events = append(l.events, "locked")
	return nil
}

func (l *constraintLog) Unlocked(*cpc.ZwpLockedPointerV1) error {
	l.events = append(l.events, "unlocked")
	return nil
}

func (l *constraintLog) Confined(*cpc.ZwpConfinedPointerV1) error {
	l.events = append(l.events, "confined")
	return nil
}

func (l *constraintLog) Unconfined(*cpc.ZwpConfinedPointerV1) error {
	l.events = append(l.events, "unconfined")
	return nil
}

type relativeLog struct{ deltas [][4]float64 }

func (l *relativeLog) RelativeMotion(_ *crp.ZwpRelativePointerV1, _, _ uint32, dx, dy, ux, uy wire.Fixed) error {
	l.deltas = append(l.deltas, [4]float64{dx.Float(), dy.Float(), ux.Float(), uy.Float()})
	return nil
}

func (r *inputRig) constraints() *cpc.ZwpPointerConstraintsV1 {
	r.t.Helper()
	m, err := cpc.NewZwpPointerConstraintsV1(r.c.Display, 1)
	if err != nil {
		r.t.Fatal(err)
	}
	r.c.Bind(m)
	return m
}

func (r *inputRig) await(l *constraintLog, want ...string) {
	r.t.Helper()
	r.c.DispatchUntil("constraint events "+strings.Join(want, ", "), func() bool { return len(l.events) >= len(want) })
	if !slices.Equal(l.events, want) {
		r.t.Fatalf("constraint events %q, want %q", l.events, want)
	}
}

func (r *inputRig) cursor() (float64, float64) {
	var x, y float64
	r.h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		x, y = c.Cursor()
		return nil
	})
	return x, y
}

func (r *inputRig) nudge(dx, dy float64) {
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Pointer().Move(dx, dy), b.Pointer().Frame())
	})
}

func TestRelativePointerReportsRawMotion(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	mgr, err := crp.NewZwpRelativePointerManagerV1(r.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.c.Bind(mgr)
	l := &relativeLog{}
	if _, err := mgr.GetRelativePointer(l, r.in.Pointer); err != nil {
		t.Fatal(err)
	}
	if err := r.c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	r.nudge(7, -3)
	r.c.DispatchUntil("relative motion", func() bool { return len(l.deltas) == 1 })
	if l.deltas[0] != [4]float64{7, -3, 7, -3} {
		t.Fatalf("relative motion %v", l.deltas[0])
	}
}

func TestLockedPointerHoldsTheCursorAndWarpsToTheHint(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	l := &constraintLog{}
	lock, err := r.constraints().LockPointer(l, r.w.Surface, r.in.Pointer, nil, cpc.ZwpPointerConstraintsV1LifetimePersistent)
	if err != nil {
		t.Fatal(err)
	}
	r.await(l, "locked")
	r.nudge(30, 20)
	if x, y := r.cursor(); x != 80 || y != 50 {
		t.Fatalf("a locked pointer moved the cursor to %v,%v", x, y)
	}
	if err := errors.Join(lock.SetCursorPositionHint(wire.FixedFromFloat(10), wire.FixedFromFloat(12)), r.w.Surface.Commit(), lock.Destroy(), r.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	if x, y := r.cursor(); x != 10 || y != 12 {
		t.Fatalf("unlocking left the cursor at %v,%v, not the 10,12 hint", x, y)
	}
	r.nudge(5, 0)
	if x, _ := r.cursor(); x != 15 {
		t.Fatalf("after the lock went away the cursor sits at x %v", x)
	}
}

func TestALockOutlivesFocusOnlyWhenPersistent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lifetime cpc.ZwpPointerConstraintsV1Lifetime
		back     []string
	}{
		{"oneshot", cpc.ZwpPointerConstraintsV1LifetimeOneshot, []string{"locked", "unlocked"}},
		{"persistent", cpc.ZwpPointerConstraintsV1LifetimePersistent, []string{"locked", "unlocked", "locked"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := startInput(t)
			r.expect("kb_enter win", "enter win 80,50", "frame")
			l := &constraintLog{}
			if _, err := r.constraints().LockPointer(l, r.w.Surface, r.in.Pointer, nil, tc.lifetime); err != nil {
				t.Fatal(err)
			}
			r.await(l, "locked")
			r.h.dispatch("workspace 2")
			r.await(l, "locked", "unlocked")
			r.h.dispatch("workspace 1")
			r.h.Eventually("the window focused again", func(_ *headless.Backend, c *compositor.Compositor) bool {
				ws := c.Snapshot().Windows
				return len(ws) == 1 && ws[0].Focused
			})
			r.nudge(1, 0)
			r.c.Sync()
			if !slices.Equal(l.events, tc.back) {
				t.Fatalf("constraint events %q, want %q", l.events, tc.back)
			}
		})
	}
}

func TestConfinedPointerStaysInItsRegion(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	area, err := r.c.Compositor.CreateRegion()
	if err != nil {
		t.Fatal(err)
	}
	if err := area.Add(20, 20, 40, 40); err != nil {
		t.Fatal(err)
	}
	l := &constraintLog{}
	confine, err := r.constraints().ConfinePointer(l, r.w.Surface, r.in.Pointer, area, cpc.ZwpPointerConstraintsV1LifetimePersistent)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.c.Display.Roundtrip(); err != nil || len(l.events) != 0 {
		t.Fatalf("confined with the cursor outside the region: %q (%v)", l.events, err)
	}
	r.moveTo(30, 30)
	r.await(l, "confined")
	r.nudge(100, 100)
	if x, y := r.cursor(); x < 59.99 || x >= 60 || y < 59.99 || y >= 60 {
		t.Fatalf("a confined cursor reached %v,%v outside 20,20 40x40", x, y)
	}
	r.nudge(-100, 0)
	if x, _ := r.cursor(); x != 20 {
		t.Fatalf("a confined cursor reached x %v left of the region", x)
	}
	corner, err := r.c.Compositor.CreateRegion()
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(corner.Add(0, 0, 10, 10), confine.SetRegion(corner), r.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	if x, _ := r.cursor(); x != 20 {
		t.Fatalf("an uncommitted region moved the cursor to x %v", x)
	}
	if err := errors.Join(r.w.Surface.Commit(), r.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	if x, y := r.cursor(); x >= 10 || y >= 10 {
		t.Fatalf("the committed 10x10 region left the cursor at %v,%v", x, y)
	}
	r.h.dispatch("workspace 2")
	r.await(l, "confined", "unconfined")
	r.h.dispatch("workspace 1")
	r.await(l, "confined", "unconfined", "confined")
}

func TestPointerConstraintsAreOnePerSurface(t *testing.T) {
	r := startInput(t)
	m := r.constraints()
	l := &constraintLog{}
	if _, err := m.LockPointer(l, r.w.Surface, r.in.Pointer, nil, cpc.ZwpPointerConstraintsV1LifetimePersistent); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ConfinePointer(l, r.w.Surface, r.in.Pointer, nil, cpc.ZwpPointerConstraintsV1LifetimePersistent); err != nil {
		t.Fatal(err)
	}
	r.c.ExpectProtocolError(uint32(cpc.ZwpPointerConstraintsV1ErrorAlreadyConstrained))
}

func TestALockedPointerHoldsAgainstVirtualAbsoluteMotion(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	l := &constraintLog{}
	if _, err := r.constraints().LockPointer(l, r.w.Surface, r.in.Pointer, nil, cpc.ZwpPointerConstraintsV1LifetimePersistent); err != nil {
		t.Fatal(err)
	}
	r.await(l, "locked")
	driver := testclient.Connect(t, r.h.Socket())
	seat, _ := driver.Seat()
	mgr, err := cvp.NewZwlrVirtualPointerManagerV1(driver.Display, 2)
	if err != nil {
		t.Fatal(err)
	}
	driver.Bind(mgr)
	vp, err := mgr.CreateVirtualPointer(seat)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(vp.MotionAbsolute(1, 10, 10, 160, 100), vp.Frame(), driver.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	if x, y := r.cursor(); x != 80 || y != 50 {
		t.Fatalf("virtual absolute motion moved a locked cursor to %v,%v", x, y)
	}
}
