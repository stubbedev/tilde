package seat

import (
	"errors"
	"image"
	"slices"
	"time"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const cursorRole = "wl_pointer-cursor"

type Axis struct {
	Orientation sw.WlPointerAxis
	Source      sw.WlPointerAxisSource
	Value       float64
	Value120    int32
}

type Cursor interface {
	cursor()
}

type DefaultCursor struct{}

type HiddenCursor struct{}

type SurfaceCursor struct {
	Surface *surface.Surface
	Hotspot image.Point
}

type ShapeCursor struct {
	Shape string
}

func (DefaultCursor) cursor() {}
func (HiddenCursor) cursor()  {}
func (SurfaceCursor) cursor() {}
func (ShapeCursor) cursor()   {}

type clientCursor struct {
	client *server.Client
	want   Cursor
}

type pointerState struct {
	focus       *surface.Surface
	enterSerial uint32
	buttons     []uint32
	cursor      clientCursor
	framed      []*server.Client
}

func (s *Seat) HasPointer() bool {
	return s.devices[sw.WlSeatCapabilityPointer] > 0
}

func (s *Seat) PointerFocus() *surface.Surface {
	return s.pointer.focus
}

func (s *Seat) PointerGrabbed() bool {
	return len(s.pointer.buttons) > 0
}

func (s *Seat) Cursor() Cursor {
	if s.pointer.focus == nil || s.pointer.cursor.client != s.pointer.focus.Client() {
		return DefaultCursor{}
	}
	return s.pointer.cursor.want
}

func (s *Seat) focusPointers() (*server.Client, []*sw.WlPointer) {
	if s.pointer.focus == nil {
		return nil, nil
	}
	c := s.pointer.focus.Client()
	if r := s.lookup(c); r != nil {
		return c, r.pointers
	}
	return c, nil
}

func (s *Seat) SetPointerFocus(target *surface.Surface, x, y float64) error {
	p := &s.pointer
	if target == p.focus {
		return nil
	}
	var errs []error
	if c, pointers := s.focusPointers(); c != nil {
		for _, ptr := range pointers {
			errs = append(errs, c.Sent(ptr.SendLeave(s.display.NextSerial(), p.focus.Resource())))
		}
		if !slices.Contains(p.framed, c) {
			p.framed = append(p.framed, c)
		}
	}
	p.focus, p.buttons = target, nil
	if c, pointers := s.focusPointers(); c != nil {
		p.enterSerial = s.display.NextSerial()
		for _, ptr := range pointers {
			errs = append(errs, c.Sent(ptr.SendEnter(p.enterSerial, target.Resource(), wire.FixedFromFloat(x), wire.FixedFromFloat(y))))
		}
	}
	return errors.Join(append(errs, s.listener.PointerFocusChanged(target))...)
}

func (s *Seat) PointerMotion(at time.Duration, x, y float64) error {
	c, pointers := s.focusPointers()
	var errs []error
	for _, ptr := range pointers {
		errs = append(errs, c.Sent(ptr.SendMotion(wire.Millis(at), wire.FixedFromFloat(x), wire.FixedFromFloat(y))))
	}
	return errors.Join(errs...)
}

func (s *Seat) PointerButton(at time.Duration, button uint32, pressed bool) error {
	p := &s.pointer
	i := slices.Index(p.buttons, button)
	switch {
	case pressed && i >= 0, !pressed && i < 0 && p.focus == nil:
		return nil
	case pressed:
		p.buttons = append(p.buttons, button)
	case i >= 0:
		p.buttons = slices.Delete(p.buttons, i, i+1)
	}
	state := sw.WlPointerButtonStateReleased
	if pressed {
		state = sw.WlPointerButtonStatePressed
	}
	c, pointers := s.focusPointers()
	if c == nil {
		return nil
	}
	serial := s.eventSerial(c, pressed)
	var errs []error
	for _, ptr := range pointers {
		errs = append(errs, c.Sent(ptr.SendButton(serial, wire.Millis(at), button, state)))
	}
	return errors.Join(errs...)
}

func (s *Seat) PointerAxis(at time.Duration, a Axis) error {
	c, pointers := s.focusPointers()
	var errs []error
	for _, ptr := range pointers {
		errs = append(errs, c.Sent(s.sendAxis(ptr, at, a)))
	}
	return errors.Join(errs...)
}

func (s *Seat) sendAxis(ptr *sw.WlPointer, at time.Duration, a Axis) error {
	v := ptr.Base().Version()
	if v >= 5 {
		if err := ptr.SendAxisSource(a.Source); err != nil {
			return err
		}
	}
	if a.Value == 0 && a.Source == sw.WlPointerAxisSourceFinger {
		if v >= 5 {
			return ptr.SendAxisStop(wire.Millis(at), a.Orientation)
		}
		return nil
	}
	switch {
	case v >= 8 && a.Value120 != 0:
		if err := ptr.SendAxisValue120(a.Orientation, a.Value120); err != nil {
			return err
		}
	case v >= 5 && a.Value120 != 0 && a.Value120%120 == 0:
		if err := ptr.SendAxisDiscrete(a.Orientation, a.Value120/120); err != nil {
			return err
		}
	}
	if v >= 9 {
		if err := ptr.SendAxisRelativeDirection(a.Orientation, sw.WlPointerAxisRelativeDirectionIdentical); err != nil {
			return err
		}
	}
	return ptr.SendAxis(wire.Millis(at), a.Orientation, wire.FixedFromFloat(a.Value))
}

func (s *Seat) PointerFrame() error {
	p := &s.pointer
	if p.focus != nil && !slices.Contains(p.framed, p.focus.Client()) {
		p.framed = append(p.framed, p.focus.Client())
	}
	var errs []error
	for _, c := range p.framed {
		r := s.lookup(c)
		if r == nil {
			continue
		}
		for _, ptr := range r.pointers {
			if ptr.Base().Version() >= 5 {
				errs = append(errs, c.Sent(ptr.SendFrame()))
			}
		}
	}
	clear(p.framed)
	p.framed = p.framed[:0]
	return errors.Join(errs...)
}

func (r seatRequests) GetPointer(obj *sw.WlSeat, ptr *sw.WlPointer) (sw.WlPointerHandler, error) {
	s := r.s
	if !s.everCaps.Has(sw.WlSeatCapabilityPointer) {
		return nil, missing(obj, "pointer")
	}
	res := s.resources(r.c)
	track(s, res, &res.pointers, ptr)
	return pointerRequests{s, r.c}, nil
}

type cursorSurface struct {
	s *Seat
}

func (cursorSurface) RoleName() string {
	return cursorRole
}

func (c cursorSurface) Committed(sf *surface.Surface) error {
	cur := &c.s.pointer.cursor
	if sc, ok := cur.want.(SurfaceCursor); ok && sc.Surface == sf {
		sc.Hotspot = sc.Hotspot.Sub(sf.Offset())
		cur.want = sc
	}
	return nil
}

type pointerRequests struct {
	s *Seat
	c *server.Client
}

func (s *Seat) PointerEntered(c *server.Client, serial uint32) bool {
	return s.pointer.focus != nil && s.pointer.focus.Client() == c && serial == s.pointer.enterSerial
}

func (r pointerRequests) SetCursor(obj *sw.WlPointer, serial uint32, wl *sw.WlSurface, hx, hy int32) error {
	s := r.s
	if !s.PointerEntered(r.c, serial) {
		return nil
	}
	cur := clientCursor{client: r.c, want: HiddenCursor{}}
	if wl != nil {
		sf, err := surface.From(wl)
		if err != nil {
			return err
		}
		if sf.Role() == nil || sf.Role().RoleName() != cursorRole {
			if err := sf.SetRole(cursorSurface{s}); err != nil {
				return obj.ProtocolError(sw.WlPointerErrorRole, err.Error())
			}
		}
		cur.want = SurfaceCursor{Surface: sf, Hotspot: image.Pt(int(hx), int(hy))}
	}
	s.pointer.cursor = cur
	return s.listener.CursorChanged()
}

func (r pointerRequests) Release(*sw.WlPointer) error {
	return nil
}
