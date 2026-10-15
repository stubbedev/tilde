package harness

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/binds"
	"github.com/stubbedev/tilde/internal/compositor"
	cel "github.com/stubbedev/tilde/internal/protocol/client/extsessionlock"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wm"
)

type locker struct {
	t      *testing.T
	c      *testclient.Client
	lock   *cel.ExtSessionLockV1
	log    []string
	serial uint32
	size   image.Point
	in     *testclient.Input
}

func (l *locker) Locked(*cel.ExtSessionLockV1) error {
	l.log = append(l.log, "locked")
	return nil
}

func (l *locker) Finished(*cel.ExtSessionLockV1) error {
	l.log = append(l.log, "finished")
	return nil
}

func (l *locker) Configure(_ *cel.ExtSessionLockSurfaceV1, serial, w, h uint32) error {
	l.serial, l.size = serial, image.Pt(int(w), int(h))
	l.log = append(l.log, fmt.Sprintf("configure %dx%d", w, h))
	return nil
}

func lockSession(t *testing.T, h *Harness) *locker {
	t.Helper()
	l := &locker{t: t, c: testclient.Connect(t, h.Socket())}
	var seat *cw.WlSeat
	seat, l.in = l.c.Seat()
	_ = seat
	mgr, err := cel.NewExtSessionLockManagerV1(l.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	l.c.Bind(mgr)
	if l.lock, err = mgr.Lock(l); err != nil {
		t.Fatal(err)
	}
	if err := l.c.Display.Flush(); err != nil {
		t.Fatal(err)
	}
	return l
}

func (l *locker) seen(what string) {
	l.t.Helper()
	l.c.DispatchUntil(what, func() bool { return slices.Contains(l.log, what) })
}

func (l *locker) cover(size image.Point, b, g, r byte) {
	l.t.Helper()
	s := l.c.Surface()
	ls, err := l.lock.GetLockSurface(l, s, l.c.Outputs[0])
	if err != nil {
		l.t.Fatal(err)
	}
	l.c.DispatchUntil("the lock surface configure", func() bool { return l.serial != 0 })
	if err := errors.Join(ls.AckConfigure(l.serial), s.Attach(l.c.Buffer(size, 0, testclient.Fill(b, g, r)), 0, 0), s.Commit(), l.c.Display.Flush()); err != nil {
		l.t.Fatal(err)
	}
	l.in.Name(s, "lock")
}

func TestSessionLockCoversTheDesktopAndTakesTheKeyboard(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	l := lockSession(t, r.h)
	l.seen("locked")
	r.h.Eventually("the desktop blanked", showing(5, 5, [3]byte{compositor.LockedColour.R, compositor.LockedColour.G, compositor.LockedColour.B}))
	r.expect("kb_leave win", "leave win", "frame")
	l.cover(image.Pt(160, 100), 0xff, 0, 0)
	if !slices.Contains(l.log, "configure 160x100") {
		t.Fatalf("lock events %v", l.log)
	}
	r.h.Eventually("the lock surface shown", showing(5, 5, [3]byte{0, 0, 0xff}))
	l.c.DispatchUntil("the keyboard on the lock surface", func() bool { return slices.Contains(l.in.Log, "kb_enter lock") })
	r.inject(func(b *headless.Backend, _ *headless.Output) error { return b.Keyboard().Key(30, true) })
	l.c.DispatchUntil("the key at the lock", func() bool { return slices.Contains(l.in.Log, "key 30 1") })
	if err := errors.Join(l.lock.UnlockAndDestroy(), l.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.h.Eventually("the desktop back", showing(150, 90, [3]byte{0, 0, 0xff}))
	r.expect("kb_enter win")
}

func TestAnAbandonedLockStaysUntilANewLockerTakesOver(t *testing.T) {
	h := Start(t, small)
	first := lockSession(t, h)
	first.seen("locked")
	second := lockSession(t, h)
	second.seen("finished")
	if err := errors.Join(second.lock.UnlockAndDestroy(), second.c.Display.Roundtrip()); err != nil {
		t.Fatalf("a refused lock unlocking: %v", err)
	}
	locked := [3]byte{compositor.LockedColour.R, compositor.LockedColour.G, compositor.LockedColour.B}
	if got := pixel(h.Rendered(0), 5, 5); got != locked {
		t.Fatalf("after a refused lock's unlock_and_destroy the screen shows %v, not the lock colour", got)
	}
	if err := first.c.Display.Close(); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the abandoned lock colour", showing(5, 5, [3]byte{compositor.AbandonedColour.R, compositor.AbandonedColour.G, compositor.AbandonedColour.B}))
	third := lockSession(t, h)
	third.seen("locked")
	h.Eventually("the lock colour with a live locker", showing(5, 5, [3]byte{compositor.LockedColour.R, compositor.LockedColour.G, compositor.LockedColour.B}))
	if err := errors.Join(third.lock.UnlockAndDestroy(), third.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("unlocked", showing(5, 5, [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}))
}

func TestOnlyLockedBindsRunWhileLocked(t *testing.T) {
	workspace := func(n string) wm.Action {
		a, err := wm.ParseAction("workspace " + n)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Binds = append(cfg.Binds,
			binds.Bind{Chord: binds.Chord{Trigger: binds.Key{Code: 30}}, Flags: binds.Flags{Locked: true}, Action: workspace("2")},
			binds.Bind{Chord: binds.Chord{Trigger: binds.Key{Code: 48}}, Action: workspace("3")})
	}, small)
	l := lockSession(t, h)
	l.seen("locked")
	press := func(code uint32) {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return errors.Join(b.Keyboard().Key(code, true), b.Keyboard().Key(code, false))
		})
	}
	press(48)
	press(30)
	h.Eventually("the locked bind ran and the other did not", showingWorkspace(2))
}

func TestSessionLockProtocolErrors(t *testing.T) {
	for _, c := range []struct {
		what string
		code uint32
		do   func(l *locker) error
	}{
		{"unlock before locked", uint32(cel.ExtSessionLockV1ErrorInvalidUnlock), func(l *locker) error { return l.lock.UnlockAndDestroy() }},
		{"a surface of the wrong size", uint32(cel.ExtSessionLockSurfaceV1ErrorDimensionsMismatch), func(l *locker) error {
			l.seen("locked")
			s := l.c.Surface()
			ls, err := l.lock.GetLockSurface(l, s, l.c.Outputs[0])
			if err != nil {
				return err
			}
			l.c.DispatchUntil("the configure", func() bool { return l.serial != 0 })
			return errors.Join(ls.AckConfigure(l.serial), s.Attach(l.c.Buffer(image.Pt(10, 10), 0, testclient.Fill(0, 0, 0)), 0, 0), s.Commit())
		}},
		{"two surfaces on one output", uint32(cel.ExtSessionLockV1ErrorDuplicateOutput), func(l *locker) error {
			for range 2 {
				if _, err := l.lock.GetLockSurface(l, l.c.Surface(), l.c.Outputs[0]); err != nil {
					return err
				}
			}
			return nil
		}},
		{"destroy after locked", uint32(cel.ExtSessionLockV1ErrorInvalidDestroy), func(l *locker) error {
			l.seen("locked")
			return l.lock.Destroy()
		}},
	} {
		t.Run(c.what, func(t *testing.T) {
			h := Start(t, small)
			l := lockSession(t, h)
			if err := c.do(l); err != nil {
				t.Fatal(err)
			}
			l.c.ExpectProtocolError(c.code)
		})
	}
}

func TestOutputsAddedWhileLockedStayBlank(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("hidden", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	l := lockSession(t, h)
	l.seen("locked")
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddOutput(small)
		return err
	})
	h.Eventually("the new output blank", func(b *headless.Backend, _ *compositor.Compositor) bool {
		outs := b.Outputs()
		return len(outs) == 2 && pixel(outs[1].Snapshot(), 5, 5) == [3]byte{compositor.LockedColour.R, compositor.LockedColour.G, compositor.LockedColour.B} &&
			pixel(outs[0].Snapshot(), 150, 90) == [3]byte{compositor.LockedColour.R, compositor.LockedColour.G, compositor.LockedColour.B}
	})
}

func TestPoweringOffBeforeTheLockFrameStillConfirmsTheLock(t *testing.T) {
	slow := small
	slow.RefreshMHz = 1000
	h := Start(t, slow)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("shown", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	l := lockSession(t, h)
	if err := l.c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		return comp.SetPower(comp.Outputs()[0], false)
	})
	l.seen("locked")
}

func TestTouchesFollowAFingerOnTheLockScreen(t *testing.T) {
	h := Start(t, small)
	l := lockSession(t, h)
	l.seen("locked")
	l.cover(image.Pt(160, 100), 0xff, 0, 0)
	h.Eventually("the lock surface shown", showing(5, 5, [3]byte{0, 0, 0xff}))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[0]
		return errors.Join(b.Touch().Down(1, o, 0.25, 0.5), b.Touch().Frame(), b.Touch().Motion(1, 0.5, 0.5), b.Touch().Frame())
	})
	l.c.DispatchUntil("the finger's motion on the lock surface", func() bool {
		return slices.Contains(l.in.Log, "touch_motion 1 80,50")
	})
}
