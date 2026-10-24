package compositor

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	svk "github.com/stubbedev/lys-wm/internal/protocol/server/virtualkeyboard"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	svp "github.com/stubbedev/lys-wm/internal/protocol/server/wlrvirtualpointer"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const (
	virtualKeyboardVersion = 1
	virtualPointerVersion  = 2
)

func millis(ms uint32) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

func (c *Compositor) addVirtualInput(d *server.Display) error {
	if _, err := d.AddGlobal(svk.ZwpVirtualKeyboardManagerV1Interface, virtualKeyboardVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := svk.NewZwpVirtualKeyboardManagerV1(cl, id, version, virtualKeyboards{c, cl})
		return err
	}); err != nil {
		return err
	}
	_, err := d.AddGlobal(svp.ZwlrVirtualPointerManagerV1Interface, virtualPointerVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := svp.NewZwlrVirtualPointerManagerV1(cl, id, version, virtualPointers{c})
		return err
	})
	return err
}

type virtualKeyboards struct {
	c  *Compositor
	cl *server.Client
}

func (m virtualKeyboards) CreateVirtualKeyboard(_ *svk.ZwpVirtualKeyboardManagerV1, _ *sw.WlSeat, res *svk.ZwpVirtualKeyboardV1) (svk.ZwpVirtualKeyboardV1Handler, error) {
	v := &virtualKeyboard{c: m.c, kb: seat.NewVirtualKeyboard()}
	server.OnDestroy(res, v.kb.Close)
	return v, nil
}

type virtualKeyboard struct {
	c  *Compositor
	kb *seat.VirtualKeyboard
}

func (v *virtualKeyboard) Keymap(_ *svk.ZwpVirtualKeyboardV1, format uint32, fd int, size uint32) error {
	if sw.WlKeyboardKeymapFormat(format) != sw.WlKeyboardKeymapFormatXkbV1 {
		return errors.Join(v.kb.Close(), unix.Close(fd))
	}
	return v.kb.SetKeymap(fd, size)
}

func (v *virtualKeyboard) Key(obj *svk.ZwpVirtualKeyboardV1, ms, key, state uint32) error {
	if !v.kb.HasKeymap() {
		return obj.ProtocolError(svk.ZwpVirtualKeyboardV1ErrorNoKeymap, "key before keymap")
	}
	return errors.Join(v.c.idle.Activity(), v.c.seat.VirtualKey(v.kb, key, state == uint32(sw.WlKeyboardKeyStatePressed), millis(ms)))
}

func (v *virtualKeyboard) Modifiers(obj *svk.ZwpVirtualKeyboardV1, depressed, latched, locked, group uint32) error {
	if !v.kb.HasKeymap() {
		return obj.ProtocolError(svk.ZwpVirtualKeyboardV1ErrorNoKeymap, "modifiers before keymap")
	}
	return v.c.seat.VirtualModifiers(v.kb, depressed, latched, locked, group)
}

func (*virtualKeyboard) Destroy(*svk.ZwpVirtualKeyboardV1) error {
	return nil
}

type virtualDevice struct{}

func (virtualDevice) Name() string                     { return "virtual-pointer" }
func (virtualDevice) Capabilities() backend.Capability { return backend.Pointer }

type virtualPointers struct{ c *Compositor }

func (virtualPointers) Destroy(*svp.ZwlrVirtualPointerManagerV1) error {
	return nil
}

func (m virtualPointers) CreateVirtualPointer(_ *svp.ZwlrVirtualPointerManagerV1, _ *sw.WlSeat, _ *svp.ZwlrVirtualPointerV1) (svp.ZwlrVirtualPointerV1Handler, error) {
	return &virtualPointer{c: m.c}, nil
}

func (m virtualPointers) CreateVirtualPointerWithOutput(_ *svp.ZwlrVirtualPointerManagerV1, _ *sw.WlSeat, wl *sw.WlOutput, _ *svp.ZwlrVirtualPointerV1) (svp.ZwlrVirtualPointerV1Handler, error) {
	var o *output.Output
	if wl != nil {
		o = m.c.layout.Owner(wl)
	}
	return &virtualPointer{c: m.c, output: o}, nil
}

type virtualPointer struct {
	c      *Compositor
	output *output.Output
	source backend.AxisSource
	axes   []backend.PointerAxis
	stops  []backend.PointerAxis
}

func (v *virtualPointer) input(e backend.Event) error {
	return v.c.Input(virtualDevice{}, e)
}

func (v *virtualPointer) Motion(_ *svp.ZwlrVirtualPointerV1, ms uint32, dx, dy wire.Fixed) error {
	x, y := dx.Float(), dy.Float()
	return v.input(backend.PointerMotion{Time: millis(ms), DX: x, DY: y, UnaccelDX: x, UnaccelDY: y})
}

func (v *virtualPointer) MotionAbsolute(_ *svp.ZwlrVirtualPointerV1, ms, x, y, xExtent, yExtent uint32) error {
	if xExtent == 0 || yExtent == 0 {
		return nil
	}
	fx, fy := float64(x)/float64(xExtent), float64(y)/float64(yExtent)
	if v.output != nil {
		return v.input(backend.PointerMotionAbsolute{Time: millis(ms), Output: v.output.Backend(), X: fx, Y: fy})
	}
	if err := v.c.idle.Activity(); err != nil {
		return err
	}
	return v.c.moveCursor(millis(ms), v.c.clampToOutputs(denormalize(v.c.layoutBounds(), fx, fy)))
}

func (v *virtualPointer) Button(_ *svp.ZwlrVirtualPointerV1, ms, button uint32, state sw.WlPointerButtonState) error {
	return v.input(backend.PointerButton{Time: millis(ms), Button: button, Pressed: state == sw.WlPointerButtonStatePressed})
}

func orientationOf(obj *svp.ZwlrVirtualPointerV1, axis sw.WlPointerAxis) (backend.Orientation, error) {
	switch axis {
	case sw.WlPointerAxisVerticalScroll:
		return backend.Vertical, nil
	case sw.WlPointerAxisHorizontalScroll:
		return backend.Horizontal, nil
	}
	return 0, obj.ProtocolError(svp.ZwlrVirtualPointerV1ErrorInvalidAxis, "axis %d", axis)
}

func (v *virtualPointer) axisFor(obj *svp.ZwlrVirtualPointerV1, ms uint32, axis sw.WlPointerAxis) (*backend.PointerAxis, error) {
	o, err := orientationOf(obj, axis)
	if err != nil {
		return nil, err
	}
	for i := range v.axes {
		if v.axes[i].Orientation == o {
			return &v.axes[i], nil
		}
	}
	v.axes = append(v.axes, backend.PointerAxis{Time: millis(ms), Orientation: o})
	return &v.axes[len(v.axes)-1], nil
}

func (v *virtualPointer) Axis(obj *svp.ZwlrVirtualPointerV1, ms uint32, axis sw.WlPointerAxis, value wire.Fixed) error {
	a, err := v.axisFor(obj, ms, axis)
	if err != nil {
		return err
	}
	a.Value += value.Float()
	return nil
}

func (v *virtualPointer) AxisDiscrete(obj *svp.ZwlrVirtualPointerV1, ms uint32, axis sw.WlPointerAxis, value wire.Fixed, discrete int32) error {
	a, err := v.axisFor(obj, ms, axis)
	if err != nil {
		return err
	}
	a.Value += value.Float()
	a.Value120 += discrete * 120
	return nil
}

var axisSources = map[sw.WlPointerAxisSource]backend.AxisSource{
	sw.WlPointerAxisSourceWheel:      backend.Wheel,
	sw.WlPointerAxisSourceFinger:     backend.Finger,
	sw.WlPointerAxisSourceContinuous: backend.Continuous,
	sw.WlPointerAxisSourceWheelTilt:  backend.WheelTilt,
}

func (v *virtualPointer) AxisSource(obj *svp.ZwlrVirtualPointerV1, source sw.WlPointerAxisSource) error {
	s, ok := axisSources[source]
	if !ok {
		return obj.ProtocolError(svp.ZwlrVirtualPointerV1ErrorInvalidAxisSource, "axis source %d", source)
	}
	v.source = s
	return nil
}

func (v *virtualPointer) AxisStop(obj *svp.ZwlrVirtualPointerV1, ms uint32, axis sw.WlPointerAxis) error {
	o, err := orientationOf(obj, axis)
	if err != nil {
		return err
	}
	v.stops = append(v.stops, backend.PointerAxis{Time: millis(ms), Orientation: o, Source: backend.Finger})
	return nil
}

func (v *virtualPointer) Frame(*svp.ZwlrVirtualPointerV1) error {
	var errs []error
	for _, a := range v.axes {
		a.Source = v.source
		errs = append(errs, v.input(a))
	}
	for _, a := range v.stops {
		errs = append(errs, v.input(a))
	}
	v.axes, v.stops, v.source = v.axes[:0], v.stops[:0], backend.Wheel
	return errors.Join(append(errs, v.input(backend.PointerFrame{}))...)
}

func (*virtualPointer) Destroy(*svp.ZwlrVirtualPointerV1) error {
	return nil
}
