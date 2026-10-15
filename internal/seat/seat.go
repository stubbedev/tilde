// Package seat implements wl_seat with its keyboard, pointer and touch:
// keymaps, focus, and delivering input to the focused clients.
package seat

import (
	"errors"
	"fmt"
	"slices"

	scs "github.com/stubbedev/tilde/internal/protocol/server/cursorshape"
	spg "github.com/stubbedev/tilde/internal/protocol/server/pointergestures"
	srp "github.com/stubbedev/tilde/internal/protocol/server/relativepointer"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/xkb"
)

const (
	seatVersion = 9
	seatName    = "seat0"

	DefaultRepeatRate  = 50
	DefaultRepeatDelay = 300

	recentPresses = 16
)

type Config struct {
	Keymap      *xkb.Keymap
	RepeatRate  int32
	RepeatDelay int32
}

type clientResources struct {
	client    *server.Client
	seats     []*sw.WlSeat
	keyboards []*sw.WlKeyboard
	pointers  []*sw.WlPointer
	relatives []*srp.ZwpRelativePointerV1
	swipes    []*spg.ZwpPointerGestureSwipeV1
	pinches   []*spg.ZwpPointerGesturePinchV1
	holds     []*spg.ZwpPointerGestureHoldV1
	touches   []*sw.WlTouch
	presses   [recentPresses]uint32
	pressed   int
}

type Listener interface {
	CursorChanged() error
	KeyboardFocusChanged(focus *surface.Surface) error
	PointerFocusChanged(focus *surface.Surface) error
}

type Seat struct {
	display  *server.Display
	listener Listener
	cfg      Config
	state    *xkb.State
	devices  map[sw.WlSeatCapability]int
	caps     sw.WlSeatCapability
	everCaps sw.WlSeatCapability
	clients  []*clientResources
	keyboard keyboardState
	keymaps  map[*sw.WlKeyboard]sentKeymap
	pointer  pointerState
	gesture  gestureState
	touch    touchState
}

func New(d *server.Display, cfg Config, listener Listener) (*Seat, error) {
	p, err := Prepare(cfg)
	if err != nil {
		return nil, err
	}
	s := &Seat{display: d, listener: listener, cfg: p.cfg, state: p.state, devices: map[sw.WlSeatCapability]int{}, keymaps: map[*sw.WlKeyboard]sentKeymap{}}
	if _, err := d.AddGlobal(sw.WlSeatInterface, seatVersion, s.bind); err != nil {
		s.Close()
		return nil, err
	}
	if _, err := d.AddGlobal(scs.WpCursorShapeManagerV1Interface, cursorShapeVersion, s.bindCursorShape); err != nil {
		s.Close()
		return nil, err
	}
	if _, err := d.AddGlobal(srp.ZwpRelativePointerManagerV1Interface, relativePointerVersion, s.bindRelativePointer); err != nil {
		s.Close()
		return nil, err
	}
	if _, err := d.AddGlobal(spg.ZwpPointerGesturesV1Interface, gesturesVersion, s.bindGestures); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Seat) Close() {
	s.state.Close()
}

func (s *Seat) lookup(c *server.Client) *clientResources {
	for _, r := range s.clients {
		if r.client == c {
			return r
		}
	}
	return nil
}

func (s *Seat) resources(c *server.Client) *clientResources {
	if r := s.lookup(c); r != nil {
		return r
	}
	r := &clientResources{client: c}
	s.clients = append(s.clients, r)
	return r
}

func (s *Seat) eventSerial(c *server.Client, press bool) uint32 {
	serial := s.display.NextSerial()
	if r := s.lookup(c); press && r != nil {
		r.presses[r.pressed%recentPresses] = serial
		r.pressed++
	}
	return serial
}

func (s *Seat) PressSerial(c *server.Client, serial uint32) bool {
	r := s.lookup(c)
	return r != nil && serial != 0 && slices.Contains(r.presses[:], serial)
}

func (s *Seat) forget(r *clientResources) {
	if len(r.seats) == 0 && len(r.keyboards) == 0 && len(r.pointers) == 0 && len(r.relatives) == 0 && len(r.touches) == 0 &&
		len(r.swipes) == 0 && len(r.pinches) == 0 && len(r.holds) == 0 {
		s.clients = slices.DeleteFunc(s.clients, func(x *clientResources) bool { return x == r })
	}
}

func track[T wire.Resource](s *Seat, r *clientResources, list *[]T, res T) {
	*list = append(*list, res)
	r.client.OnDestroy(res, func() error {
		*list = slices.DeleteFunc(*list, func(x T) bool { return wire.Resource(x) == wire.Resource(res) })
		s.forget(r)
		return nil
	})
}

func (s *Seat) bind(c *server.Client, id wire.ObjectID, version uint32) error {
	res, err := sw.NewWlSeat(c, id, version, seatRequests{s, c})
	if err != nil {
		return err
	}
	r := s.resources(c)
	track(s, r, &r.seats, res)
	if err := res.SendCapabilities(s.caps); err != nil {
		return err
	}
	if version >= 2 {
		return res.SendName(seatName)
	}
	return nil
}

func (s *Seat) AddDevice(caps sw.WlSeatCapability) error {
	for _, c := range []sw.WlSeatCapability{sw.WlSeatCapabilityKeyboard, sw.WlSeatCapabilityPointer, sw.WlSeatCapabilityTouch} {
		if caps.Has(c) {
			s.devices[c]++
		}
	}
	return s.updateCapabilities()
}

func (s *Seat) RemoveDevice(caps sw.WlSeatCapability) error {
	for _, c := range []sw.WlSeatCapability{sw.WlSeatCapabilityKeyboard, sw.WlSeatCapabilityPointer, sw.WlSeatCapabilityTouch} {
		if !caps.Has(c) {
			continue
		}
		if s.devices[c] == 0 {
			return fmt.Errorf("seat: removed a device with capability %d that was never added", c)
		}
		s.devices[c]--
	}
	return s.updateCapabilities()
}

func (s *Seat) updateCapabilities() error {
	caps := sw.WlSeatCapability(0)
	for c, n := range s.devices {
		if n > 0 {
			caps |= c
		}
	}
	if caps == s.caps {
		return nil
	}
	s.caps = caps
	s.everCaps |= caps
	var errs []error
	for _, r := range s.clients {
		for _, res := range r.seats {
			errs = append(errs, r.client.Sent(res.SendCapabilities(caps)))
		}
	}
	return errors.Join(errs...)
}

func (s *Seat) SurfaceDestroyed(target *surface.Surface) error {
	var err error
	if s.keyboard.focus == target {
		s.keyboard.focus = nil
		err = s.listener.KeyboardFocusChanged(nil)
	}
	if s.pointer.focus == target {
		s.pointer.focus, s.pointer.buttons = nil, nil
		err = errors.Join(err, s.listener.PointerFocusChanged(nil))
	}
	if s.pointer.cursor.surface == target {
		s.pointer.cursor = Cursor{}
	}
	if s.gesture.surface == target {
		s.gesture = gestureState{}
	}
	s.touch.points = slices.DeleteFunc(s.touch.points, func(p touchPoint) bool { return p.surface == target })
	return err
}

type seatRequests struct {
	s *Seat
	c *server.Client
}

func missing(obj *sw.WlSeat, what string) error {
	return obj.ProtocolError(sw.WlSeatErrorMissingCapability, "the seat has never had a "+what)
}

func (r seatRequests) Release(*sw.WlSeat) error {
	return nil
}
