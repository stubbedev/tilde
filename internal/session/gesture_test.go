package session

import (
	"reflect"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
)

type fakeGesture struct {
	typ       int32
	usec      uint64
	fingers   int32
	cancelled int32
	dx, dy    float64
	scale     float64
	angle     float64
}

func TestLibinputGesturesBecomeBackendEvents(t *testing.T) {
	events := []fakeGesture{
		{typ: evSwipeBegin, usec: 1000, fingers: 3},
		{typ: evSwipeUpdate, usec: 2000, dx: 4.5, dy: -1},
		{typ: evSwipeEnd, usec: 3000},
		{typ: evPinchBegin, usec: 4000, fingers: 2},
		{typ: evPinchUpdate, usec: 5000, dx: 1, dy: 2, scale: 1.25, angle: -10},
		{typ: evPinchEnd, usec: 6000, cancelled: 1},
		{typ: evHoldBegin, usec: 7000, fingers: 4},
		{typ: evHoldEnd, usec: 8000, cancelled: 1},
	}
	at := func(i uintptr) fakeGesture { return events[i-1] }
	in := &Libinput{fn: libinputFuncs{
		eventType:    func(ev uintptr) int32 { return at(ev).typ },
		gestureEvent: func(ev uintptr) uintptr { return ev },
		gestureTime:  func(g uintptr) uint64 { return at(g).usec },
		fingers:      func(g uintptr) int32 { return at(g).fingers },
		cancelled:    func(g uintptr) int32 { return at(g).cancelled },
		gestureDX:    func(g uintptr) float64 { return at(g).dx },
		gestureDY:    func(g uintptr) float64 { return at(g).dy },
		gestureScale: func(g uintptr) float64 { return at(g).scale },
		gestureAngle: func(g uintptr) float64 { return at(g).angle },
	}}
	var got []backend.Event
	for i := range events {
		got = in.translate(got, &InputDevice{}, uintptr(i+1))
	}
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	want := []backend.Event{
		backend.GestureBegin{Time: ms(1), Gesture: backend.Swipe, Fingers: 3},
		backend.SwipeUpdate{Time: ms(2), DX: 4.5, DY: -1},
		backend.GestureEnd{Time: ms(3), Gesture: backend.Swipe},
		backend.GestureBegin{Time: ms(4), Gesture: backend.Pinch, Fingers: 2},
		backend.PinchUpdate{Time: ms(5), DX: 1, DY: 2, Scale: 1.25, Rotation: -10},
		backend.GestureEnd{Time: ms(6), Gesture: backend.Pinch, Cancelled: true},
		backend.GestureBegin{Time: ms(7), Gesture: backend.Hold, Fingers: 4},
		backend.GestureEnd{Time: ms(8), Gesture: backend.Hold, Cancelled: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("translated\n%+v\nwant\n%+v", got, want)
	}
}
