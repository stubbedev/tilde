package seat

import (
	"errors"
	"fmt"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	spg "github.com/stubbedev/tilde/internal/protocol/server/pointergestures"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const gesturesVersion = 3

type gestureState struct {
	kind    backend.Gesture
	surface *surface.Surface
}

func (s *Seat) bindGestures(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := spg.NewZwpPointerGesturesV1(c, id, version, gestureRequests{s, c})
	return err
}

type gestureRequests struct {
	s *Seat
	c *server.Client
}

func (gestureRequests) Release(*spg.ZwpPointerGesturesV1) error { return nil }

func (g gestureRequests) GetSwipeGesture(_ *spg.ZwpPointerGesturesV1, res *spg.ZwpPointerGestureSwipeV1, _ *sw.WlPointer) (spg.ZwpPointerGestureSwipeV1Handler, error) {
	r := g.s.resources(g.c)
	track(g.s, r, &r.swipes, res)
	return swipeObject{}, nil
}

func (g gestureRequests) GetPinchGesture(_ *spg.ZwpPointerGesturesV1, res *spg.ZwpPointerGesturePinchV1, _ *sw.WlPointer) (spg.ZwpPointerGesturePinchV1Handler, error) {
	r := g.s.resources(g.c)
	track(g.s, r, &r.pinches, res)
	return pinchObject{}, nil
}

func (g gestureRequests) GetHoldGesture(_ *spg.ZwpPointerGesturesV1, res *spg.ZwpPointerGestureHoldV1, _ *sw.WlPointer) (spg.ZwpPointerGestureHoldV1Handler, error) {
	r := g.s.resources(g.c)
	track(g.s, r, &r.holds, res)
	return holdObject{}, nil
}

type (
	swipeObject struct{}
	pinchObject struct{}
	holdObject  struct{}
)

func (swipeObject) Destroy(*spg.ZwpPointerGestureSwipeV1) error { return nil }
func (pinchObject) Destroy(*spg.ZwpPointerGesturePinchV1) error { return nil }
func (holdObject) Destroy(*spg.ZwpPointerGestureHoldV1) error   { return nil }

func (s *Seat) gestureTarget() (*server.Client, *clientResources) {
	g := s.gesture
	if g.surface == nil {
		return nil, nil
	}
	c := g.surface.Client()
	return c, s.lookup(c)
}

func (s *Seat) GestureBegin(at time.Duration, kind backend.Gesture, fingers uint32) error {
	if s.gesture.surface != nil {
		if err := s.GestureEnd(at, s.gesture.kind, true); err != nil {
			return err
		}
	}
	if s.pointer.focus == nil {
		return nil
	}
	s.gesture = gestureState{kind: kind, surface: s.pointer.focus}
	c, r := s.gestureTarget()
	if r == nil {
		return nil
	}
	serial, ms, wl := s.display.NextSerial(), wire.Millis(at), s.gesture.surface.Resource()
	var errs []error
	switch kind {
	case backend.Swipe:
		for _, g := range r.swipes {
			errs = append(errs, c.Sent(g.SendBegin(serial, ms, wl, fingers)))
		}
	case backend.Pinch:
		for _, g := range r.pinches {
			errs = append(errs, c.Sent(g.SendBegin(serial, ms, wl, fingers)))
		}
	case backend.Hold:
		for _, g := range r.holds {
			errs = append(errs, c.Sent(g.SendBegin(serial, ms, wl, fingers)))
		}
	default:
		return fmt.Errorf("seat: gesture %d is neither swipe, pinch nor hold", kind)
	}
	return errors.Join(errs...)
}

func (s *Seat) SwipeUpdate(at time.Duration, dx, dy float64) error {
	c, r := s.gestureTarget()
	if r == nil || s.gesture.kind != backend.Swipe {
		return nil
	}
	errs := make([]error, 0, len(r.swipes))
	for _, g := range r.swipes {
		errs = append(errs, c.Sent(g.SendUpdate(wire.Millis(at), wire.FixedFromFloat(dx), wire.FixedFromFloat(dy))))
	}
	return errors.Join(errs...)
}

func (s *Seat) PinchUpdate(at time.Duration, dx, dy, scale, rotation float64) error {
	c, r := s.gestureTarget()
	if r == nil || s.gesture.kind != backend.Pinch {
		return nil
	}
	errs := make([]error, 0, len(r.pinches))
	for _, g := range r.pinches {
		errs = append(errs, c.Sent(g.SendUpdate(wire.Millis(at), wire.FixedFromFloat(dx), wire.FixedFromFloat(dy), wire.FixedFromFloat(scale), wire.FixedFromFloat(rotation))))
	}
	return errors.Join(errs...)
}

func (s *Seat) GestureEnd(at time.Duration, kind backend.Gesture, cancelled bool) error {
	c, r := s.gestureTarget()
	active := s.gesture.kind
	s.gesture = gestureState{}
	if r == nil || active != kind {
		return nil
	}
	serial, ms, flag := s.display.NextSerial(), wire.Millis(at), int32(0)
	if cancelled {
		flag = 1
	}
	var errs []error
	switch kind {
	case backend.Swipe:
		for _, g := range r.swipes {
			errs = append(errs, c.Sent(g.SendEnd(serial, ms, flag)))
		}
	case backend.Pinch:
		for _, g := range r.pinches {
			errs = append(errs, c.Sent(g.SendEnd(serial, ms, flag)))
		}
	case backend.Hold:
		for _, g := range r.holds {
			errs = append(errs, c.Sent(g.SendEnd(serial, ms, flag)))
		}
	}
	return errors.Join(errs...)
}
