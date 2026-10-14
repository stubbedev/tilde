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
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/gles"
	"github.com/stubbedev/tilde/internal/render/soft"
)

type Backend struct {
	loop       *loop.Loop
	renderer   render.Renderer
	gpu        *gpuRenderer
	handler    backend.Handler
	outputs    []*Output
	next       int
	keyboard   *Keyboard
	pointer    *Pointer
	touch      *Touch
	switches   *Switches
	cursorSize image.Point
	closed     bool
}

func New(l *loop.Loop) *Backend {
	b := &Backend{loop: l, renderer: soft.New()}
	b.keyboard = &Keyboard{device{b, "headless-keyboard", backend.Keyboard}}
	b.pointer = &Pointer{device{b, "headless-pointer", backend.Pointer}}
	b.touch = &Touch{device{b, "headless-touch", backend.Touch}}
	b.switches = &Switches{device{b, "headless-switches", 0}}
	return b
}

type gpuRenderer struct {
	lib     *gpu.Library
	display *gpu.Display
	gles    *gles.Renderer
}

func (g *gpuRenderer) close() error {
	return errors.Join(g.gles.Close(), g.display.Close(), g.lib.Close())
}

func NewGLES(l *loop.Loop, paths gpu.Paths) (*Backend, error) {
	lib, err := gpu.Load(paths)
	if err != nil {
		return nil, err
	}
	d, err := lib.Surfaceless()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("headless: %w", err), lib.Close())
	}
	r, err := gles.New(d)
	if err != nil {
		return nil, errors.Join(err, d.Close(), lib.Close())
	}
	b := New(l)
	b.renderer, b.gpu = r, &gpuRenderer{lib: lib, display: d, gles: r}
	return b, nil
}

func (o *Output) resize(size image.Point) error {
	if err := o.freeTarget(); err != nil {
		return err
	}
	o.buffer = soft.NewBuffer(size.X, size.Y)
	o.drawn = false
	if o.backend.gpu == nil {
		o.target = o.buffer
		return nil
	}
	fb, err := o.backend.gpu.gles.NewFramebuffer(size)
	if err != nil {
		return fmt.Errorf("headless: %s: %w", o.name, err)
	}
	o.target = fb
	return nil
}

func (o *Output) freeTarget() error {
	fb, ok := o.target.(*gles.Framebuffer)
	o.target = nil
	if !ok {
		return nil
	}
	return fb.Destroy()
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

func (b *Backend) Renderer() render.Renderer {
	return b.renderer
}

func (b *Backend) devices() []backend.Device {
	return []backend.Device{b.keyboard, b.pointer, b.touch, b.switches}
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
	if b.gpu != nil {
		errs = append(errs, b.gpu.close())
	}
	return errors.Join(errs...)
}

func (b *Backend) Outputs() []*Output {
	return slices.Clone(b.outputs)
}

func (b *Backend) AddOutput(modes ...backend.Mode) (*Output, error) {
	return b.AddNamedOutput(fmt.Sprintf("HEADLESS-%d", b.next+1), modes...)
}

func (b *Backend) AddNamedOutput(name string, modes ...backend.Mode) (*Output, error) {
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
		name:    name,
		modes:   slices.Clone(modes),
		state:   backend.OutputState{Enabled: true, Mode: current},
	}
	if err := o.resize(current.Size); err != nil {
		return nil, err
	}
	if b.cursorSize != (image.Point{}) {
		o.cursor = &Cursor{o: o}
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
	errs = append(errs, o.timer.Remove(), o.freeTarget())
	if o.cursor != nil {
		errs = append(errs, o.cursor.destroy())
	}
	return errors.Join(errs...)
}

type Output struct {
	backend        *Backend
	name           string
	modes          []backend.Mode
	state          backend.OutputState
	buffer         *soft.Buffer
	target         render.Target
	drawn          bool
	timer          *loop.Timer
	armed          bool
	frameRequested bool
	presentPending bool
	inFrame        bool
	lastVblank     time.Duration
	seq            uint64
	presents       uint64
	frames         uint64
	damage         render.Region
	removed        bool
	cursor         *Cursor
	scanout        bool
	scanouts       uint64
	shownRelease   func() error
	queuedRelease  func() error
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
			if err := o.resize(s.Mode.Size); err != nil {
				return err
			}
		}
	}
	if !s.Enabled || s.Off {
		o.frameRequested, o.presentPending = false, false
	}
	o.state = s
	return nil
}

func (o *Output) ScheduleFrame() error {
	if !o.state.Enabled || o.state.Off || o.removed {
		return nil
	}
	o.frameRequested = true
	return o.arm()
}

func (o *Output) BeginFrame() (backend.Frame, error) {
	switch {
	case o.removed:
		return backend.Frame{}, fmt.Errorf("headless: frame on removed output %s", o.name)
	case !o.state.Enabled:
		return backend.Frame{}, fmt.Errorf("headless: frame on disabled output %s", o.name)
	case o.inFrame:
		return backend.Frame{}, fmt.Errorf("headless: %s already has a frame in progress", o.name)
	}
	o.inFrame = true
	age := 0
	if o.drawn {
		age = 1
	}
	return backend.Frame{Renderer: o.backend.renderer, Target: o.target, Age: age}, nil
}

func (o *Output) Present(damage render.Region) error {
	if !o.inFrame {
		return fmt.Errorf("headless: present on %s without BeginFrame", o.name)
	}
	o.inFrame = false
	o.drawn = true
	o.damage = append(o.damage[:0], damage...)
	if fb, ok := o.target.(*gles.Framebuffer); ok {
		if err := fb.Read(o.buffer.Pix, o.buffer.Stride); err != nil {
			return fmt.Errorf("headless: read back %s: %w", o.name, err)
		}
	}
	o.presentPending = true
	o.presents++
	return o.arm()
}

func (o *Output) AllowScanout(on bool) {
	o.scanout = on
}

func (o *Output) Scanouts() uint64 {
	return o.scanouts
}

func (o *Output) ShowsClientBuffer() bool {
	return o.shownRelease != nil || o.queuedRelease != nil
}

func (o *Output) Scanout(d gpu.DMABUF, release func() error) (bool, error) {
	if !o.scanout || o.removed || !o.state.Enabled || o.state.Off || o.inFrame || o.presentPending || d.Size != o.state.Mode.Size {
		return false, nil
	}
	o.queuedRelease, o.presentPending, o.drawn = release, true, false
	o.presents++
	o.scanouts++
	return true, o.arm()
}

func (o *Output) Presents() uint64 {
	return o.presents
}

func (o *Output) Frames() uint64 {
	return o.frames
}

func (o *Output) LastDamage() render.Region {
	return slices.Clone(o.damage)
}

func (o *Output) Primary() *soft.Buffer {
	return &soft.Buffer{Pix: slices.Clone(o.buffer.Pix), Stride: o.buffer.Stride, W: o.buffer.W, H: o.buffer.H}
}

func (o *Output) Snapshot() *soft.Buffer {
	snap := o.Primary()
	if o.cursor != nil {
		o.cursor.composite(snap)
	}
	return snap
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
		left := o.shownRelease
		o.shownRelease, o.queuedRelease = o.queuedRelease, nil
		if left != nil {
			if err := left(); err != nil {
				return err
			}
		}
		if err := h.Presented(o, backend.Presentation{Time: now, Refresh: o.state.Mode.Interval(), Seq: o.seq}); err != nil {
			return err
		}
	}
	if o.frameRequested {
		o.frameRequested = false
		o.frames++
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

type Switches struct{ device }

func (b *Backend) Switches() *Switches {
	return b.switches
}

func (s *Switches) Toggle(kind backend.SwitchKind, on bool) error {
	return s.backend.emit(s, backend.Switch{Time: now(), Kind: kind, On: on})
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
