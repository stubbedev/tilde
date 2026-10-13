// Package headless is a backend with virtual outputs rendered in software and
// synthetic input devices, for tests and the compositor-in-the-loop harness.
package headless

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/soft"
)

type Backend struct {
	loop     *loop.Loop
	renderer *soft.Renderer
	handler  backend.Handler
	outputs  []*Output
	next     int
	keyboard *Keyboard
	pointer  *Pointer
	touch    *Touch
	closed   bool
}

func New(l *loop.Loop) *Backend {
	b := &Backend{loop: l, renderer: soft.New()}
	b.keyboard = &Keyboard{device{b, "headless-keyboard", backend.Keyboard}}
	b.pointer = &Pointer{device{b, "headless-pointer", backend.Pointer}}
	b.touch = &Touch{device{b, "headless-touch", backend.Touch}}
	return b
}

func (b *Backend) Start(h backend.Handler) error {
	if b.handler != nil {
		return errors.New("headless: already started")
	}
	b.handler = h
	for _, d := range b.devices() {
		if err := h.DeviceAdded(d); err != nil {
			return err
		}
	}
	for _, o := range b.outputs {
		if err := h.OutputAdded(o); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) devices() []backend.Device {
	return []backend.Device{b.keyboard, b.pointer, b.touch}
}

func (b *Backend) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	var errs []error
	for len(b.outputs) > 0 {
		errs = append(errs, b.RemoveOutput(b.outputs[0]))
	}
	if b.handler != nil {
		for _, d := range b.devices() {
			errs = append(errs, b.handler.DeviceRemoved(d))
		}
	}
	return errors.Join(errs...)
}

func (b *Backend) Outputs() []*Output {
	return slices.Clone(b.outputs)
}

func (b *Backend) AddOutput(modes ...backend.Mode) (*Output, error) {
	if len(modes) == 0 {
		return nil, errors.New("headless: an output needs at least one mode")
	}
	current := modes[0]
	for _, m := range modes {
		if err := m.Valid(); err != nil {
			return nil, err
		}
		if m.Preferred {
			current = m
		}
	}
	b.next++
	o := &Output{
		backend: b,
		name:    fmt.Sprintf("HEADLESS-%d", b.next),
		modes:   slices.Clone(modes),
		state:   backend.OutputState{Enabled: true, Mode: current},
		buffer:  soft.NewBuffer(current.Size.X, current.Size.Y),
	}
	var err error
	if o.timer, err = b.loop.AddTimer(o.vblank); err != nil {
		return nil, err
	}
	b.outputs = append(b.outputs, o)
	if b.handler != nil {
		if err := b.handler.OutputAdded(o); err != nil {
			return nil, err
		}
	}
	return o, nil
}

func (b *Backend) RemoveOutput(o *Output) error {
	i := slices.Index(b.outputs, o)
	if i < 0 {
		return fmt.Errorf("headless: %s is not an output of this backend", o.name)
	}
	b.outputs = slices.Delete(b.outputs, i, i+1)
	o.removed = true
	var errs []error
	if b.handler != nil {
		errs = append(errs, b.handler.OutputRemoved(o))
	}
	return errors.Join(append(errs, o.timer.Remove())...)
}

type Output struct {
	backend        *Backend
	name           string
	modes          []backend.Mode
	state          backend.OutputState
	buffer         *soft.Buffer
	timer          *loop.Timer
	armed          bool
	frameRequested bool
	presentPending bool
	inFrame        bool
	lastVblank     time.Duration
	seq            uint64
	removed        bool
}

func (o *Output) Name() string {
	return o.name
}

func (o *Output) Description() string {
	return "Headless output " + o.name
}

func (o *Output) PhysicalSize() image.Point {
	return image.Point{}
}

func (o *Output) Modes() []backend.Mode {
	return slices.Clone(o.modes)
}

func (o *Output) State() backend.OutputState {
	return o.state
}

func (o *Output) Commit(s backend.OutputState) error {
	if o.removed {
		return fmt.Errorf("headless: commit to removed output %s", o.name)
	}
	if s.Enabled {
		if err := s.Mode.Valid(); err != nil {
			return fmt.Errorf("headless: %s: %w", o.name, err)
		}
		if s.Mode.Size != o.buffer.Size() {
			o.buffer = soft.NewBuffer(s.Mode.Size.X, s.Mode.Size.Y)
		}
	} else {
		o.frameRequested, o.presentPending = false, false
	}
	o.state = s
	return nil
}

func (o *Output) ScheduleFrame() error {
	if !o.state.Enabled || o.removed {
		return nil
	}
	o.frameRequested = true
	return o.arm()
}

func (o *Output) BeginFrame() (render.Renderer, render.Target, error) {
	switch {
	case o.removed:
		return nil, nil, fmt.Errorf("headless: frame on removed output %s", o.name)
	case !o.state.Enabled:
		return nil, nil, fmt.Errorf("headless: frame on disabled output %s", o.name)
	case o.inFrame:
		return nil, nil, fmt.Errorf("headless: %s already has a frame in progress", o.name)
	}
	o.inFrame = true
	return o.backend.renderer, o.buffer, nil
}

func (o *Output) Present(render.Region) error {
	if !o.inFrame {
		return fmt.Errorf("headless: present on %s without BeginFrame", o.name)
	}
	o.inFrame = false
	o.presentPending = true
	return o.arm()
}

func (o *Output) Snapshot() *soft.Buffer {
	return &soft.Buffer{Pix: slices.Clone(o.buffer.Pix), Stride: o.buffer.Stride, W: o.buffer.W, H: o.buffer.H}
}

func (o *Output) arm() error {
	if o.armed {
		return nil
	}
	interval := o.state.Mode.Interval()
	delay := time.Duration(0)
	if since := backend.Now() - o.lastVblank; since < interval {
		delay = interval - since
	}
	if err := o.timer.Arm(delay); err != nil {
		return fmt.Errorf("headless: arm frame timer for %s: %w", o.name, err)
	}
	o.armed = true
	return nil
}

func (o *Output) vblank() error {
	o.armed = false
	now := backend.Now()
	o.lastVblank = now
	o.seq++
	h := o.backend.handler
	if h == nil {
		return nil
	}
	if o.presentPending {
		o.presentPending = false
		if err := h.Presented(o, backend.Presentation{Time: now, Refresh: o.state.Mode.Interval(), Seq: o.seq}); err != nil {
			return err
		}
	}
	if o.frameRequested {
		o.frameRequested = false
		return h.Frame(o)
	}
	return nil
}

type device struct {
	backend *Backend
	name    string
	caps    backend.Capability
}

func (d *device) Name() string {
	return d.name
}

func (d *device) Capabilities() backend.Capability {
	return d.caps
}

func (b *Backend) emit(d backend.Device, e backend.Event) error {
	if b.handler == nil {
		return errors.New("headless: input injected before Start")
	}
	return b.handler.Input(d, e)
}

func now() time.Duration {
	return backend.Now()
}

type Keyboard struct{ device }

func (b *Backend) Keyboard() *Keyboard {
	return b.keyboard
}

func (k *Keyboard) Key(code uint32, pressed bool) error {
	return k.backend.emit(k, backend.Key{Time: now(), Code: code, Pressed: pressed})
}

type Pointer struct{ device }

func (b *Backend) Pointer() *Pointer {
	return b.pointer
}

func (p *Pointer) Move(dx, dy float64) error {
	return p.backend.emit(p, backend.PointerMotion{Time: now(), DX: dx, DY: dy, UnaccelDX: dx, UnaccelDY: dy})
}

func (p *Pointer) MoveTo(o *Output, x, y float64) error {
	return p.backend.emit(p, backend.PointerMotionAbsolute{Time: now(), Output: o, X: x, Y: y})
}

func (p *Pointer) Button(button uint32, pressed bool) error {
	return p.backend.emit(p, backend.PointerButton{Time: now(), Button: button, Pressed: pressed})
}

func (p *Pointer) Axis(orientation backend.Orientation, source backend.AxisSource, value float64, value120 int32) error {
	return p.backend.emit(p, backend.PointerAxis{Time: now(), Orientation: orientation, Source: source, Value: value, Value120: value120})
}

func (p *Pointer) Frame() error {
	return p.backend.emit(p, backend.PointerFrame{Time: now()})
}

type Touch struct{ device }

func (b *Backend) Touch() *Touch {
	return b.touch
}

func (t *Touch) Down(id int32, o *Output, x, y float64) error {
	return t.backend.emit(t, backend.TouchDown{Time: now(), ID: id, Output: o, X: x, Y: y})
}

func (t *Touch) Motion(id int32, x, y float64) error {
	return t.backend.emit(t, backend.TouchMotion{Time: now(), ID: id, X: x, Y: y})
}

func (t *Touch) Up(id int32) error {
	return t.backend.emit(t, backend.TouchUp{Time: now(), ID: id})
}

func (t *Touch) Cancel(id int32) error {
	return t.backend.emit(t, backend.TouchCancel{Time: now(), ID: id})
}

func (t *Touch) Frame() error {
	return t.backend.emit(t, backend.TouchFrame{Time: now()})
}
