package testclient

import (
	"fmt"

	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/wire"
)

type Input struct {
	Log      []string
	Surfaces map[wire.ObjectID]string
	Serial   uint32
}

func (in *Input) name(s *cw.WlSurface) string {
	if s == nil {
		return "nil"
	}
	if n, ok := in.Surfaces[s.Base().ID()]; ok {
		return n
	}
	return fmt.Sprint("surface@", s.Base().ID())
}

func (in *Input) note(format string, args ...any) error {
	in.Log = append(in.Log, fmt.Sprintf(format, args...))
	return nil
}

func (in *Input) Name(s *cw.WlSurface, name string) {
	in.Surfaces[s.Base().ID()] = name
}

type seatEvents struct{ caps *cw.WlSeatCapability }

func (e seatEvents) Capabilities(_ *cw.WlSeat, caps cw.WlSeatCapability) error {
	*e.caps = caps
	return nil
}

func (seatEvents) Name(*cw.WlSeat, string) error { return nil }

func (c *Client) Seat() (*cw.WlSeat, *Input) {
	c.t.Helper()
	var caps cw.WlSeatCapability
	seat, err := cw.NewWlSeat(c.Display, 9, seatEvents{&caps})
	c.must(err)
	c.must(c.registry.Bind(c.globals.byName["wl_seat"].name, seat))
	c.must(c.Display.Roundtrip())
	in := &Input{Surfaces: map[wire.ObjectID]string{}}
	if caps.Has(cw.WlSeatCapabilityPointer) {
		_, err := seat.GetPointer(pointerEvents{in})
		c.must(err)
	}
	if caps.Has(cw.WlSeatCapabilityTouch) {
		_, err := seat.GetTouch(touchEvents{in})
		c.must(err)
	}
	if caps.Has(cw.WlSeatCapabilityKeyboard) {
		_, err := seat.GetKeyboard(keyboardEvents{in})
		c.must(err)
	}
	c.must(c.Display.Roundtrip())
	return seat, in
}

type pointerEvents struct{ in *Input }

func (e pointerEvents) Enter(_ *cw.WlPointer, serial uint32, s *cw.WlSurface, x, y wire.Fixed) error {
	e.in.Serial = serial
	return e.in.note("enter %s %g,%g", e.in.name(s), x.Float(), y.Float())
}

func (e pointerEvents) Leave(_ *cw.WlPointer, _ uint32, s *cw.WlSurface) error {
	return e.in.note("leave %s", e.in.name(s))
}

func (e pointerEvents) Motion(_ *cw.WlPointer, _ uint32, x, y wire.Fixed) error {
	return e.in.note("motion %g,%g", x.Float(), y.Float())
}

func (e pointerEvents) Button(_ *cw.WlPointer, _, _, button uint32, state cw.WlPointerButtonState) error {
	return e.in.note("button %d %d", button, state)
}

func (e pointerEvents) Axis(_ *cw.WlPointer, _ uint32, axis cw.WlPointerAxis, v wire.Fixed) error {
	return e.in.note("axis %d %g", axis, v.Float())
}

func (e pointerEvents) Frame(*cw.WlPointer) error {
	return e.in.note("frame")
}

func (e pointerEvents) AxisSource(_ *cw.WlPointer, src cw.WlPointerAxisSource) error {
	return e.in.note("axis_source %d", src)
}

func (e pointerEvents) AxisStop(_ *cw.WlPointer, _ uint32, axis cw.WlPointerAxis) error {
	return e.in.note("axis_stop %d", axis)
}

func (e pointerEvents) AxisDiscrete(_ *cw.WlPointer, axis cw.WlPointerAxis, d int32) error {
	return e.in.note("axis_discrete %d %d", axis, d)
}

func (e pointerEvents) AxisValue120(_ *cw.WlPointer, axis cw.WlPointerAxis, v int32) error {
	return e.in.note("axis_value120 %d %d", axis, v)
}

func (e pointerEvents) AxisRelativeDirection(_ *cw.WlPointer, axis cw.WlPointerAxis, d cw.WlPointerAxisRelativeDirection) error {
	return e.in.note("axis_relative_direction %d %d", axis, d)
}

func (e pointerEvents) Warp(*cw.WlPointer, wire.Fixed, wire.Fixed) error { return nil }

type touchEvents struct{ in *Input }

func (e touchEvents) Down(_ *cw.WlTouch, _, _ uint32, s *cw.WlSurface, id int32, x, y wire.Fixed) error {
	return e.in.note("down %d %s %g,%g", id, e.in.name(s), x.Float(), y.Float())
}

func (e touchEvents) Up(_ *cw.WlTouch, _, _ uint32, id int32) error {
	return e.in.note("up %d", id)
}

func (e touchEvents) Motion(_ *cw.WlTouch, _ uint32, id int32, x, y wire.Fixed) error {
	return e.in.note("touch_motion %d %g,%g", id, x.Float(), y.Float())
}

func (e touchEvents) Frame(*cw.WlTouch) error  { return e.in.note("touch_frame") }
func (e touchEvents) Cancel(*cw.WlTouch) error { return e.in.note("cancel") }

func (e touchEvents) Shape(*cw.WlTouch, int32, wire.Fixed, wire.Fixed) error { return nil }
func (e touchEvents) Orientation(*cw.WlTouch, int32, wire.Fixed) error       { return nil }

type keyboardEvents struct{ in *Input }

func (e keyboardEvents) Keymap(_ *cw.WlKeyboard, _ cw.WlKeyboardKeymapFormat, fd int, _ uint32) error {
	return closeFD(fd)
}

func (e keyboardEvents) Enter(_ *cw.WlKeyboard, _ uint32, s *cw.WlSurface, _ []byte) error {
	return e.in.note("kb_enter %s", e.in.name(s))
}

func (e keyboardEvents) Leave(_ *cw.WlKeyboard, _ uint32, s *cw.WlSurface) error {
	return e.in.note("kb_leave %s", e.in.name(s))
}

func (e keyboardEvents) Key(_ *cw.WlKeyboard, _, _, key uint32, state cw.WlKeyboardKeyState) error {
	return e.in.note("key %d %d", key, state)
}

func (keyboardEvents) Modifiers(*cw.WlKeyboard, uint32, uint32, uint32, uint32, uint32) error {
	return nil
}

func (keyboardEvents) RepeatInfo(*cw.WlKeyboard, int32, int32) error { return nil }
