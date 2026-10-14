package harness

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cpg "github.com/stubbedev/tilde/internal/protocol/client/pointergestures"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wire"
)

type gestureLog struct{ events []string }

func (l *gestureLog) add(format string, args ...any) error {
	l.events = append(l.events, fmt.Sprintf(format, args...))
	return nil
}

type swipeLog struct{ *gestureLog }

func (l swipeLog) Begin(_ *cpg.ZwpPointerGestureSwipeV1, _, _ uint32, _ *cw.WlSurface, fingers uint32) error {
	return l.add("swipe begin %d", fingers)
}

func (l swipeLog) Update(_ *cpg.ZwpPointerGestureSwipeV1, _ uint32, dx, dy wire.Fixed) error {
	return l.add("swipe update %g %g", dx.Float(), dy.Float())
}

func (l swipeLog) End(_ *cpg.ZwpPointerGestureSwipeV1, _, _ uint32, cancelled int32) error {
	return l.add("swipe end %d", cancelled)
}

type pinchLog struct{ *gestureLog }

func (l pinchLog) Begin(_ *cpg.ZwpPointerGesturePinchV1, _, _ uint32, _ *cw.WlSurface, fingers uint32) error {
	return l.add("pinch begin %d", fingers)
}

func (l pinchLog) Update(_ *cpg.ZwpPointerGesturePinchV1, _ uint32, dx, dy, scale, rotation wire.Fixed) error {
	return l.add("pinch update %g %g %g %g", dx.Float(), dy.Float(), scale.Float(), rotation.Float())
}

func (l pinchLog) End(_ *cpg.ZwpPointerGesturePinchV1, _, _ uint32, cancelled int32) error {
	return l.add("pinch end %d", cancelled)
}

type holdLog struct{ *gestureLog }

func (l holdLog) Begin(_ *cpg.ZwpPointerGestureHoldV1, _, _ uint32, _ *cw.WlSurface, fingers uint32) error {
	return l.add("hold begin %d", fingers)
}

func (l holdLog) End(_ *cpg.ZwpPointerGestureHoldV1, _, _ uint32, cancelled int32) error {
	return l.add("hold end %d", cancelled)
}

func gestureClient(t *testing.T, h *Harness, window string) (*testclient.Client, *gestureLog) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	if window != "" {
		w := c.Window(window, quadrants)
		c.DispatchUntil("the window to map", w.Mapped)
	}
	m, err := cpg.NewZwpPointerGesturesV1(c.Display, 3)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("zwp_pointer_gestures_v1", m)
	log := &gestureLog{}
	_, err1 := m.GetSwipeGesture(swipeLog{log}, in.Pointer)
	_, err2 := m.GetPinchGesture(pinchLog{log}, in.Pointer)
	_, err3 := m.GetHoldGesture(holdLog{log}, in.Pointer)
	if err := errors.Join(err1, err2, err3, c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	return c, log
}

func (h *Harness) gesture(do func(o *headless.Output, p *headless.Pointer) error) {
	h.t.Helper()
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error { return do(b.Outputs()[0], b.Pointer()) })
}

func TestTouchpadGesturesReachTheSurfaceUnderThePointer(t *testing.T) {
	h := Start(t, small)
	c, log := gestureClient(t, h, "gestures")
	h.gesture(func(o *headless.Output, p *headless.Pointer) error {
		return errors.Join(
			p.MoveTo(o, 0.5, 0.5), p.Frame(),
			p.GestureBegin(backend.Swipe, 3), p.Swipe(4.5, -1), p.GestureEnd(backend.Swipe, false),
			p.GestureBegin(backend.Pinch, 2), p.Pinch(1, 2, 1.25, -10), p.GestureEnd(backend.Pinch, true),
			p.GestureBegin(backend.Hold, 4), p.GestureEnd(backend.Hold, false),
		)
	})
	want := []string{
		"swipe begin 3", "swipe update 4.5 -1", "swipe end 0",
		"pinch begin 2", "pinch update 1 2 1.25 -10", "pinch end 1",
		"hold begin 4", "hold end 0",
	}
	c.DispatchUntil("the gestures", func() bool { return len(log.events) >= len(want) })
	if !slices.Equal(log.events, want) {
		t.Fatalf("events %q, want %q", log.events, want)
	}
}

func TestAGestureStaysWithTheSurfaceItBeganOn(t *testing.T) {
	h := Start(t, small)
	left, leftLog := gestureClient(t, h, "left")
	right, rightLog := gestureClient(t, h, "right")
	h.gesture(func(o *headless.Output, p *headless.Pointer) error {
		return errors.Join(
			p.MoveTo(o, 0.25, 0.5), p.Frame(), p.GestureBegin(backend.Swipe, 3),
			p.MoveTo(o, 0.75, 0.5), p.Frame(), p.Swipe(1, 1), p.GestureEnd(backend.Swipe, false),
		)
	})
	want := []string{"swipe begin 3", "swipe update 1 1", "swipe end 0"}
	left.DispatchUntil("the whole swipe on the left window", func() bool { return len(leftLog.events) >= len(want) })
	if err := right.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(leftLog.events, want) || len(rightLog.events) != 0 {
		t.Fatalf("left saw %q and right %q, want all of %q on the left", leftLog.events, rightLog.events, want)
	}
}

func TestGesturesWithoutAFocusGoNowhere(t *testing.T) {
	h := Start(t, small)
	c, log := gestureClient(t, h, "")
	h.gesture(func(_ *headless.Output, p *headless.Pointer) error {
		return errors.Join(p.GestureBegin(backend.Pinch, 2), p.Pinch(0, 0, 2, 0), p.GestureEnd(backend.Pinch, false))
	})
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(log.events) != 0 {
		t.Fatalf("a gesture with no surface under the pointer went to a client: %q", log.events)
	}
}
