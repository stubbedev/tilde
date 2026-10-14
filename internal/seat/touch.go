package seat

import (
	"errors"
	"slices"
	"time"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

type touchPoint struct {
	id      int32
	surface *surface.Surface
	serial  uint32
}

type touchState struct {
	points []touchPoint
	framed []*server.Client
}

func (s *Seat) TouchSurface(id int32) *surface.Surface {
	for _, p := range s.touch.points {
		if p.id == id {
			return p.surface
		}
	}
	return nil
}

func (s *Seat) TouchBySerial(target *surface.Surface, serial uint32) (int32, bool) {
	for _, p := range s.touch.points {
		if p.surface == target && p.serial == serial {
			return p.id, true
		}
	}
	return 0, false
}

func (s *Seat) DropTouch(id int32) {
	s.touch.points = slices.DeleteFunc(s.touch.points, func(p touchPoint) bool { return p.id == id })
}

func (s *Seat) touchesOf(target *surface.Surface) (*server.Client, []*sw.WlTouch) {
	c := target.Client()
	if !slices.Contains(s.touch.framed, c) {
		s.touch.framed = append(s.touch.framed, c)
	}
	if r := s.lookup(c); r != nil {
		return c, r.touches
	}
	return c, nil
}

func (s *Seat) TouchDown(at time.Duration, id int32, target *surface.Surface, x, y float64) error {
	if s.TouchSurface(id) != nil {
		return nil
	}
	c, touches := s.touchesOf(target)
	serial := s.eventSerial(c, true)
	s.touch.points = append(s.touch.points, touchPoint{id: id, surface: target, serial: serial})
	var errs []error
	for _, t := range touches {
		errs = append(errs, c.Sent(t.SendDown(serial, wire.Millis(at), target.Resource(), id, wire.FixedFromFloat(x), wire.FixedFromFloat(y))))
	}
	return errors.Join(errs...)
}

func (s *Seat) TouchMotion(at time.Duration, id int32, x, y float64) error {
	target := s.TouchSurface(id)
	if target == nil {
		return nil
	}
	c, touches := s.touchesOf(target)
	var errs []error
	for _, t := range touches {
		errs = append(errs, c.Sent(t.SendMotion(wire.Millis(at), id, wire.FixedFromFloat(x), wire.FixedFromFloat(y))))
	}
	return errors.Join(errs...)
}

func (s *Seat) TouchUp(at time.Duration, id int32) error {
	target := s.TouchSurface(id)
	if target == nil {
		return nil
	}
	s.DropTouch(id)
	c, touches := s.touchesOf(target)
	var errs []error
	for _, t := range touches {
		errs = append(errs, c.Sent(t.SendUp(s.display.NextSerial(), wire.Millis(at), id)))
	}
	return errors.Join(errs...)
}

func (s *Seat) touchClients(send func(*sw.WlTouch) error) error {
	var errs []error
	for _, c := range s.touch.framed {
		if r := s.lookup(c); r != nil {
			for _, t := range r.touches {
				errs = append(errs, c.Sent(send(t)))
			}
		}
	}
	s.touch.framed = s.touch.framed[:0]
	return errors.Join(errs...)
}

func (s *Seat) TouchFrame() error {
	return s.touchClients((*sw.WlTouch).SendFrame)
}

func (s *Seat) TouchCancel() error {
	for _, p := range s.touch.points {
		if !slices.Contains(s.touch.framed, p.surface.Client()) {
			s.touch.framed = append(s.touch.framed, p.surface.Client())
		}
	}
	s.touch.points = nil
	return s.touchClients((*sw.WlTouch).SendCancel)
}

func (r seatRequests) GetTouch(obj *sw.WlSeat, t *sw.WlTouch) (sw.WlTouchHandler, error) {
	s := r.s
	if !s.everCaps.Has(sw.WlSeatCapabilityTouch) {
		return nil, missing(obj, "touch device")
	}
	res := s.resources(r.c)
	track(s, res, &res.touches, t)
	return touchRequests{}, nil
}

type touchRequests struct{}

func (touchRequests) Release(*sw.WlTouch) error {
	return nil
}
