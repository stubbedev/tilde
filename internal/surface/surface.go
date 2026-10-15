// Package surface implements wl_compositor, wl_surface, wl_region and
// wl_subcompositor: double-buffered surface state, subsurface trees and roles.
package surface

import (
	"errors"
	"fmt"
	"image"
	"iter"
	"slices"

	sfs "github.com/stubbedev/tilde/internal/protocol/server/fractionalscale"
	spt "github.com/stubbedev/tilde/internal/protocol/server/presentationtime"
	svp "github.com/stubbedev/tilde/internal/protocol/server/viewporter"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/region"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	compositorVersion    = 6
	subcompositorVersion = 1
)

type Buffer interface {
	Size() image.Point
}

type Role interface {
	RoleName() string
	Committed(s *Surface) error
}

type Listener interface {
	Committed(s *Surface) error
	Destroyed(s *Surface) error
}

type Manager struct {
	listener Listener
}

func New(d *server.Display, l Listener) (*Manager, error) {
	m := &Manager{listener: l}
	if _, err := d.AddGlobal(sw.WlCompositorInterface, compositorVersion, m.bindCompositor); err != nil {
		return nil, err
	}
	if _, err := d.AddGlobal(sw.WlSubcompositorInterface, subcompositorVersion, m.bindSubcompositor); err != nil {
		return nil, err
	}
	if _, err := d.AddGlobal(svp.WpViewporterInterface, viewporterVersion, bindViewporter); err != nil {
		return nil, err
	}
	if _, err := d.AddGlobal(sfs.WpFractionalScaleManagerV1Interface, fractionalVersion, bindFractional); err != nil {
		return nil, err
	}
	if _, err := d.AddGlobal(spt.WpPresentationInterface, presentationVersion, bindPresentation); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) bindCompositor(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sw.NewWlCompositor(c, id, version, compositorRequests{m, c})
	return err
}

func (m *Manager) bindSubcompositor(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sw.NewWlSubcompositor(c, id, version, subcompositorRequests{})
	return err
}

type compositorRequests struct {
	m *Manager
	c *server.Client
}

func (r compositorRequests) CreateSurface(_ *sw.WlCompositor, res *sw.WlSurface) (sw.WlSurfaceHandler, error) {
	s := &Surface{manager: r.m, client: r.c, resource: res}
	s.current.scale, s.current.inputInfinite = 1, true
	s.order = []*Surface{s}
	s.pendingOrder = []*Surface{s}
	server.OnDestroy(res, s.destroy)
	return surfaceRequests{s}, nil
}

func (r compositorRequests) CreateRegion(_ *sw.WlCompositor, res *sw.WlRegion) (sw.WlRegionHandler, error) {
	return &regionObject{}, nil
}

func (r compositorRequests) Release(*sw.WlCompositor) error {
	return nil
}

type regionObject struct {
	value region.Region
}

func (g *regionObject) Destroy(*sw.WlRegion) error {
	return nil
}

func rect(x, y, w, h int32) image.Rectangle {
	if w <= 0 || h <= 0 {
		return image.Rectangle{}
	}
	return image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h))
}

func (g *regionObject) Add(_ *sw.WlRegion, x, y, w, h int32) error {
	g.value = g.value.Add(rect(x, y, w, h))
	return nil
}

func (g *regionObject) Subtract(_ *sw.WlRegion, x, y, w, h int32) error {
	g.value = g.value.Subtract(rect(x, y, w, h))
	return nil
}

func RegionValue(r *sw.WlRegion) (region.Region, error) {
	g, ok := r.Handler().(*regionObject)
	if !ok {
		return region.Region{}, fmt.Errorf("surface: wl_region@%d has handler %T", r.Base().ID(), r.Handler())
	}
	return g.value, nil
}

type field uint8

const (
	fieldBuffer field = 1 << iota
	fieldOffset
	fieldScale
	fieldTransform
	fieldOpaque
	fieldInput
	fieldSource
	fieldDest
)

type state struct {
	set           field
	buffer        Buffer
	offset        image.Point
	damage        region.Region
	bufferDamage  region.Region
	scale         int32
	transform     render.Transform
	opaque        region.Region
	input         region.Region
	inputInfinite bool
	source        render.FRect
	hasSource     bool
	dest          image.Point
	hasDest       bool
	frames        []*sw.WlCallback
	feedbacks     []*spt.WpPresentationFeedback
}

func (dst *state) merge(src *state) {
	if src.set&fieldBuffer != 0 {
		dst.buffer = src.buffer
	}
	if src.set&fieldOffset != 0 {
		dst.offset = dst.offset.Add(src.offset)
	}
	if src.set&fieldScale != 0 {
		dst.scale = src.scale
	}
	if src.set&fieldTransform != 0 {
		dst.transform = src.transform
	}
	if src.set&fieldOpaque != 0 {
		dst.opaque = src.opaque
	}
	if src.set&fieldInput != 0 {
		dst.input, dst.inputInfinite = src.input, src.inputInfinite
	}
	if src.set&fieldSource != 0 {
		dst.source, dst.hasSource = src.source, src.hasSource
	}
	if src.set&fieldDest != 0 {
		dst.dest, dst.hasDest = src.dest, src.hasDest
	}
	dst.set |= src.set
	dst.damage = dst.damage.Union(src.damage)
	dst.bufferDamage = dst.bufferDamage.Union(src.bufferDamage)
	dst.frames = append(dst.frames, src.frames...)
	dst.feedbacks = append(dst.feedbacks, src.feedbacks...)
	*src = state{}
}

type Surface struct {
	manager      *Manager
	client       *server.Client
	resource     *sw.WlSurface
	pending      state
	cache        *Update
	queue        []*Update
	current      state
	latched      []*spt.WpPresentationFeedback
	damage       region.Region
	bufferDamage region.Region
	attached     bool
	offset       image.Point
	role         Role
	roleName     string
	sub          *Subsurface
	order        []*Surface
	pendingOrder []*Surface
	viewport     *viewport
	fractional   *fractional
	preferred    uint32
	destroyed    bool
	hooks        []*Hooks
}

type Hooks struct {
	Commit    func(u *Update) error
	Applied   func() error
	Destroyed func() error
}

type Update struct {
	s       *Surface
	state   state
	gates   []func() bool
	applied []func() error
}

func (u *Update) Gate(ready func() bool) {
	u.gates = append(u.gates, ready)
}

func (u *Update) Hold() *Hold {
	h := &Hold{s: u.s}
	u.Gate(func() bool { return h.done })
	return h
}

func (u *Update) OnApply(f func() error) {
	u.applied = append(u.applied, f)
}

func (u *Update) ready() bool {
	for _, ready := range u.gates {
		if !ready() {
			return false
		}
	}
	return true
}

func (u *Update) absorb(later *Update) {
	u.state.merge(&later.state)
	u.gates = append(u.gates, later.gates...)
	u.applied = append(u.applied, later.applied...)
}

func (s *Surface) AddHooks(h *Hooks) {
	s.hooks = append(s.hooks, h)
}

func (s *Surface) RemoveHooks(h *Hooks) {
	s.hooks = slices.DeleteFunc(s.hooks, func(x *Hooks) bool { return x == h })
}

func (s *Surface) HasBuffer() bool {
	return s.current.buffer != nil || s.pending.set&fieldBuffer != 0 && s.pending.buffer != nil
}

func (s *Surface) PendingBuffer() (Buffer, bool) {
	return s.pending.buffer, s.pending.set&fieldBuffer != 0
}

type Hold struct {
	s    *Surface
	done bool
}

func (h *Hold) Release() error {
	if h.done {
		return nil
	}
	h.done = true
	return h.s.Retry()
}

func (s *Surface) Retry() error {
	if err := s.drain(false); err != nil {
		return s.client.Fail(err)
	}
	return nil
}

func From(r *sw.WlSurface) (*Surface, error) {
	h, ok := r.Handler().(surfaceRequests)
	if !ok {
		return nil, fmt.Errorf("surface: wl_surface@%d has handler %T", r.Base().ID(), r.Handler())
	}
	return h.s, nil
}

func (s *Surface) Resource() *sw.WlSurface {
	return s.resource
}

func (s *Surface) Client() *server.Client {
	return s.client
}

func (s *Surface) Buffer() Buffer {
	return s.current.buffer
}

func (s *Surface) Scale() int32 {
	return s.current.scale
}

func (s *Surface) Transform() render.Transform {
	return s.current.transform
}

func (s *Surface) geometry(over ...*state) geometry {
	g := geometry{
		buffer: s.current.buffer, scale: s.current.scale, transform: s.current.transform,
		source: s.current.source, hasSource: s.current.hasSource, dest: s.current.dest, hasDest: s.current.hasDest,
	}
	for _, st := range over {
		if st.set&fieldBuffer != 0 {
			g.buffer = st.buffer
		}
		if st.set&fieldScale != 0 {
			g.scale = st.scale
		}
		if st.set&fieldTransform != 0 {
			g.transform = st.transform
		}
		if st.set&fieldSource != 0 {
			g.source, g.hasSource = st.source, st.hasSource
		}
		if st.set&fieldDest != 0 {
			g.dest, g.hasDest = st.dest, st.hasDest
		}
	}
	return g
}

func (s *Surface) Size() image.Point {
	return s.geometry().size()
}

func (s *Surface) BufferSource() render.FRect {
	return s.geometry().bufferSource()
}

func (s *Surface) Damage() region.Region {
	return s.damage
}

func (s *Surface) BufferDamage() region.Region {
	return s.bufferDamage
}

func (s *Surface) Attached() bool {
	return s.attached
}

func (s *Surface) Offset() image.Point {
	return s.offset
}

func (s *Surface) Opaque() region.Region {
	return s.current.opaque
}

func (s *Surface) AcceptsInput(p image.Point) bool {
	if !p.In(image.Rectangle{Max: s.Size()}) {
		return false
	}
	return s.current.inputInfinite || s.current.input.Contains(p)
}

func (s *Surface) Input() region.Region {
	bounds := image.Rectangle{Max: s.Size()}
	if s.current.inputInfinite {
		return region.Rect(bounds)
	}
	return s.current.input.Intersect(bounds)
}

func (s *Surface) Stack() iter.Seq[*Surface] {
	return slices.Values(s.order)
}

func (s *Surface) StackTopDown() iter.Seq[*Surface] {
	return func(yield func(*Surface) bool) {
		for _, member := range slices.Backward(s.order) {
			if !yield(member) {
				return
			}
		}
	}
}

func (s *Surface) Subsurface() *Subsurface {
	return s.sub
}

func (s *Surface) Role() Role {
	return s.role
}

func (s *Surface) SetRole(r Role) error {
	if s.roleName != "" && s.roleName != r.RoleName() {
		return fmt.Errorf("wl_surface@%d already has the %s role", s.resource.Base().ID(), s.roleName)
	}
	if s.role != nil {
		return fmt.Errorf("wl_surface@%d already has a live %s role object", s.resource.Base().ID(), s.roleName)
	}
	s.role, s.roleName = r, r.RoleName()
	return nil
}

func (s *Surface) ClearRole(r Role) {
	if s.role == r {
		s.role = nil
	}
}

func (s *Surface) Enter(outputs []*sw.WlOutput) error {
	for _, o := range outputs {
		if err := s.resource.SendEnter(o); err != nil {
			return err
		}
	}
	return nil
}

func (s *Surface) Leave(outputs []*sw.WlOutput) error {
	for _, o := range outputs {
		if err := s.resource.SendLeave(o); err != nil {
			return err
		}
	}
	return nil
}

func (s *Surface) SetPreferredScale(scale120 uint32) error {
	if scale120 == 0 || scale120 == s.preferred || s.destroyed {
		return nil
	}
	s.preferred = scale120
	if s.fractional != nil {
		if err := s.fractional.resource.SendPreferredScale(scale120); err != nil {
			return err
		}
	}
	if s.resource.Base().Version() >= 6 {
		return s.resource.SendPreferredBufferScale(int32((scale120 + 119) / 120)) //nolint:gosec // at most 2^32/120
	}
	return nil
}

func (s *Surface) FrameDone(msec uint32) error {
	frames := s.current.frames
	s.current.frames = nil
	for _, cb := range frames {
		if err := cb.SendDone(msec); err != nil {
			return err
		}
	}
	return nil
}

func (s *Surface) synchronized() bool {
	return s.sub != nil && s.sub.parent != nil && (s.sub.sync || s.sub.parent.synchronized())
}

func (s *Surface) protocolError(code sw.WlSurfaceError, format string, args ...any) error {
	return s.resource.ProtocolError(code, fmt.Sprintf(format, args...))
}

func (s *Surface) unapplied() []*state {
	states := make([]*state, 0, len(s.queue)+2)
	for _, u := range s.queue {
		states = append(states, &u.state)
	}
	if s.cache != nil {
		states = append(states, &s.cache.state)
	}
	return append(states, &s.pending)
}

func (s *Surface) checkSize() error {
	g := s.geometry(s.unapplied()...)
	err := g.validate()
	switch {
	case errors.Is(err, errInvalidSize):
		return s.protocolError(sw.WlSurfaceErrorInvalidSize, "buffer %v is not a multiple of scale %d", g.buffer.Size(), g.scale)
	case errors.Is(err, errBadSize):
		return s.viewport.badSize(g.source)
	case errors.Is(err, errOutOfBuffer):
		return s.viewport.outOfBuffer(g.source, g.unscaled())
	}
	return err
}

func (s *Surface) commit() error {
	if err := s.checkSize(); err != nil {
		return err
	}
	u := &Update{s: s}
	for _, h := range slices.Clone(s.hooks) {
		if h.Commit != nil {
			if err := h.Commit(u); err != nil {
				return err
			}
		}
	}
	u.state, s.pending = s.pending, state{}
	if s.synchronized() {
		if s.cache == nil {
			s.cache = u
		} else {
			s.cache.absorb(u)
		}
		return nil
	}
	s.queue = append(s.queue, u)
	return s.drain(false)
}

func (s *Surface) flush() error {
	if s.cache != nil {
		s.queue = append(s.queue, s.cache)
		s.cache = nil
	}
	return s.drain(true)
}

func (s *Surface) drain(parentApplied bool) error {
	for len(s.queue) > 0 && !s.destroyed && (parentApplied || !s.synchronized()) && s.queue[0].ready() {
		u := s.queue[0]
		s.queue[0] = nil
		s.queue = s.queue[1:]
		if err := s.apply(u); err != nil {
			return err
		}
	}
	return nil
}

func (s *Surface) apply(u *Update) error {
	st := &u.state
	s.damage, s.bufferDamage = s.mapDamage(st)
	s.attached = st.set&fieldBuffer != 0
	s.offset = image.Point{}
	if st.set&fieldOffset != 0 {
		s.offset = st.offset
	}
	st.damage, st.bufferDamage, st.offset = region.Region{}, region.Region{}, image.Point{}
	superseded := s.current.feedbacks
	s.current.feedbacks = nil
	if err := s.client.Sent(discard(superseded)); err != nil {
		return err
	}
	s.current.merge(st)
	s.current.set = 0
	s.order = slices.Clone(s.pendingOrder)
	for _, child := range s.order {
		if child != s {
			child.sub.position = child.sub.pendingPosition
		}
	}
	if s.role != nil {
		if err := s.role.Committed(s); err != nil {
			return err
		}
	}
	if err := s.manager.listener.Committed(s); err != nil {
		return err
	}
	for _, f := range u.applied {
		if err := f(); err != nil {
			return err
		}
	}
	for _, h := range slices.Clone(s.hooks) {
		if h.Applied != nil {
			if err := h.Applied(); err != nil {
				return err
			}
		}
	}
	for _, child := range slices.Clone(s.order) {
		if child != s && child.synchronized() {
			if err := child.flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Surface) mapDamage(st *state) (surface, buffer region.Region) {
	g := s.geometry(st)
	if g.buffer == nil {
		return st.damage, region.Region{}
	}
	bounds := image.Rectangle{Max: g.buffer.Size()}
	return st.damage.Union(st.bufferDamage.Map(g.toSurface)), st.bufferDamage.Intersect(bounds).Union(st.damage.Map(g.toBuffer))
}

func (s *Surface) destroy() error {
	s.destroyed = true
	var errs []error
	var feedbacks []*spt.WpPresentationFeedback
	for _, st := range append(s.unapplied(), &s.current) {
		for _, cb := range st.frames {
			errs = append(errs, s.client.Remove(cb.Base().ID()))
		}
		feedbacks = append(feedbacks, st.feedbacks...)
		st.frames, st.feedbacks = nil, nil
	}
	s.cache, s.queue = nil, nil
	feedbacks = append(feedbacks, s.latched...)
	s.latched = nil
	errs = append(errs, s.client.Sent(discard(feedbacks)))
	if s.sub != nil {
		s.sub.detach()
	}
	for _, child := range s.pendingOrder {
		if child != s {
			child.sub.parent = nil
		}
	}
	for _, child := range s.order {
		if child != s {
			child.sub.parent = nil
		}
	}
	s.order, s.pendingOrder = nil, nil
	errs = append(errs, s.manager.listener.Destroyed(s))
	for _, h := range slices.Clone(s.hooks) {
		if h.Destroyed != nil {
			errs = append(errs, h.Destroyed())
		}
	}
	return errors.Join(errs...)
}

type surfaceRequests struct{ s *Surface }

func (r surfaceRequests) Destroy(*sw.WlSurface) error {
	return nil
}

func (r surfaceRequests) Attach(obj *sw.WlSurface, buffer *sw.WlBuffer, x, y int32) error {
	s := r.s
	if (x != 0 || y != 0) && obj.Base().Version() >= 5 {
		return s.protocolError(sw.WlSurfaceErrorInvalidOffset, "attach offset (%d, %d) must be 0 since version 5; use wl_surface.offset", x, y)
	}
	s.pending.set |= fieldBuffer
	s.pending.buffer = nil
	if buffer != nil {
		b, ok := buffer.Handler().(Buffer)
		if !ok {
			return fmt.Errorf("surface: wl_buffer@%d has handler %T, which is not a surface.Buffer", buffer.Base().ID(), buffer.Handler())
		}
		s.pending.buffer = b
	}
	if x != 0 || y != 0 {
		s.pending.set |= fieldOffset
		s.pending.offset = image.Pt(int(x), int(y))
	}
	return nil
}

func (r surfaceRequests) Offset(_ *sw.WlSurface, x, y int32) error {
	r.s.pending.set |= fieldOffset
	r.s.pending.offset = image.Pt(int(x), int(y))
	return nil
}

const maxDamageRects = 32

func addDamage(g region.Region, r image.Rectangle) region.Region {
	g = g.Add(r)
	if len(g.Rects()) > maxDamageRects {
		return region.Rect(g.Bounds())
	}
	return g
}

func (r surfaceRequests) Damage(_ *sw.WlSurface, x, y, w, h int32) error {
	r.s.pending.damage = addDamage(r.s.pending.damage, rect(x, y, w, h))
	return nil
}

func (r surfaceRequests) DamageBuffer(_ *sw.WlSurface, x, y, w, h int32) error {
	r.s.pending.bufferDamage = addDamage(r.s.pending.bufferDamage, rect(x, y, w, h))
	return nil
}

func (r surfaceRequests) Frame(_ *sw.WlSurface, cb *sw.WlCallback) error {
	r.s.pending.frames = append(r.s.pending.frames, cb)
	return nil
}

func (r surfaceRequests) SetOpaqueRegion(_ *sw.WlSurface, g *sw.WlRegion) error {
	r.s.pending.set |= fieldOpaque
	r.s.pending.opaque = region.Region{}
	if g == nil {
		return nil
	}
	v, err := RegionValue(g)
	r.s.pending.opaque = v
	return err
}

func (r surfaceRequests) SetInputRegion(_ *sw.WlSurface, g *sw.WlRegion) error {
	r.s.pending.set |= fieldInput
	r.s.pending.input, r.s.pending.inputInfinite = region.Region{}, g == nil
	if g == nil {
		return nil
	}
	v, err := RegionValue(g)
	r.s.pending.input = v
	return err
}

func (r surfaceRequests) Commit(*sw.WlSurface) error {
	return r.s.commit()
}

func (r surfaceRequests) SetBufferTransform(_ *sw.WlSurface, t sw.WlOutputTransform) error {
	if t > sw.WlOutputTransformFlipped270 {
		return r.s.protocolError(sw.WlSurfaceErrorInvalidTransform, "invalid buffer transform %d", t)
	}
	r.s.pending.set |= fieldTransform
	r.s.pending.transform = render.Transform(t)
	return nil
}

func (r surfaceRequests) SetBufferScale(_ *sw.WlSurface, scale int32) error {
	if scale <= 0 {
		return r.s.protocolError(sw.WlSurfaceErrorInvalidScale, "invalid buffer scale %d", scale)
	}
	r.s.pending.set |= fieldScale
	r.s.pending.scale = scale
	return nil
}

func (r surfaceRequests) GetRelease(*sw.WlSurface, *sw.WlCallback) error {
	return errors.New("surface: get_release needs wl_surface version 7, which is not advertised")
}
