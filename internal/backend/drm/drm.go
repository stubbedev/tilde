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
	Number() (uint64, error)
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
	PrimeFDToHandle(fd int) (uint32, error)
	CloseHandle(handle uint32) error
	RemoveFB(id uint32) error
	Commit(r *kdrm.Request, flags uint32, userData uint64) error
}

type scanout struct {
	target    render.Target
	client    bool
	fb        uint32
	prepare   func() error
	free      func() error
	drop      func() error
	presented uint64
}

type allocator interface {
	renderer() render.Renderer
	renderable(format uint32) ([]uint64, error)
	allocate(size image.Point, format uint32, modifiers []uint64) (*scanout, error)
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
	clients   []gpu.FormatModifier
	props     map[string]kdrm.Property
	taken     bool
}

var _ backend.Scanner = (*Output)(nil)

type Backend struct {
	loop      *loop.Loop
	modifiers bool
	async     bool
	number    uint64
	vendors   edid.Vendors
	report    func(error)
	dev       device
	alloc     allocator
	handler   backend.Handler
	crtcs     []*crtc
	planes    []*plane
	outputs   []*Output
	src       *loop.Source
	req       kdrm.Request
	events    []byte
	cursor    image.Point
	paused    bool
	closed    bool
	gone      bool
}

func newBackend(l *loop.Loop, dev device, alloc allocator, opts Options) (*Backend, error) {
	if opts.Report == nil {
		return nil, errors.New("drm: Options.Report is required")
	}
	b := &Backend{loop: l, dev: dev, alloc: alloc, vendors: opts.Vendors, report: opts.Report, events: make([]byte, eventBuffer)}
	modifiers, err := dev.Cap(kdrm.CapAddFB2Modifiers)
	if err != nil {
		return nil, fmt.Errorf("drm: ADDFB2 modifier support: %w", err)
	}
	b.modifiers = modifiers == 1
	async, err := dev.Cap(kdrm.CapAtomicAsyncFlip)
	if err != nil && !errors.Is(err, unix.EINVAL) {
		return nil, fmt.Errorf("drm: atomic async page flip support: %w", err)
	}
	b.async = async == 1
	if b.number, err = dev.Number(); err != nil {
		return nil, err
	}
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
	var blob []byte
	if in, ok := props["IN_FORMATS"]; ok && in.Value != 0 {
		if blob, err = b.dev.Blob(uint32(in.Value)); err != nil { //nolint:gosec // property blob IDs are 32-bit
			return nil, err
		}
	}
	if p.kind == primaryPlane {
		if p.clients, err = b.clientFormats(info.Formats, blob); err != nil {
			return nil, fmt.Errorf("drm: formats of primary plane %d: %w", id, err)
		}
	}
	if blob == nil {
		return p, nil
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

func (b *Backend) clientFormats(formats []uint32, inFormats []byte) ([]gpu.FormatModifier, error) {
	var pairs []gpu.FormatModifier
	for _, f := range formats {
		modifiers := []uint64{gpu.ModifierInvalid}
		if inFormats != nil {
			var err error
			if modifiers, err = kdrm.Modifiers(inFormats, f); err != nil {
				return nil, err
			}
		}
		for _, m := range modifiers {
			if addable(m, b.modifiers) {
				pairs = append(pairs, gpu.FormatModifier{Format: f, Modifier: m})
			}
		}
	}
	return pairs, nil
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
		if !slices.Contains(res.Connectors, o.conn.ID) {
			errs = append(errs, b.remove(o))
			continue
		}
		c, err := b.dev.Connector(o.conn.ID)
		switch {
		case errors.Is(err, unix.ENOENT):
			errs = append(errs, b.remove(o))
		case err != nil:
			errs = append(errs, err, b.remove(o))
		case c.Connection != kdrm.Connected:
			errs = append(errs, b.remove(o))
		}
	}
	for _, id := range res.Connectors {
		if slices.ContainsFunc(b.outputs, func(o *Output) bool { return o.conn.ID == id }) {
			continue
		}
		c, err := b.dev.Connector(id)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
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
	o := &Output{b: b, conn: c, props: props, crtc: cr, primary: p}
	if blob, ok := props["EDID"]; ok && blob.Value != 0 {
		data, err := b.dev.Blob(uint32(blob.Value)) //nolint:gosec // property blob IDs are 32-bit
		if err != nil {
			return err
		}
		if info, err := edid.Parse(data, b.vendors); err != nil {
			b.report(fmt.Errorf("drm: %s has an EDID tilde cannot read, so only its connector names it: %w", c.Name(), err))
		} else {
			o.identity = info
		}
	}
	cr.output, p.taken = o, true
	if cp := b.free(cursorPlane, cr); cp != nil {
		cp.taken = true
		o.cursor = &cursor{o: o, plane: cp}
	}
	if o.pacer, err = b.loop.AddTimer(o.paceElapsed); err != nil {
		return errors.Join(err, o.release())
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
		o.modeset, o.flipping = true, false
		errs = append(errs, o.dropScanout(o.queued))
		o.queued = nil
		if o.cursor != nil {
			o.cursor.queued = nil
		}
		errs = append(errs, b.handler.OutputDamaged(o))
	}
	return errors.Join(append(errs, b.scan())...)
}

func (b *Backend) Unplug() error {
	if b.closed {
		return nil
	}
	b.closed, b.gone = true, true
	var errs []error
	for len(b.outputs) > 0 {
		o := b.outputs[0]
		b.outputs = b.outputs[1:]
		if b.handler != nil {
			errs = append(errs, b.handler.OutputRemoved(o))
		}
		errs = append(errs, o.release())
	}
	if b.src != nil {
		errs = append(errs, b.src.Remove())
	}
	return errors.Join(append(errs, b.alloc.close())...)
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
	identity       edid.Info
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
	pacer          *loop.Timer
	paced          bool
	lastFlip       time.Duration
	presented      uint64
	gamma          gamma
	torn           bool
}

func (o *Output) Name() string {
	return o.conn.Name()
}

func (o *Output) Description() string {
	if d := o.identity.Description(); d != "" {
		return d
	}
	return o.Name()
}

func (o *Output) Identity() edid.Info {
	return o.identity
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
	if o.lit {
		r := &o.b.req
		r.Reset()
		o.add(o.crtc.id, o.crtc.props, "ACTIVE", 0)
		o.detach()
		if err := o.b.dev.Commit(r, kdrm.AtomicAllowModeset, 0); err != nil {
			return fmt.Errorf("drm: power off %s: %w", o.Name(), err)
		}
	}
	o.off, o.frameRequested, o.flipping = true, false, false
	return o.dropScanouts()
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
	o.enabled, o.lit, o.frameRequested, o.flipping = false, false, false, false
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
	if !o.frameRequested || o.flipping || o.paced || o.idleQueued || o.drawing != nil {
		return
	}
	o.idleQueued = true
	o.b.loop.Idle(func() error {
		o.idleQueued = false
		if !o.frameRequested || !o.enabled || o.flipping || o.paced || o.b.paused || o.b.closed {
			return nil
		}
		o.frameRequested, o.framing = false, true
		err := o.b.handler.Frame(o)
		o.framing = false
		if err == nil && !o.flipping && o.enabled && !o.off {
			err = o.pace()
		}
		return errors.Join(err, o.flushCursor(), o.flushGamma())
	})
}

func (o *Output) pace() error {
	now := backend.Now()
	at, _ := backend.NextVblank(o.lastFlip, now, modeOf(o.mode).Interval())
	if err := o.pacer.Arm(at - now); err != nil {
		return fmt.Errorf("drm: pace the frames of %s: %w", o.Name(), err)
	}
	o.paced = true
	return nil
}

func (o *Output) paceElapsed() error {
	o.paced = false
	o.maybeFrame()
	return errors.Join(o.flushCursor(), o.flushGamma())
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
	if s.prepare != nil {
		if err := s.prepare(); err != nil {
			return fmt.Errorf("drm: copy the frame of %s to its device: %w", o.Name(), err)
		}
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
	o.modeset, o.lit, o.flipping, o.torn = false, true, true, false
	o.queued = s
	return nil
}

func (o *Output) ScanoutFormats() gpu.ScanoutTarget {
	return gpu.ScanoutTarget{Device: o.b.number, Formats: o.primary.clients}
}

func (o *Output) Scanout(d gpu.DMABUF, release func() error, tear bool) (bool, error) {
	if !o.enabled || o.off || o.b.paused || o.modeset || o.drawing != nil || o.flipping || d.Size != modeOf(o.mode).Size {
		return false, nil
	}
	fb, remove, err := importFB(o.b.dev, d, o.b.modifiers)
	if err != nil {
		return false, nil
	}
	s := &scanout{client: true, fb: fb, free: func() error { return errors.Join(remove(), release()) }, drop: release}
	flags := uint32(kdrm.AtomicNonblock | kdrm.AtomicPageFlipEvent)
	torn := tear && o.tearable(fb, flags|kdrm.AtomicPageFlipAsync)
	if torn {
		flags |= kdrm.AtomicPageFlipAsync
	} else {
		o.frame(s, -1)
		if o.test(flags) != nil {
			return false, remove()
		}
	}
	if err := o.b.dev.Commit(&o.b.req, flags, uint64(o.crtc.id)); err != nil {
		return false, errors.Join(fmt.Errorf("drm: commit a client buffer on %s: %w", o.Name(), err), remove())
	}
	if o.cursor != nil && !torn {
		o.cursor.sent()
	}
	o.lit, o.flipping, o.queued, o.torn = true, true, s, torn
	if torn {
		return true, nil
	}
	return true, o.gammaSent()
}

func (o *Output) tearable(fb uint32, flags uint32) bool {
	if !o.b.async || o.front == nil || o.gamma.dirty || o.cursor != nil && o.cursor.dirty {
		return false
	}
	r := &o.b.req
	r.Reset()
	o.add(o.primary.id, o.primary.props, "FB_ID", uint64(fb))
	return o.test(flags) == nil
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
	return o.gammaSent()
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
	o.addGamma()
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
	o.lastFlip = time.Duration(e.Sec)*time.Second + time.Duration(e.Usec)*time.Microsecond
	if c := o.cursor; c != nil && c.queued != nil {
		c.front, c.queued = c.queued, nil
	}
	if o.queued == nil {
		o.maybeFrame()
		return errors.Join(o.flushCursor(), o.flushGamma())
	}
	var errs []error
	if old := o.front; old != nil && old.client {
		errs = append(errs, old.free())
	}
	o.front, o.queued = o.queued, nil
	o.presented++
	o.front.presented = o.presented
	errs = append(errs, o.b.handler.Presented(o, backend.Presentation{Time: o.lastFlip, Refresh: modeOf(o.mode).Interval(), Seq: uint64(e.Sequence), Torn: o.torn}))
	o.maybeFrame()
	return errors.Join(append(errs, o.flushCursor(), o.flushGamma())...)
}

func (o *Output) dropScanout(s *scanout) error {
	switch {
	case s == nil || !s.client:
		return nil
	case o.b.gone:
		return s.drop()
	}
	return s.free()
}

func (o *Output) dropScanouts() error {
	err := errors.Join(o.dropScanout(o.front), o.dropScanout(o.queued))
	o.front, o.queued = nil, nil
	return err
}

func (o *Output) freeBuffers() error {
	errs := []error{o.dropScanouts()}
	for _, s := range o.buffers {
		if o.b.gone {
			errs = append(errs, s.drop())
		} else {
			errs = append(errs, s.free())
		}
	}
	o.buffers, o.drawing, o.queued, o.front = nil, nil, nil, nil
	return errors.Join(errs...)
}

func (o *Output) release() error {
	err := errors.Join(o.freeBuffers(), o.releaseGamma())
	if o.modeBlob != 0 && !o.b.gone {
		err = errors.Join(err, o.b.dev.DestroyBlob(o.modeBlob))
		o.modeBlob = 0
	}
	o.crtc.output, o.primary.taken = nil, false
	if o.pacer != nil {
		err = errors.Join(err, o.pacer.Remove())
		o.pacer = nil
	}
	if o.cursor != nil {
		err = errors.Join(err, o.cursor.release())
		o.cursor = nil
	}
	return err
}
