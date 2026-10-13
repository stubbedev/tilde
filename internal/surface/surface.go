// Package surface implements wl_compositor, wl_surface, wl_region and
// wl_subcompositor: double-buffered surface state, subsurface trees and roles.
package surface

import (
	"errors"
	"fmt"
	"image"
	"slices"

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
	Destroyed(s *Surface)
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
	r.c.OnDestroy(res, s.destroy)
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

func regionValue(r *sw.WlRegion) (region.Region, error) {
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
	frames        []*sw.WlCallback
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
	dst.set |= src.set
	dst.damage = dst.damage.Union(src.damage)
	dst.bufferDamage = dst.bufferDamage.Union(src.bufferDamage)
	dst.frames = append(dst.frames, src.frames...)
	*src = state{}
}

type Surface struct {
	manager      *Manager
	client       *server.Client
	resource     *sw.WlSurface
	pending      state
	cache        state
	cached       bool
	current      state
	damage       region.Region
	offset       image.Point
	role         Role
	roleName     string
	sub          *Subsurface
	order        []*Surface
	pendingOrder []*Surface
	destroyed    bool
}

func surfaceOf(r *sw.WlSurface) (*Surface, error) {
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

func (s *Surface) Size() image.Point {
	if s.current.buffer == nil {
		return image.Point{}
	}
	size := s.current.transform.Size(s.current.buffer.Size())
	return image.Pt(size.X/int(s.current.scale), size.Y/int(s.current.scale))
}

func (s *Surface) Damage() region.Region {
	return s.damage
}

func (s *Surface) Offset() image.Point {
	return s.offset
}

func (s *Surface) Opaque() region.Region {
	return s.current.opaque
}

func (s *Surface) Input() region.Region {
	bounds := image.Rectangle{Max: s.Size()}
	if s.current.inputInfinite {
		return region.Rect(bounds)
	}
	return s.current.input.Intersect(bounds)
}

func (s *Surface) Stack() []*Surface {
	return slices.Clone(s.order)
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

func (s *Surface) checkSize() error {
	buffer, scale, transform := s.current.buffer, s.current.scale, s.current.transform
	for _, st := range []*state{&s.cache, &s.pending} {
		if st.set&fieldBuffer != 0 {
			buffer = st.buffer
		}
		if st.set&fieldScale != 0 {
			scale = st.scale
		}
		if st.set&fieldTransform != 0 {
			transform = st.transform
		}
	}
	if buffer == nil {
		return nil
	}
	size := transform.Size(buffer.Size())
	if size.X%int(scale) != 0 || size.Y%int(scale) != 0 {
		return s.protocolError(sw.WlSurfaceErrorInvalidSize, "buffer %v is not a multiple of scale %d", buffer.Size(), scale)
	}
	return nil
}

func (s *Surface) commit() error {
	if err := s.checkSize(); err != nil {
		return err
	}
	s.cache.merge(&s.pending)
	s.cached = true
	if s.synchronized() {
		return nil
	}
	return s.applyCache()
}

func (s *Surface) applyCache() error {
	if !s.cached {
		return nil
	}
	s.cached = false
	st := &s.cache
	s.damage = s.surfaceDamage(st)
	s.offset = image.Point{}
	if st.set&fieldOffset != 0 {
		s.offset = st.offset
	}
	st.damage, st.bufferDamage, st.offset = region.Region{}, region.Region{}, image.Point{}
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
	for _, child := range s.order {
		if child != s && child.synchronized() {
			if err := child.applyCache(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Surface) surfaceDamage(st *state) region.Region {
	damage := st.damage
	buffer, scale, transform := s.current.buffer, s.current.scale, s.current.transform
	if st.set&fieldBuffer != 0 {
		buffer = st.buffer
	}
	if st.set&fieldScale != 0 {
		scale = st.scale
	}
	if st.set&fieldTransform != 0 {
		transform = st.transform
	}
	if buffer == nil || st.bufferDamage.Empty() {
		return damage
	}
	size := buffer.Size()
	return damage.Union(st.bufferDamage.Map(func(r image.Rectangle) image.Rectangle {
		r = transform.Invert().Rect(r.Intersect(image.Rectangle{Max: size}), size)
		return image.Rect(r.Min.X/int(scale), r.Min.Y/int(scale), ceilDiv(r.Max.X, int(scale)), ceilDiv(r.Max.Y, int(scale)))
	}))
}

func ceilDiv(a, b int) int {
	return (a + b - 1) / b
}

func (s *Surface) destroy() {
	s.destroyed = true
	for _, cb := range append(append(s.pending.frames, s.cache.frames...), s.current.frames...) {
		_ = s.client.Remove(cb.Base().ID())
	}
	s.pending.frames, s.cache.frames, s.current.frames = nil, nil, nil
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
	s.manager.listener.Destroyed(s)
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

func (r surfaceRequests) Damage(_ *sw.WlSurface, x, y, w, h int32) error {
	r.s.pending.damage = r.s.pending.damage.Add(rect(x, y, w, h))
	return nil
}

func (r surfaceRequests) DamageBuffer(_ *sw.WlSurface, x, y, w, h int32) error {
	r.s.pending.bufferDamage = r.s.pending.bufferDamage.Add(rect(x, y, w, h))
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
	v, err := regionValue(g)
	r.s.pending.opaque = v
	return err
}

func (r surfaceRequests) SetInputRegion(_ *sw.WlSurface, g *sw.WlRegion) error {
	r.s.pending.set |= fieldInput
	r.s.pending.input, r.s.pending.inputInfinite = region.Region{}, g == nil
	if g == nil {
		return nil
	}
	v, err := regionValue(g)
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
