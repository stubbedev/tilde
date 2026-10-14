// Package drm is the native backend: one DRM device driven with atomic KMS,
// scanning out GBM buffers that the GLES renderer draws into.
package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend"
	kdrm "github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/edid"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
)

const (
	scanoutFormat = gpu.FormatXRGB8888
	cursorFormat  = gpu.FormatARGB8888
	buffersPerOut = 2
	cursorBuffers = 3
	eventBuffer   = 1024
)

type device interface {
	FD() int
	Cap(capability uint64) (uint64, error)
	Resources() (kdrm.Resources, error)
	Connector(id uint32) (kdrm.Connector, error)
	Encoder(id uint32) (kdrm.Encoder, error)
	Planes() ([]uint32, error)
	Plane(id uint32) (kdrm.Plane, error)
	Properties(object, kind uint32) (map[string]kdrm.Property, error)
	PropertyInfo(id uint32) (kdrm.PropertyInfo, error)
	Blob(id uint32) ([]byte, error)
	CreateBlob(data []byte) (uint32, error)
	DestroyBlob(id uint32) error
	CreateDumb(size image.Point) (kdrm.Dumb, error)
	DestroyDumb(b kdrm.Dumb) error
	AddFB2(fb kdrm.FB) (uint32, error)
	RemoveFB(id uint32) error
	Commit(r *kdrm.Request, flags uint32, userData uint64) error
}

type scanout struct {
	target    render.Target
	fb        uint32
	free      func() error
	presented uint64
}

type allocator interface {
	renderer() render.Renderer
	renderable(format uint32) ([]uint64, error)
	allocate(size image.Point, format uint32, modifiers []uint64) (*scanout, error)
	offscreen(size image.Point) (render.ReadTarget, error)
	fence() (int, error)
	close() error
}

type crtc struct {
	id     uint32
	index  int
	props  map[string]kdrm.Property
	output *Output
}

type planeKind uint8

const (
	primaryPlane planeKind = iota
	cursorPlane
)

type plane struct {
	id        uint32
	kind      planeKind
	possible  uint32
	modifiers []uint64
	props     map[string]kdrm.Property
	taken     bool
}

type Backend struct {
	loop    *loop.Loop
	vendors edid.Vendors
	dev     device
	alloc   allocator
	handler backend.Handler
	crtcs   []*crtc
	planes  []*plane
	outputs []*Output
	src     *loop.Source
	req     kdrm.Request
	events  []byte
	cursor  image.Point
	paused  bool
	closed  bool
}

func newBackend(l *loop.Loop, dev device, alloc allocator, vendors edid.Vendors) (*Backend, error) {
	b := &Backend{loop: l, dev: dev, alloc: alloc, vendors: vendors, events: make([]byte, eventBuffer)}
	res, err := dev.Resources()
	if err != nil {
		return nil, err
	}
	for i, id := range res.CRTCs {
		props, err := dev.Properties(id, kdrm.ObjectCRTC)
		if err != nil {
			return nil, err
		}
		b.crtcs = append(b.crtcs, &crtc{id: id, index: i, props: props})
	}
	planes, err := dev.Planes()
	if err != nil {
		return nil, err
	}
	renderable, err := alloc.renderable(scanoutFormat)
	if err != nil {
		return nil, err
	}
	var size [2]uint64
	for i, c := range []uint64{kdrm.CapCursorWidth, kdrm.CapCursorHeight} {
		if size[i], err = dev.Cap(c); err != nil {
			return nil, fmt.Errorf("drm: cursor size: %w", err)
		}
	}
	b.cursor = image.Pt(int(size[0]), int(size[1])) //nolint:gosec // cursor sizes are a few hundred pixels
	for _, id := range planes {
		p, err := b.plane(id, renderable)
		if err != nil {
			return nil, err
		}
		if p != nil {
			b.planes = append(b.planes, p)
		}
	}
	if b.src, err = l.AddFD(dev.FD(), loop.Readable, b.readEvents); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Backend) plane(id uint32, renderable []uint64) (*plane, error) {
	info, err := b.dev.Plane(id)
	if err != nil {
		return nil, err
	}
	props, err := b.dev.Properties(id, kdrm.ObjectPlane)
	if err != nil {
		return nil, err
	}
	kind, ok := props["type"]
	if !ok {
		return nil, fmt.Errorf("drm: plane %d has no type property", id)
	}
	enum, err := b.dev.PropertyInfo(kind.ID)
	if err != nil {
		return nil, err
	}
	p := &plane{id: id, possible: info.PossibleCRTCs, props: props}
	var format uint32
	switch kind.Value {
	case enum.Enums["Primary"]:
		p.kind, format = primaryPlane, scanoutFormat
	case enum.Enums["Cursor"]:
		p.kind, format = cursorPlane, cursorFormat
	default:
		return nil, nil
	}
	if !slices.Contains(info.Formats, format) {
		return nil, nil
	}
	in, ok := props["IN_FORMATS"]
	if !ok || in.Value == 0 {
		return p, nil
	}
	blob, err := b.dev.Blob(uint32(in.Value)) //nolint:gosec // property blob IDs are 32-bit
	if err != nil {
		return nil, err
	}
	scanable, err := kdrm.Modifiers(blob, format)
	if err != nil {
		return nil, err
	}
	if p.kind == cursorPlane {
		if !slices.Contains(scanable, gpu.ModifierLinear) {
			return nil, nil
		}
		return p, nil
	}
	for _, m := range scanable {
		if slices.Contains(renderable, m) {
			p.modifiers = append(p.modifiers, m)
		}
	}
	if len(p.modifiers) == 0 {
		return nil, fmt.Errorf("drm: primary plane %d scans out no XRGB8888 modifier the renderer can draw: plane %#x, renderer %#x", id, scanable, renderable)
	}
	return p, nil
}

func (b *Backend) Renderer() render.Renderer {
	return b.alloc.renderer()
}

func (b *Backend) Start(h backend.Handler) error {
	if b.handler != nil {
		return errors.New("drm: already started")
	}
	b.handler = h
	return b.scan()
}

func (b *Backend) scan() error {
	res, err := b.dev.Resources()
	if err != nil {
		return err
	}
	var errs []error
	for _, o := range slices.Clone(b.outputs) {
		c, err := b.dev.Connector(o.conn.ID)
		if err != nil || c.Connection != kdrm.Connected {
			errs = append(errs, err, b.remove(o))
		}
	}
	for _, id := range res.Connectors {
		if slices.ContainsFunc(b.outputs, func(o *Output) bool { return o.conn.ID == id }) {
			continue
		}
		c, err := b.dev.Connector(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if c.Connection != kdrm.Connected || len(c.Modes) == 0 {
			continue
		}
		errs = append(errs, b.add(c))
	}
	return errors.Join(errs...)
}

func (b *Backend) Hotplug() error {
	if b.paused {
		return nil
	}
	return b.scan()
}

func (b *Backend) free(kind planeKind, cr *crtc) *plane {
	for _, p := range b.planes {
		if p.kind == kind && !p.taken && p.possible&(1<<cr.index) != 0 {
			return p
		}
	}
	return nil
}

func (b *Backend) pick(c kdrm.Connector) (*crtc, *plane, error) {
	for _, encID := range c.Encoders {
		enc, err := b.dev.Encoder(encID)
		if err != nil {
			return nil, nil, err
		}
		for _, cr := range b.crtcs {
			if cr.output != nil || enc.PossibleCRTCs&(1<<cr.index) == 0 {
				continue
			}
			if p := b.free(primaryPlane, cr); p != nil {
				return cr, p, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("drm: no free CRTC and primary plane for %s", c.Name())
}

func (b *Backend) add(c kdrm.Connector) error {
	props, err := b.dev.Properties(c.ID, kdrm.ObjectConnector)
	if err != nil {
		return err
	}
	cr, p, err := b.pick(c)
	if err != nil {
		return err
	}
	o := &Output{b: b, conn: c, props: props, crtc: cr, primary: p, description: c.Name()}
	if blob, ok := props["EDID"]; ok && blob.Value != 0 {
		data, err := b.dev.Blob(uint32(blob.Value)) //nolint:gosec // property blob IDs are 32-bit
		if err != nil {
			return err
		}
		info, err := edid.Parse(data, b.vendors)
		if err != nil {
			return fmt.Errorf("drm: %s: %w", c.Name(), err)
		}
		o.description = info.Description()
	}
	cr.output, p.taken = o, true
	if cp := b.free(cursorPlane, cr); cp != nil {
		cp.taken = true
		o.cursor = &cursor{o: o, plane: cp}
	}
	i := max(slices.IndexFunc(c.Modes, kdrm.Mode.Preferred), 0)
	if err := o.setMode(c.Modes[i]); err != nil {
		return errors.Join(err, o.release())
	}
	b.outputs = append(b.outputs, o)
	return b.handler.OutputAdded(o)
}

func (b *Backend) remove(o *Output) error {
	i := slices.Index(b.outputs, o)
	if i < 0 {
		return nil
	}
	b.outputs = slices.Delete(b.outputs, i, i+1)
	return errors.Join(b.handler.OutputRemoved(o), o.disable(), o.release())
}

func (b *Backend) Outputs() []*Output {
	return slices.Clone(b.outputs)
}

func (b *Backend) readEvents(loop.Events) error {
	for {
		n, err := unix.Read(b.dev.FD(), b.events)
		switch {
		case errors.Is(err, unix.EAGAIN):
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil:
			return fmt.Errorf("drm: read events: %w", err)
		case n == 0:
			return nil
		}
		if err := kdrm.ParseEvents(b.events[:n], b.flipped); err != nil {
			return err
		}
		if n < len(b.events) {
			return nil
		}
	}
}

func (b *Backend) flipped(e kdrm.FlipEvent) error {
	for _, o := range b.outputs {
		if uint64(o.crtc.id) == e.UserData {
			return o.flipped(e)
		}
	}
	return nil
}

func (b *Backend) Paused() bool {
	return b.paused
}

func (b *Backend) Pause() {
	b.paused = true
}

func (b *Backend) Wake() error {
	var errs []error
	for _, o := range b.outputs {
		o.modeset = true
		errs = append(errs, b.handler.OutputDamaged(o))
	}
	return errors.Join(errs...)
}

func (b *Backend) Resume() error {
	b.paused = false
	var errs []error
	for _, o := range b.outputs {
		o.modeset, o.flipping, o.queued = true, false, nil
		if o.cursor != nil {
			o.cursor.queued = nil
		}
		errs = append(errs, b.handler.OutputDamaged(o))
	}
	return errors.Join(append(errs, b.scan())...)
}

func (b *Backend) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	var errs []error
	for len(b.outputs) > 0 {
		errs = append(errs, b.remove(b.outputs[0]))
	}
	if b.src != nil {
		errs = append(errs, b.src.Remove())
	}
	return errors.Join(append(errs, b.alloc.close())...)
}

type Output struct {
	b              *Backend
	description    string
	conn           kdrm.Connector
	props          map[string]kdrm.Property
	crtc           *crtc
	primary        *plane
	cursor         *cursor
	mode           kdrm.Mode
	modeBlob       uint32
	enabled        bool
	off            bool
	modeset        bool
	lit            bool
	buffers        []*scanout
	drawing        *scanout
	queued         *scanout
	front          *scanout
	frameRequested bool
	flipping       bool
	framing        bool
	idleQueued     bool
	presented      uint64
}

func (o *Output) Name() string {
	return o.conn.Name()
}

func (o *Output) Description() string {
	return o.description
}

func (o *Output) PhysicalSize() image.Point {
	return image.Pt(int(o.conn.PhysicalMM[0]), int(o.conn.PhysicalMM[1]))
}

func modeOf(m kdrm.Mode) backend.Mode {
	return backend.Mode{Size: image.Pt(int(m.HDisplay), int(m.VDisplay)), RefreshMHz: m.RefreshMHz(), Preferred: m.Preferred()}
}

func (o *Output) Modes() []backend.Mode {
	out := make([]backend.Mode, len(o.conn.Modes))
	for i, m := range o.conn.Modes {
		out[i] = modeOf(m)
	}
	return out
}

func (o *Output) State() backend.OutputState {
	return backend.OutputState{Enabled: o.enabled, Mode: modeOf(o.mode), Off: o.enabled && o.off}
}

func (o *Output) setMode(m kdrm.Mode) error {
	blob, err := o.b.dev.CreateBlob(m.Bytes())
	if err != nil {
		return err
	}
	if err := o.freeBuffers(); err != nil {
		return errors.Join(err, o.b.dev.DestroyBlob(blob))
	}
	size := image.Pt(int(m.HDisplay), int(m.VDisplay))
	for range buffersPerOut {
		s, err := o.b.alloc.allocate(size, scanoutFormat, o.primary.modifiers)
		if err != nil {
			return errors.Join(err, o.b.dev.DestroyBlob(blob), o.freeBuffers())
		}
		o.buffers = append(o.buffers, s)
	}
	if o.modeBlob != 0 {
		err = o.b.dev.DestroyBlob(o.modeBlob)
	}
	o.mode, o.modeBlob, o.enabled, o.modeset = m, blob, true, true
	return err
}

func (o *Output) Commit(s backend.OutputState) error {
	if !s.Enabled {
		return o.disable()
	}
	i := slices.IndexFunc(o.conn.Modes, func(m kdrm.Mode) bool { return modeOf(m) == s.Mode })
	if i < 0 {
		return fmt.Errorf("drm: %s has no mode %+v", o.Name(), s.Mode)
	}
	if o.enabled && o.conn.Modes[i] == o.mode {
		return o.setPower(!s.Off)
	}
	if err := o.setMode(o.conn.Modes[i]); err != nil {
		return err
	}
	o.off = s.Off
	return o.ScheduleFrame()
}

func (o *Output) setPower(on bool) error {
	if on == !o.off {
		return nil
	}
	if on {
		o.off, o.modeset = false, true
		return o.b.handler.OutputDamaged(o)
	}
	o.off, o.queued, o.front, o.frameRequested, o.flipping = true, nil, nil, false, false
	if !o.lit {
		return nil
	}
	r := &o.b.req
	r.Reset()
	o.add(o.crtc.id, o.crtc.props, "ACTIVE", 0)
	o.detach()
	if err := o.b.dev.Commit(r, kdrm.AtomicAllowModeset, 0); err != nil {
		return fmt.Errorf("drm: power off %s: %w", o.Name(), err)
	}
	return nil
}

func (o *Output) disable() error {
	if !o.enabled {
		return nil
	}
	if !o.lit {
		o.enabled = false
		return o.freeBuffers()
	}
	r := &o.b.req
	r.Reset()
	o.add(o.conn.ID, o.props, "CRTC_ID", 0)
	o.add(o.crtc.id, o.crtc.props, "ACTIVE", 0)
	o.add(o.crtc.id, o.crtc.props, "MODE_ID", 0)
	o.detach()
	if err := o.b.dev.Commit(r, kdrm.AtomicAllowModeset, 0); err != nil {
		return fmt.Errorf("drm: disable %s: %w", o.Name(), err)
	}
	o.enabled, o.lit, o.queued, o.front, o.frameRequested, o.flipping = false, false, nil, nil, false, false
	return o.freeBuffers()
}

func (o *Output) detach() {
	o.add(o.primary.id, o.primary.props, "FB_ID", 0)
	o.add(o.primary.id, o.primary.props, "CRTC_ID", 0)
	if c := o.cursor; c != nil {
		o.add(c.plane.id, c.plane.props, "FB_ID", 0)
		o.add(c.plane.id, c.plane.props, "CRTC_ID", 0)
		c.queued, c.front, c.dirty = nil, nil, true
	}
}

func (o *Output) add(object uint32, props map[string]kdrm.Property, name string, value uint64) {
	if p, ok := props[name]; ok {
		o.b.req.Add(object, p.ID, value)
	}
}

func (o *Output) ScheduleFrame() error {
	if !o.enabled || o.off || o.b.paused {
		return nil
	}
	o.frameRequested = true
	o.maybeFrame()
	return nil
}

func (o *Output) maybeFrame() {
	if !o.frameRequested || o.flipping || o.idleQueued || o.drawing != nil {
		return
	}
	o.idleQueued = true
	o.b.loop.Idle(func() error {
		o.idleQueued = false
		if !o.frameRequested || !o.enabled || o.flipping || o.b.paused || o.b.closed {
			return nil
		}
		o.frameRequested, o.framing = false, true
		err := o.b.handler.Frame(o)
		o.framing = false
		return errors.Join(err, o.flushCursor())
	})
}

func (o *Output) BeginFrame() (backend.Frame, error) {
	switch {
	case !o.enabled:
		return backend.Frame{}, fmt.Errorf("drm: frame on disabled %s", o.Name())
	case o.drawing != nil:
		return backend.Frame{}, fmt.Errorf("drm: %s already has a frame in progress", o.Name())
	}
	i := slices.IndexFunc(o.buffers, func(s *scanout) bool { return s != o.front && s != o.queued })
	if i < 0 {
		return backend.Frame{}, fmt.Errorf("drm: every buffer of %s is on screen or queued", o.Name())
	}
	s := o.buffers[i]
	o.drawing = s
	age := 0
	if s.presented != 0 {
		age = int(o.presented - s.presented + 1) //nolint:gosec // a couple of buffers apart
	}
	return backend.Frame{Renderer: o.b.alloc.renderer(), Target: s.target, Age: age}, nil
}

func (o *Output) Present(render.Region) error {
	s := o.drawing
	if s == nil {
		return fmt.Errorf("drm: present on %s without BeginFrame", o.Name())
	}
	o.drawing = nil
	if o.b.paused {
		return nil
	}
	fence, err := o.b.alloc.fence()
	if err != nil {
		return err
	}
	flags := uint32(kdrm.AtomicNonblock | kdrm.AtomicPageFlipEvent)
	if o.modeset {
		flags |= kdrm.AtomicAllowModeset
	}
	err = o.submit(s, fence, flags)
	if fence >= 0 {
		err = errors.Join(err, unix.Close(fence))
	}
	if err != nil {
		return err
	}
	o.modeset, o.lit, o.flipping = false, true, true
	o.queued = s
	return nil
}

func (o *Output) submit(s *scanout, fence int, flags uint32) error {
	o.frame(s, fence)
	err := o.test(flags)
	if c := o.cursor; err != nil && c != nil && c.visible {
		c.visible = false
		o.frame(s, fence)
		if o.test(flags) != nil {
			c.visible = true
			return err
		}
		c.refused, err = err, nil
	}
	if err != nil {
		return err
	}
	if err := o.b.dev.Commit(&o.b.req, flags, uint64(o.crtc.id)); err != nil {
		return fmt.Errorf("drm: commit the frame for %s: %w", o.Name(), err)
	}
	if o.cursor != nil {
		o.cursor.sent()
	}
	return nil
}

func (o *Output) frame(s *scanout, fence int) {
	r := &o.b.req
	r.Reset()
	if o.modeset {
		o.add(o.conn.ID, o.props, "CRTC_ID", uint64(o.crtc.id))
		o.add(o.crtc.id, o.crtc.props, "MODE_ID", uint64(o.modeBlob))
		o.add(o.crtc.id, o.crtc.props, "ACTIVE", 1)
	}
	o.place(o.primary, s.fb, image.Rectangle{Max: image.Pt(int(o.mode.HDisplay), int(o.mode.VDisplay))}, fence)
	if o.cursor != nil {
		o.cursor.add()
	}
}

func (o *Output) place(p *plane, fb uint32, at image.Rectangle, fence int) {
	w, h := uint64(at.Dx()), uint64(at.Dy()) //nolint:gosec // plane sizes are positive
	o.add(p.id, p.props, "FB_ID", uint64(fb))
	o.add(p.id, p.props, "CRTC_ID", uint64(o.crtc.id))
	o.add(p.id, p.props, "SRC_X", 0)
	o.add(p.id, p.props, "SRC_Y", 0)
	o.add(p.id, p.props, "SRC_W", w<<16)
	o.add(p.id, p.props, "SRC_H", h<<16)
	o.add(p.id, p.props, "CRTC_X", uint64(int64(at.Min.X))) //nolint:gosec // signed plane positions travel as two's complement
	o.add(p.id, p.props, "CRTC_Y", uint64(int64(at.Min.Y))) //nolint:gosec // signed plane positions travel as two's complement
	o.add(p.id, p.props, "CRTC_W", w)
	o.add(p.id, p.props, "CRTC_H", h)
	if fence >= 0 {
		o.add(p.id, p.props, "IN_FENCE_FD", uint64(fence))
	}
}

func (o *Output) test(flags uint32) error {
	test := flags&^(kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent) | kdrm.AtomicTestOnly
	if err := o.b.dev.Commit(&o.b.req, test, 0); err != nil {
		return fmt.Errorf("drm: test the frame for %s: %w", o.Name(), err)
	}
	return nil
}

func (o *Output) flushCursor() error {
	c := o.cursor
	if c == nil || !c.dirty || !o.lit || o.off || o.b.paused || o.flipping || o.frameRequested || o.framing {
		return nil
	}
	r := &o.b.req
	r.Reset()
	c.add()
	flags := uint32(kdrm.AtomicNonblock | kdrm.AtomicPageFlipEvent)
	if err := o.test(flags); err != nil {
		c.refused, c.visible = err, false
		return err
	}
	if err := o.b.dev.Commit(r, flags, uint64(o.crtc.id)); err != nil {
		return fmt.Errorf("drm: commit the cursor for %s: %w", o.Name(), err)
	}
	o.flipping = true
	c.sent()
	return nil
}

func (o *Output) flipped(e kdrm.FlipEvent) error {
	if !o.flipping {
		return nil
	}
	o.flipping = false
	if c := o.cursor; c != nil && c.queued != nil {
		c.front, c.queued = c.queued, nil
	}
	if o.queued == nil {
		o.maybeFrame()
		return o.flushCursor()
	}
	o.front, o.queued = o.queued, nil
	o.presented++
	o.front.presented = o.presented
	t := time.Duration(e.Sec)*time.Second + time.Duration(e.Usec)*time.Microsecond
	err := o.b.handler.Presented(o, backend.Presentation{Time: t, Refresh: modeOf(o.mode).Interval(), Seq: uint64(e.Sequence)})
	o.maybeFrame()
	return errors.Join(err, o.flushCursor())
}

func (o *Output) freeBuffers() error {
	var errs []error
	for _, s := range o.buffers {
		errs = append(errs, s.free())
	}
	o.buffers, o.drawing, o.queued, o.front = nil, nil, nil, nil
	return errors.Join(errs...)
}

func (o *Output) release() error {
	err := o.freeBuffers()
	if o.modeBlob != 0 {
		err = errors.Join(err, o.b.dev.DestroyBlob(o.modeBlob))
		o.modeBlob = 0
	}
	o.crtc.output, o.primary.taken = nil, false
	if o.cursor != nil {
		err = errors.Join(err, o.cursor.release())
		o.cursor = nil
	}
	return err
}
