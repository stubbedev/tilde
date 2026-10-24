// Package nested runs lys-wm as a client of a host Wayland compositor: each
// output is a host window and the host seat is lys-wm's input.
package nested

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/edid"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	xdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wlclient"
)

var ErrClosed = errors.New("nested: the last host window was closed")

var defaultSize = image.Pt(1280, 720)

const nominalRefreshMHz = 60000

type Backend struct {
	loop       *loop.Loop
	host       *wlclient.Display
	source     *loop.Source
	writable   bool
	chain      swapchain
	handler    backend.Handler
	compositor *cw.WlCompositor
	shm        *cw.WlShm
	wmBase     *xdg.XdgWmBase
	seat       *cw.WlSeat
	outputs    []*Output
	next       int
	keyboard   *Keyboard
	pointer    *Pointer
	touch      *Touch
	closed     bool
}

type global struct {
	name    uint32
	version uint32
}

type registry map[string]global

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, version uint32) error {
	r[iface] = global{name, version}
	return nil
}

func (r registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type Renderer uint8

const (
	Soft Renderer = iota + 1
	GLES
)

type Options struct {
	Renderer Renderer
	GPU      gpu.Paths
}

func New(l *loop.Loop, socket string, opts Options) (b *Backend, err error) {
	host, err := wlclient.Connect(socket)
	if err != nil {
		return nil, fmt.Errorf("nested: %w", err)
	}
	b = &Backend{loop: l, host: host}
	defer func() {
		if err != nil {
			if b.chain != nil {
				err = errors.Join(err, b.chain.close())
			}
			err = errors.Join(err, host.Close())
		}
	}()
	globals := registry{}
	reg, err := host.WlDisplay().GetRegistry(globals)
	if err != nil {
		return nil, err
	}
	if err := host.Roundtrip(); err != nil {
		return nil, fmt.Errorf("nested: list host globals: %w", err)
	}
	bind := func(iface *wire.Interface, minVersion uint32, create func(version uint32) (wire.Resource, error)) error {
		g, ok := globals[iface.Name]
		if !ok || g.version < minVersion {
			return fmt.Errorf("nested: host lacks %s version %d", iface.Name, minVersion)
		}
		r, err := create(min(g.version, iface.Version))
		if err != nil {
			return err
		}
		return reg.Bind(g.name, r)
	}
	if err := bind(cw.WlCompositorInterface, 4, func(v uint32) (wire.Resource, error) {
		b.compositor, err = cw.NewWlCompositor(host, v)
		return b.compositor, err
	}); err != nil {
		return nil, err
	}
	if err := bind(cw.WlShmInterface, 1, func(v uint32) (wire.Resource, error) {
		b.shm, err = cw.NewWlShm(host, v, shmEvents{})
		return b.shm, err
	}); err != nil {
		return nil, err
	}
	if err := bind(xdg.XdgWmBaseInterface, 1, func(v uint32) (wire.Resource, error) {
		b.wmBase, err = xdg.NewXdgWmBase(host, v, wmBaseEvents{})
		return b.wmBase, err
	}); err != nil {
		return nil, err
	}
	if _, ok := globals[cw.WlSeatInterface.Name]; ok {
		if err := bind(cw.WlSeatInterface, 1, func(v uint32) (wire.Resource, error) {
			b.seat, err = cw.NewWlSeat(host, v, seatEvents{b})
			return b.seat, err
		}); err != nil {
			return nil, err
		}
	}
	if err := host.Roundtrip(); err != nil {
		return nil, fmt.Errorf("nested: bind host globals: %w", err)
	}
	switch opts.Renderer {
	case Soft:
		b.chain = &shmChain{shm: b.shm, soft: soft.New()}
	case GLES:
		if b.chain, err = newDMABUFChain(host, globals, reg, opts.GPU); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("nested: unknown renderer %d", opts.Renderer)
	}
	if b.source, err = l.AddFD(host.Conn().FD(), loop.Readable, b.readHost); err != nil {
		return nil, err
	}
	l.BeforeWait(b.flush)
	return b, nil
}

func (b *Backend) readHost(ev loop.Events) error {
	if ev&loop.Writable != 0 {
		if err := b.flush(); err != nil {
			return err
		}
	}
	if ev&loop.Readable != 0 {
		if err := b.host.ReadEvents(); err != nil {
			if errors.Is(err, ErrClosed) {
				return err
			}
			return fmt.Errorf("nested: host connection: %w", err)
		}
	}
	if ev&(loop.Hangup|loop.Error) != 0 {
		return errors.New("nested: host connection hung up")
	}
	return nil
}

func (b *Backend) flush() error {
	if b.closed {
		return nil
	}
	if err := b.host.Conn().Err(); err != nil {
		return fmt.Errorf("nested: write to host: %w", err)
	}
	err := b.host.Conn().Flush()
	switch {
	case errors.Is(err, wire.ErrWouldBlock):
		if !b.writable {
			b.writable = true
			return b.source.Modify(loop.Readable | loop.Writable)
		}
		return nil
	case err != nil:
		return fmt.Errorf("nested: flush to host: %w", err)
	}
	if b.writable {
		b.writable = false
		return b.source.Modify(loop.Readable)
	}
	return nil
}

func (b *Backend) Start(h backend.Handler) error {
	if b.handler != nil {
		return errors.New("nested: already started")
	}
	b.handler = h
	for _, d := range b.devices() {
		if err := h.DeviceAdded(d); err != nil {
			return err
		}
	}
	_, err := b.AddOutput()
	return err
}

func (b *Backend) Renderer() render.Renderer {
	return b.chain.renderer()
}

func (b *Backend) devices() []backend.Device {
	var out []backend.Device
	if b.keyboard != nil {
		out = append(out, b.keyboard)
	}
	if b.pointer != nil {
		out = append(out, b.pointer)
	}
	if b.touch != nil {
		out = append(out, b.touch)
	}
	return out
}

func (b *Backend) Close() error {
	if b.closed {
		return nil
	}
	var errs []error
	for len(b.outputs) > 0 {
		errs = append(errs, b.outputs[0].destroy())
	}
	if b.handler != nil {
		for _, d := range b.devices() {
			errs = append(errs, b.handler.DeviceRemoved(d))
		}
	}
	b.closed = true
	errs = append(errs, b.chain.close(), b.source.Remove(), b.host.Close())
	return errors.Join(errs...)
}

func (b *Backend) Outputs() []*Output {
	return slices.Clone(b.outputs)
}

func (b *Backend) AddOutput() (*Output, error) {
	b.next++
	o := &Output{backend: b, name: fmt.Sprintf("NESTED-%d", b.next)}
	o.clock = backend.NewFrameClock(b.loop.Idle, o.frameReady, o.runFrame, nil)
	var err error
	if o.surface, err = b.compositor.CreateSurface(surfaceEvents{}); err != nil {
		return nil, err
	}
	if o.xdgSurface, err = b.wmBase.GetXdgSurface(xdgSurfaceEvents{o}, o.surface); err != nil {
		return nil, err
	}
	if o.toplevel, err = o.xdgSurface.GetToplevel(toplevelEvents{o}); err != nil {
		return nil, err
	}
	if err := errors.Join(o.toplevel.SetTitle("lys-wm "+o.name), o.toplevel.SetAppID("lys-wm"), o.surface.Commit()); err != nil {
		return nil, err
	}
	b.outputs = append(b.outputs, o)
	return o, nil
}

func (b *Backend) outputFor(s *cw.WlSurface) *Output {
	for _, o := range b.outputs {
		if o.surface == s {
			return o
		}
	}
	return nil
}

type buffer struct {
	wl        *cw.WlBuffer
	target    render.Target
	busy      bool
	presented uint64
}

type swapchain interface {
	renderer() render.Renderer
	allocate(o *Output, size image.Point) ([]*buffer, func() error, error)
	finish() error
	close() error
}

type Output struct {
	backend         *Backend
	name            string
	surface         *cw.WlSurface
	xdgSurface      *xdg.XdgSurface
	toplevel        *xdg.XdgToplevel
	pending         image.Point
	size            image.Point
	configured      bool
	buffers         []*buffer
	free            func() error
	drawing         *buffer
	clock           *backend.FrameClock
	callbackPending bool
	presentPending  bool
	seq             uint64
	presented       uint64
}

func (o *Output) Name() string {
	return o.name
}

func (o *Output) Description() string {
	return "Nested output " + o.name
}

func (*Output) Identity() edid.Info {
	return edid.Info{}
}

func (o *Output) PhysicalSize() image.Point {
	return image.Point{}
}

func (o *Output) mode() backend.Mode {
	return backend.Mode{Size: o.size, RefreshMHz: nominalRefreshMHz, Preferred: true}
}

func (o *Output) Modes() []backend.Mode {
	return []backend.Mode{o.mode()}
}

func (o *Output) State() backend.OutputState {
	if !o.configured {
		return backend.OutputState{Power: backend.Disabled, Mode: o.mode()}
	}
	return backend.OutputState{Power: backend.PoweredOn, Mode: o.mode()}
}

func (o *Output) Commit(s backend.OutputState) error {
	if s != o.State() {
		return fmt.Errorf("nested: %s follows its host window; it cannot be set to %+v", o.name, s)
	}
	return nil
}

func (o *Output) configure(serial uint32) error {
	if err := o.xdgSurface.AckConfigure(serial); err != nil {
		return err
	}
	size := o.pending
	if size.X <= 0 || size.Y <= 0 {
		size = o.size
	}
	if size.X <= 0 || size.Y <= 0 {
		size = defaultSize
	}
	if o.configured && size == o.size {
		return nil
	}
	if err := o.resize(size); err != nil {
		return err
	}
	if !o.configured {
		o.configured = true
		return o.backend.handler.OutputAdded(o)
	}
	return o.backend.handler.OutputChanged(o)
}

func (o *Output) resize(size image.Point) error {
	if err := o.releaseBuffers(); err != nil {
		return err
	}
	buffers, free, err := o.backend.chain.allocate(o, size)
	if err != nil {
		return err
	}
	o.buffers, o.free, o.size = buffers, free, size
	return nil
}

func (o *Output) releaseBuffers() error {
	var errs []error
	for _, buf := range o.buffers {
		errs = append(errs, buf.wl.Destroy())
	}
	o.buffers, o.drawing = nil, nil
	if o.free != nil {
		errs = append(errs, o.free())
		o.free = nil
	}
	return errors.Join(errs...)
}

func (o *Output) freeBuffer() *buffer {
	for _, buf := range o.buffers {
		if !buf.busy {
			return buf
		}
	}
	return nil
}

func (o *Output) ScheduleFrame() error {
	if !o.configured {
		return nil
	}
	return o.clock.Request()
}

func (o *Output) frameReady() bool {
	return o.configured && !o.callbackPending && o.drawing == nil && o.freeBuffer() != nil
}

func (o *Output) runFrame() error {
	if err := o.backend.handler.Frame(o); err != nil || o.callbackPending {
		return err
	}
	return o.commitForCallback()
}

func (o *Output) commitForCallback() error {
	if _, err := o.surface.Frame(callbackEvents{o}); err != nil {
		return err
	}
	if err := o.surface.Commit(); err != nil {
		return err
	}
	o.callbackPending = true
	return nil
}

func (o *Output) BeginFrame() (backend.Frame, error) {
	switch {
	case !o.configured:
		return backend.Frame{}, fmt.Errorf("nested: frame on %s before the host configured it", o.name)
	case o.drawing != nil:
		return backend.Frame{}, fmt.Errorf("nested: %s already has a frame in progress", o.name)
	}
	buf := o.freeBuffer()
	if buf == nil {
		return backend.Frame{}, fmt.Errorf("nested: the host holds every buffer of %s", o.name)
	}
	o.drawing = buf
	age := 0
	if buf.presented != 0 {
		age = int(o.presented - buf.presented + 1) //nolint:gosec // a handful of buffers apart
	}
	return backend.Frame{Renderer: o.backend.chain.renderer(), Target: buf.target, Age: age}, nil
}

func (o *Output) CursorPlane() (backend.CursorPlane, error) {
	return nil, backend.ErrNoCursorPlanes
}

func (o *Output) Present(damage render.Region) error {
	buf := o.drawing
	if buf == nil {
		return fmt.Errorf("nested: present on %s without BeginFrame", o.name)
	}
	o.drawing = nil
	if err := o.backend.chain.finish(); err != nil {
		return err
	}
	if err := o.surface.Attach(buf.wl, 0, 0); err != nil {
		return err
	}
	bounds := image.Rectangle{Max: o.size}
	for _, r := range damage {
		r = r.Intersect(bounds)
		if err := o.surface.DamageBuffer(int32(r.Min.X), int32(r.Min.Y), int32(r.Dx()), int32(r.Dy())); err != nil { //nolint:gosec // clipped to the output, whose size fits int16
			return err
		}
	}
	if err := o.commitForCallback(); err != nil {
		return err
	}
	buf.busy = true
	o.presented++
	buf.presented = o.presented
	o.presentPending = true
	return nil
}

func (o *Output) frameDone() error {
	o.callbackPending = false
	if o.presentPending {
		o.presentPending = false
		o.seq++
		if err := o.backend.handler.Presented(o, backend.Presentation{Time: backend.Now(), Seq: o.seq}); err != nil {
			return err
		}
	}
	return o.clock.Kick()
}

func (o *Output) destroy() error {
	b := o.backend
	i := slices.Index(b.outputs, o)
	if i < 0 {
		return nil
	}
	b.outputs = slices.Delete(b.outputs, i, i+1)
	errs := []error{o.releaseBuffers(), o.toplevel.Destroy(), o.xdgSurface.Destroy(), o.surface.Destroy()}
	if o.configured {
		o.configured = false
		errs = append(errs, b.handler.OutputRemoved(o))
	}
	return errors.Join(errs...)
}

type shmEvents struct{}

func (shmEvents) Format(*cw.WlShm, cw.WlShmFormat) error { return nil }

type wmBaseEvents struct{}

func (wmBaseEvents) Ping(wm *xdg.XdgWmBase, serial uint32) error { return wm.Pong(serial) }

type surfaceEvents struct{}

func (surfaceEvents) Enter(*cw.WlSurface, *cw.WlOutput) error { return nil }

func (surfaceEvents) Leave(*cw.WlSurface, *cw.WlOutput) error { return nil }

func (surfaceEvents) PreferredBufferScale(*cw.WlSurface, int32) error { return nil }

func (surfaceEvents) PreferredBufferTransform(*cw.WlSurface, cw.WlOutputTransform) error { return nil }

type xdgSurfaceEvents struct{ o *Output }

func (e xdgSurfaceEvents) Configure(_ *xdg.XdgSurface, serial uint32) error {
	return e.o.configure(serial)
}

type toplevelEvents struct{ o *Output }

func (e toplevelEvents) Configure(_ *xdg.XdgToplevel, width, height int32, _ []byte) error {
	e.o.pending = image.Pt(int(width), int(height))
	return nil
}

func (e toplevelEvents) Close(*xdg.XdgToplevel) error {
	b := e.o.backend
	if err := e.o.destroy(); err != nil {
		return err
	}
	if len(b.outputs) == 0 {
		return ErrClosed
	}
	return nil
}

func (toplevelEvents) ConfigureBounds(*xdg.XdgToplevel, int32, int32) error { return nil }
func (toplevelEvents) WmCapabilities(*xdg.XdgToplevel, []byte) error        { return nil }

type bufferEvents struct {
	o   *Output
	buf *buffer
}

func (e bufferEvents) Release(*cw.WlBuffer) error {
	e.buf.busy = false
	return e.o.clock.Kick()
}

type callbackEvents struct{ o *Output }

func (e callbackEvents) Done(*cw.WlCallback, uint32) error {
	return e.o.frameDone()
}
