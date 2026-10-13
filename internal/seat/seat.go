// Package seat implements wl_seat and its keyboard: keymaps, focus, keys and
// modifiers.
package seat

import (
	"encoding/binary"
	"errors"
	"slices"
	"time"

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
}

type Seat struct {
	display   *server.Display
	cfg       Config
	state     *xkb.State
	keyboards int
	caps      sw.WlSeatCapability
	everCaps  sw.WlSeatCapability
	clients   []*clientResources
	focus     *surface.Surface
	pressed   []uint32
}

func New(d *server.Display, cfg Config) (*Seat, error) {
	if cfg.Keymap == nil {
		return nil, errors.New("seat: no keymap")
	}
	st, err := cfg.Keymap.NewState()
	if err != nil {
		return nil, err
	}
	s := &Seat{display: d, cfg: cfg, state: st}
	if _, err := d.AddGlobal(sw.WlSeatInterface, seatVersion, s.bind); err != nil {
		st.Close()
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

func (s *Seat) forget(r *clientResources) {
	if len(r.seats) == 0 && len(r.keyboards) == 0 {
		s.clients = slices.DeleteFunc(s.clients, func(x *clientResources) bool { return x == r })
	}
}

func (s *Seat) bind(c *server.Client, id wire.ObjectID, version uint32) error {
	res, err := sw.NewWlSeat(c, id, version, seatRequests{s, c})
	if err != nil {
		return err
	}
	r := s.resources(c)
	r.seats = append(r.seats, res)
	c.OnDestroy(res, func() {
		r.seats = slices.DeleteFunc(r.seats, func(x *sw.WlSeat) bool { return x == res })
		s.forget(r)
	})
	if err := res.SendCapabilities(s.caps); err != nil {
		return err
	}
	if version >= 2 {
		return res.SendName(seatName)
	}
	return nil
}

func (s *Seat) AddKeyboard() error {
	s.keyboards++
	return s.updateCapabilities()
}

func (s *Seat) RemoveKeyboard() error {
	if s.keyboards == 0 {
		return errors.New("seat: keyboard removed that was never added")
	}
	s.keyboards--
	return s.updateCapabilities()
}

func (s *Seat) updateCapabilities() error {
	caps := sw.WlSeatCapability(0)
	if s.keyboards > 0 {
		caps |= sw.WlSeatCapabilityKeyboard
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

func (s *Seat) Focus() *surface.Surface {
	return s.focus
}

func (s *Seat) keysArray() []byte {
	keys := make([]byte, 0, 4*len(s.pressed))
	for _, k := range s.pressed {
		keys = binary.NativeEndian.AppendUint32(keys, k)
	}
	return keys
}

func (s *Seat) sendModifiers(c *server.Client, kb *sw.WlKeyboard) error {
	m := s.state.Modifiers()
	return c.Sent(kb.SendModifiers(s.display.NextSerial(), m.Depressed, m.Latched, m.Locked, m.Group))
}

func (s *Seat) enter(c *server.Client, kb *sw.WlKeyboard) error {
	if err := c.Sent(kb.SendEnter(s.display.NextSerial(), s.focus.Resource(), s.keysArray())); err != nil {
		return err
	}
	return s.sendModifiers(c, kb)
}

func (s *Seat) SetKeyboardFocus(target *surface.Surface) error {
	if target == s.focus {
		return nil
	}
	var errs []error
	if old := s.focus; old != nil {
		if r := s.lookup(old.Client()); r != nil {
			for _, kb := range r.keyboards {
				errs = append(errs, old.Client().Sent(kb.SendLeave(s.display.NextSerial(), old.Resource())))
			}
		}
	}
	s.focus = target
	if target != nil {
		if r := s.lookup(target.Client()); r != nil {
			for _, kb := range r.keyboards {
				errs = append(errs, s.enter(target.Client(), kb))
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Seat) SurfaceDestroyed(target *surface.Surface) {
	if s.focus == target {
		s.focus = nil
	}
}

func (s *Seat) Key(code uint32, pressed bool, at time.Duration) error {
	i := slices.Index(s.pressed, code)
	switch {
	case pressed && i >= 0, !pressed && i < 0:
		return nil
	case pressed:
		s.pressed = append(s.pressed, code)
	default:
		s.pressed = slices.Delete(s.pressed, i, i+1)
	}
	modsChanged := s.state.Key(code, pressed)
	if s.focus == nil {
		return nil
	}
	c := s.focus.Client()
	r := s.lookup(c)
	if r == nil {
		return nil
	}
	state := sw.WlKeyboardKeyStateReleased
	if pressed {
		state = sw.WlKeyboardKeyStatePressed
	}
	ms := wire.Millis(at)
	var errs []error
	for _, kb := range r.keyboards {
		errs = append(errs, c.Sent(kb.SendKey(s.display.NextSerial(), ms, code, state)))
		if modsChanged {
			errs = append(errs, s.sendModifiers(c, kb))
		}
	}
	return errors.Join(errs...)
}

type seatRequests struct {
	s *Seat
	c *server.Client
}

func missing(obj *sw.WlSeat, what string) error {
	return obj.ProtocolError(sw.WlSeatErrorMissingCapability, "the seat has never had a "+what)
}

func (r seatRequests) GetKeyboard(obj *sw.WlSeat, kb *sw.WlKeyboard) (sw.WlKeyboardHandler, error) {
	s := r.s
	if !s.everCaps.Has(sw.WlSeatCapabilityKeyboard) {
		return nil, missing(obj, "keyboard")
	}
	res := s.resources(r.c)
	res.keyboards = append(res.keyboards, kb)
	r.c.OnDestroy(kb, func() {
		res.keyboards = slices.DeleteFunc(res.keyboards, func(x *sw.WlKeyboard) bool { return x == kb })
		s.forget(res)
	})
	if err := kb.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, s.cfg.Keymap.FD(), s.cfg.Keymap.Size()); err != nil {
		return nil, err
	}
	if kb.Base().Version() >= 4 {
		if err := kb.SendRepeatInfo(s.cfg.RepeatRate, s.cfg.RepeatDelay); err != nil {
			return nil, err
		}
	}
	if s.focus != nil && s.focus.Client() == r.c {
		if err := s.enter(r.c, kb); err != nil {
			return nil, err
		}
	}
	return keyboardRequests{}, nil
}

func (r seatRequests) GetPointer(obj *sw.WlSeat, _ *sw.WlPointer) (sw.WlPointerHandler, error) {
	return nil, missing(obj, "pointer")
}

func (r seatRequests) GetTouch(obj *sw.WlSeat, _ *sw.WlTouch) (sw.WlTouchHandler, error) {
	return nil, missing(obj, "touch device")
}

func (r seatRequests) Release(*sw.WlSeat) error {
	return nil
}

type keyboardRequests struct{}

func (keyboardRequests) Release(*sw.WlKeyboard) error {
	return nil
}
