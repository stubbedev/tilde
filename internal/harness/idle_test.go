package harness

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cei "github.com/stubbedev/tilde/internal/protocol/client/extidlenotify"
	cii "github.com/stubbedev/tilde/internal/protocol/client/idleinhibit"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wm"
)

type idleLog struct{ log []string }

func (l *idleLog) Idled(*cei.ExtIdleNotificationV1) error {
	l.log = append(l.log, "idled")
	return nil
}

func (l *idleLog) Resumed(*cei.ExtIdleNotificationV1) error {
	l.log = append(l.log, "resumed")
	return nil
}

type idler struct {
	t        *testing.T
	c        *testclient.Client
	notifier *cei.ExtIdleNotifierV1
	seat     *cw.WlSeat
}

func newIdler(t *testing.T, h *Harness) *idler {
	t.Helper()
	i := &idler{t: t, c: testclient.Connect(t, h.Socket())}
	i.seat, _ = i.c.Seat()
	var err error
	if i.notifier, err = cei.NewExtIdleNotifierV1(i.c.Display, 2); err != nil {
		t.Fatal(err)
	}
	i.c.Bind(i.notifier)
	return i
}

func (i *idler) watch(timeout time.Duration, input bool) *idleLog {
	i.t.Helper()
	l := &idleLog{}
	var err error
	if input {
		_, err = i.notifier.GetInputIdleNotification(l, uint32(timeout.Milliseconds()), i.seat)
	} else {
		_, err = i.notifier.GetIdleNotification(l, uint32(timeout.Milliseconds()), i.seat)
	}
	if err = errors.Join(err, i.c.Display.Flush()); err != nil {
		i.t.Fatal(err)
	}
	return l
}

func (i *idler) quiet(l *idleLog, d time.Duration) {
	i.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := i.c.Display.Roundtrip(); err != nil {
			i.t.Fatal(err)
		}
		if len(l.log) > 0 {
			i.t.Fatalf("idle events %v during %v", l.log, d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func move(h *Harness) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Move(1, 0), b.Pointer().Frame())
	})
}

func TestIdleNotificationsFireAndResume(t *testing.T) {
	h := Start(t, small)
	i := newIdler(t, h)
	l := i.watch(60*time.Millisecond, false)
	i.c.DispatchUntil("idled", func() bool { return slices.Equal(l.log, []string{"idled"}) })
	move(h)
	i.c.DispatchUntil("resumed", func() bool { return slices.Equal(l.log, []string{"idled", "resumed"}) })
	for range 10 {
		time.Sleep(20 * time.Millisecond)
		move(h)
	}
	if !slices.Equal(l.log, []string{"idled", "resumed"}) {
		t.Fatalf("idle while input kept coming: %v", l.log)
	}
	i.c.DispatchUntil("idled again", func() bool { return len(l.log) == 3 && l.log[2] == "idled" })
}

func TestVisibleInhibitorsHoldOffIdle(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("video", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	mgr, err := cii.NewZwpIdleInhibitManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	inhibitor, err := mgr.CreateInhibitor(w.Surface)
	if err != nil || c.Display.Roundtrip() != nil {
		t.Fatal(err)
	}
	i := newIdler(t, h)
	held := i.watch(40*time.Millisecond, false)
	input := i.watch(40*time.Millisecond, true)
	i.c.DispatchUntil("input idle despite the inhibitor", func() bool { return slices.Equal(input.log, []string{"idled"}) })
	i.quiet(held, 150*time.Millisecond)

	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		a, err := wm.ParseAction("workspace 2")
		if err != nil {
			return err
		}
		return comp.Dispatch(a)
	})
	i.c.DispatchUntil("idle once the inhibiting window is hidden", func() bool { return slices.Equal(held.log, []string{"idled"}) })
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		a, err := wm.ParseAction("workspace 1")
		if err != nil {
			return err
		}
		return comp.Dispatch(a)
	})
	i.c.DispatchUntil("resumed when it shows again", func() bool { return slices.Equal(held.log, []string{"idled", "resumed"}) })
	if err := errors.Join(inhibitor.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	i.c.DispatchUntil("idle after the inhibitor went away", func() bool { return slices.Equal(held.log, []string{"idled", "resumed", "idled"}) })
}

func TestAnInhibitorStopsHoldingWhenItsSurfaceIsDestroyed(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("video", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	mgr, err := cii.NewZwpIdleInhibitManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	for range 20 {
		toggled, err := mgr.CreateInhibitor(w.Surface)
		if err != nil {
			t.Fatal(err)
		}
		if err := toggled.Destroy(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mgr.CreateInhibitor(w.Surface); err != nil {
		t.Fatal(err)
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	i := newIdler(t, h)
	held := i.watch(40*time.Millisecond, false)
	i.quiet(held, 120*time.Millisecond)
	if err := errors.Join(w.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	i.c.DispatchUntil("idle once the inhibiting surface is gone", func() bool { return slices.Equal(held.log, []string{"idled"}) })
}
