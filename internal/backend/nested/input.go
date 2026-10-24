package nested

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/wire"
)

type device struct {
	name string
	caps backend.Capability
}

func (d *device) Name() string {
	return d.name
}

func (d *device) Capabilities() backend.Capability {
	return d.caps
}

type seatEvents struct{ b *Backend }

func (e seatEvents) Name(*cw.WlSeat, string) error { return nil }

func (e seatEvents) Capabilities(seat *cw.WlSeat, caps cw.WlSeatCapability) error {
	b := e.b
	var errs []error
	switch has := caps.Has(cw.WlSeatCapabilityKeyboard); {
	case has && b.keyboard == nil:
		k := &Keyboard{device: device{"nested-keyboard", backend.Keyboard}, backend: b, pressed: map[uint32]bool{}}
		var err error
		if k.wl, err = seat.GetKeyboard(keyboardEvents{k}); err != nil {
			return err
		}
		b.keyboard = k
		errs = append(errs, b.added(k))
	case !has && b.keyboard != nil:
		errs = append(errs, b.removed(b.keyboard, b.keyboard.wl.Release()))
		b.keyboard = nil
	}
	switch has := caps.Has(cw.WlSeatCapabilityPointer); {
	case has && b.pointer == nil:
		p := &Pointer{device: device{"nested-pointer", backend.Pointer}, backend: b, value120: map[cw.WlPointerAxis]int32{}}
		var err error
		if p.wl, err = seat.GetPointer(pointerEvents{p}); err != nil {
			return err
		}
		b.pointer = p
		errs = append(errs, b.added(p))
	case !has && b.pointer != nil:
		errs = append(errs, b.removed(b.pointer, b.pointer.wl.Release()))
		b.pointer = nil
	}
	switch has := caps.Has(cw.WlSeatCapabilityTouch); {
	case has && b.touch == nil:
		t := &Touch{device: device{"nested-touch", backend.Touch}, backend: b, outputs: map[int32]*Output{}}
		var err error
		if t.wl, err = seat.GetTouch(touchEvents{t}); err != nil {
			return err
		}
		b.touch = t
		errs = append(errs, b.added(t))
	case !has && b.touch != nil:
		errs = append(errs, b.removed(b.touch, b.touch.wl.Release()))
		b.touch = nil
	}
	return errors.Join(errs...)
}

func (b *Backend) added(d backend.Device) error {
	if b.handler == nil {
		return nil
	}
	return b.handler.DeviceAdded(d)
}

func (b *Backend) removed(d backend.Device, release error) error {
	if b.handler == nil {
		return release
	}
	return errors.Join(release, b.handler.DeviceRemoved(d))
}

func (b *Backend) emit(d backend.Device, e backend.Event) error {
	if b.handler == nil {
		return nil
	}
	return b.handler.Input(d, e)
}

type Keyboard struct {
	device
	backend *Backend
	wl      *cw.WlKeyboard
	keymap  []byte
	pressed map[uint32]bool
}

func (k *Keyboard) Keymap() []byte {
	return slices.Clone(k.keymap)
}

func (k keyboardEvents) Key(_ *cw.WlKeyboard, _, _, key uint32, state cw.WlKeyboardKeyState) error {
	switch state {
	case cw.WlKeyboardKeyStatePressed:
		k.pressed[key] = true
	case cw.WlKeyboardKeyStateReleased:
		delete(k.pressed, key)
	default:
		return nil
	}
	return k.backend.emit(k.Keyboard, backend.Key{Time: backend.Now(), Code: key, Pressed: state == cw.WlKeyboardKeyStatePressed})
}

func (k keyboardEvents) Enter(*cw.WlKeyboard, uint32, *cw.WlSurface, []byte) error {
	return nil
}

func (k keyboardEvents) Leave(*cw.WlKeyboard, uint32, *cw.WlSurface) error {
	var errs []error
	for _, key := range slices.Sorted(maps.Keys(k.pressed)) {
		delete(k.pressed, key)
		errs = append(errs, k.backend.emit(k.Keyboard, backend.Key{Time: backend.Now(), Code: key}))
	}
	return errors.Join(errs...)
}

func (k keyboardEvents) Modifiers(*cw.WlKeyboard, uint32, uint32, uint32, uint32, uint32) error {
	return nil
}

func (k keyboardEvents) RepeatInfo(*cw.WlKeyboard, int32, int32) error {
	return nil
}

func readKeymap(fd int, size uint32) ([]byte, error) {
	defer func() { _ = unix.Close(fd) }()
	if size == 0 {
		return nil, nil
	}
	m, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("nested: map the host keymap: %w", err)
	}
	defer func() { _ = unix.Munmap(m) }()
	keymap := slices.Clone(m)
	if n := len(keymap); n > 0 && keymap[n-1] == 0 {
		keymap = keymap[:n-1]
	}
	return keymap, nil
}

type keyboardEvents struct{ *Keyboard }

func (k keyboardEvents) Keymap(_ *cw.WlKeyboard, format cw.WlKeyboardKeymapFormat, fd int, size uint32) error {
	if format != cw.WlKeyboardKeymapFormatXkbV1 {
		_ = unix.Close(fd)
		k.keymap = nil
		return nil
	}
	keymap, err := readKeymap(fd, size)
	if err != nil {
		return err
	}
	k.keymap = keymap
	return nil
}

type Pointer struct {
	device
	backend  *Backend
	wl       *cw.WlPointer
	output   *Output
	source   backend.AxisSource
	value120 map[cw.WlPointerAxis]int32
}

func (p pointerEvents) motion(x, y wire.Fixed) error {
	o := p.output
	if o == nil || o.size.X == 0 || o.size.Y == 0 {
		return nil
	}
	return p.backend.emit(p.Pointer, backend.PointerMotionAbsolute{Time: backend.Now(), Output: o, X: x.Float() / float64(o.size.X), Y: y.Float() / float64(o.size.Y)})
}

func (p pointerEvents) Enter(wl *cw.WlPointer, serial uint32, surface *cw.WlSurface, x, y wire.Fixed) error {
	p.output = p.backend.outputFor(surface)
	if err := wl.SetCursor(serial, nil, 0, 0); err != nil {
		return fmt.Errorf("nested: hide the host cursor: %w", err)
	}
	return p.motion(x, y)
}

func (p pointerEvents) Leave(*cw.WlPointer, uint32, *cw.WlSurface) error {
	p.output = nil
	return nil
}

func (p pointerEvents) Motion(_ *cw.WlPointer, _ uint32, x, y wire.Fixed) error {
	return p.motion(x, y)
}

func (p pointerEvents) Button(_ *cw.WlPointer, _, _, button uint32, state cw.WlPointerButtonState) error {
	return p.backend.emit(p.Pointer, backend.PointerButton{Time: backend.Now(), Button: button, Pressed: state == cw.WlPointerButtonStatePressed})
}

func (p pointerEvents) Axis(_ *cw.WlPointer, _ uint32, axis cw.WlPointerAxis, value wire.Fixed) error {
	orientation := backend.Vertical
	if axis == cw.WlPointerAxisHorizontalScroll {
		orientation = backend.Horizontal
	}
	v120 := p.value120[axis]
	delete(p.value120, axis)
	return p.backend.emit(p.Pointer, backend.PointerAxis{Time: backend.Now(), Orientation: orientation, Source: p.source, Value: value.Float(), Value120: v120})
}

func (p pointerEvents) Frame(*cw.WlPointer) error {
	p.source = backend.Wheel
	return p.backend.emit(p.Pointer, backend.PointerFrame{Time: backend.Now()})
}

func (p pointerEvents) AxisSource(_ *cw.WlPointer, source cw.WlPointerAxisSource) error {
	switch source {
	case cw.WlPointerAxisSourceFinger:
		p.source = backend.Finger
	case cw.WlPointerAxisSourceContinuous:
		p.source = backend.Continuous
	case cw.WlPointerAxisSourceWheelTilt:
		p.source = backend.WheelTilt
	default:
		p.source = backend.Wheel
	}
	return nil
}

func (p pointerEvents) AxisStop(*cw.WlPointer, uint32, cw.WlPointerAxis) error { return nil }

func (p pointerEvents) AxisDiscrete(*cw.WlPointer, cw.WlPointerAxis, int32) error { return nil }

func (p pointerEvents) AxisValue120(_ *cw.WlPointer, axis cw.WlPointerAxis, value120 int32) error {
	p.value120[axis] += value120
	return nil
}

func (p pointerEvents) AxisRelativeDirection(*cw.WlPointer, cw.WlPointerAxis, cw.WlPointerAxisRelativeDirection) error {
	return nil
}

func (p pointerEvents) Warp(*cw.WlPointer, wire.Fixed, wire.Fixed) error { return nil }

type pointerEvents struct{ *Pointer }

type Touch struct {
	device
	backend *Backend
	wl      *cw.WlTouch
	outputs map[int32]*Output
}

func (t touchEvents) Down(_ *cw.WlTouch, _, _ uint32, surface *cw.WlSurface, id int32, x, y wire.Fixed) error {
	o := t.backend.outputFor(surface)
	if o == nil || o.size.X == 0 || o.size.Y == 0 {
		return nil
	}
	t.outputs[id] = o
	return t.backend.emit(t.Touch, backend.TouchDown{Time: backend.Now(), ID: id, Output: o, X: x.Float() / float64(o.size.X), Y: y.Float() / float64(o.size.Y)})
}

func (t touchEvents) Up(_ *cw.WlTouch, _, _ uint32, id int32) error {
	if _, ok := t.outputs[id]; !ok {
		return nil
	}
	delete(t.outputs, id)
	return t.backend.emit(t.Touch, backend.TouchUp{Time: backend.Now(), ID: id})
}

func (t touchEvents) Motion(_ *cw.WlTouch, _ uint32, id int32, x, y wire.Fixed) error {
	o, ok := t.outputs[id]
	if !ok {
		return nil
	}
	return t.backend.emit(t.Touch, backend.TouchMotion{Time: backend.Now(), ID: id, X: x.Float() / float64(o.size.X), Y: y.Float() / float64(o.size.Y)})
}

func (t touchEvents) Frame(*cw.WlTouch) error {
	return t.backend.emit(t.Touch, backend.TouchFrame{Time: backend.Now()})
}

func (t touchEvents) Cancel(*cw.WlTouch) error {
	var errs []error
	for _, id := range slices.Sorted(maps.Keys(t.outputs)) {
		delete(t.outputs, id)
		errs = append(errs, t.backend.emit(t.Touch, backend.TouchCancel{Time: backend.Now(), ID: id}))
	}
	return errors.Join(errs...)
}

func (t touchEvents) Shape(*cw.WlTouch, int32, wire.Fixed, wire.Fixed) error { return nil }

func (t touchEvents) Orientation(*cw.WlTouch, int32, wire.Fixed) error { return nil }

type touchEvents struct{ *Touch }
