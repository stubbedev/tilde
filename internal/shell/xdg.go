// Package shell implements xdg-shell: the window roles clients request and
// the configure handshake the window manager drives.
package shell

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/tilde/internal/loop"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	wmBaseVersion = 6
	xdgRole       = "xdg_surface"
	PingTimeout   = 5 * time.Second
)

type Request interface {
	isRequest()
}

type (
	SetFullscreen struct{ On bool }
	SetMaximized  struct{ On bool }
	SetMinimized  struct{}
	Move          struct{ Serial uint32 }
	Resize        struct {
		Serial uint32
		Edges  sxdg.XdgToplevelResizeEdge
	}
	ShowWindowMenu struct {
		Serial uint32
		At     image.Point
	}
)

func (SetFullscreen) isRequest()  {}
func (SetMaximized) isRequest()   {}
func (SetMinimized) isRequest()   {}
func (Move) isRequest()           {}
func (Resize) isRequest()         {}
func (ShowWindowMenu) isRequest() {}

type Change uint8

const (
	ChangedTitle Change = iota + 1
	ChangedAppID
	ChangedParent
)

type Listener interface {
	ToplevelInitialCommit(t *Toplevel) error
	ToplevelMapped(t *Toplevel) error
	ToplevelCommitted(t *Toplevel) error
	ToplevelUnmapped(t *Toplevel) error
	ToplevelChanged(t *Toplevel, c Change) error
	ToplevelRequest(t *Toplevel, r Request) error
	ClientResponsive(c *server.Client, responsive bool) error
	PopupConstraint(root *Toplevel) image.Rectangle
	PopupMapped(p *Popup) error
	PopupUnmapped(p *Popup) error
	PressSerial(c *server.Client, serial uint32) bool
	GrabChanged() error
}

type Shell struct {
	display     *server.Display
	loop        *loop.Loop
	listener    Listener
	subRoles    map[*surface.Surface]string
	pingTimeout time.Duration
	grab        []*Popup
}

func New(d *server.Display, l *loop.Loop, listener Listener) (*Shell, error) {
	sh := &Shell{display: d, loop: l, listener: listener, subRoles: map[*surface.Surface]string{}, pingTimeout: PingTimeout}
	if _, err := d.AddGlobal(sxdg.XdgWmBaseInterface, wmBaseVersion, sh.bind); err != nil {
		return nil, err
	}
	return sh, nil
}

type wmBase struct {
	shell      *Shell
	client     *server.Client
	resource   *sxdg.XdgWmBase
	surfaces   []*XdgSurface
	ping       uint32
	timer      *loop.Timer
	responsive bool
}

func (sh *Shell) bind(c *server.Client, id wire.ObjectID, version uint32) error {
	b := &wmBase{shell: sh, client: c, responsive: true}
	res, err := sxdg.NewXdgWmBase(c, id, version, b)
	if err != nil {
		return err
	}
	b.resource = res
	c.OnDestroy(res, func() error {
		if b.timer == nil {
			return nil
		}
		return b.timer.Remove()
	})
	return nil
}

func (b *wmBase) Destroy(obj *sxdg.XdgWmBase) error {
	if len(b.surfaces) > 0 {
		return obj.ProtocolError(sxdg.XdgWmBaseErrorDefunctSurfaces, fmt.Sprintf("%d xdg_surfaces still exist", len(b.surfaces)))
	}
	return nil
}

func (b *wmBase) Pong(_ *sxdg.XdgWmBase, serial uint32) error {
	if serial != b.ping || b.ping == 0 {
		return nil
	}
	b.ping = 0
	if err := b.timer.Disarm(); err != nil {
		return err
	}
	if !b.responsive {
		b.responsive = true
		return b.shell.listener.ClientResponsive(b.client, true)
	}
	return nil
}

func (b *wmBase) Ping() error {
	if b.ping != 0 {
		return nil
	}
	if b.timer == nil {
		var err error
		if b.timer, err = b.shell.loop.AddTimer(b.timeout); err != nil {
			return err
		}
	}
	b.ping = b.shell.display.NextSerial()
	if err := b.client.Sent(b.resource.SendPing(b.ping)); err != nil {
		return err
	}
	return b.timer.Arm(b.shell.pingTimeout)
}

func (b *wmBase) timeout() error {
	if b.ping == 0 || !b.responsive {
		return nil
	}
	b.responsive = false
	return b.shell.listener.ClientResponsive(b.client, false)
}

func (b *wmBase) GetXdgSurface(obj *sxdg.XdgWmBase, res *sxdg.XdgSurface, wl *sw.WlSurface) (sxdg.XdgSurfaceHandler, error) {
	surf, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	if surf.Buffer() != nil {
		return nil, obj.ProtocolError(sxdg.XdgWmBaseErrorInvalidSurfaceState, "the wl_surface already has a buffer")
	}
	xs := &XdgSurface{base: b, resource: res, surface: surf}
	if err := surf.SetRole(xs); err != nil {
		return nil, obj.ProtocolError(sxdg.XdgWmBaseErrorRole, err.Error())
	}
	b.surfaces = append(b.surfaces, xs)
	forget := func() error {
		b.surfaces = slices.DeleteFunc(b.surfaces, func(x *XdgSurface) bool { return x == xs })
		surf.ClearRole(xs)
		return nil
	}
	b.client.OnDestroy(res, forget)
	b.client.OnDestroy(wl, func() error {
		delete(b.shell.subRoles, surf)
		return nil
	})
	return xs, nil
}

type queued[T any] struct {
	serial uint32
	state  T
}

type configures[T any] struct {
	pending []queued[T]
	acked   *T
}

func (q *configures[T]) push(serial uint32, state T) {
	q.pending = append(q.pending, queued[T]{serial, state})
}

func (q *configures[T]) ack(serial uint32) bool {
	i := slices.IndexFunc(q.pending, func(c queued[T]) bool { return c.serial == serial })
	if i < 0 {
		return false
	}
	state := q.pending[i].state
	q.acked = &state
	q.pending = slices.Delete(q.pending, 0, i+1)
	return true
}

func (q *configures[T]) take() (T, bool) {
	if q.acked == nil {
		var zero T
		return zero, false
	}
	state := *q.acked
	q.acked = nil
	return state, true
}

func (q *configures[T]) reset() {
	q.pending, q.acked = nil, nil
}

type roleObject interface {
	ack(serial uint32) bool
	applyPending() error
	initialCommit() error
	mapped() error
	unmapped() error
	committed() error
	inert() bool
}

type XdgSurface struct {
	base            *wmBase
	resource        *sxdg.XdgSurface
	surface         *surface.Surface
	role            roleObject
	popups          []*Popup
	configured      bool
	initialSent     bool
	mapped          bool
	geometry        image.Rectangle
	pendingGeometry image.Rectangle
	geometrySet     bool
}

func (xs *XdgSurface) RoleName() string {
	return xdgRole
}

func (xs *XdgSurface) Destroy(obj *sxdg.XdgSurface) error {
	if xs.role != nil {
		return obj.ProtocolError(sxdg.XdgSurfaceErrorDefunctRoleObject, "the role object must be destroyed first")
	}
	return nil
}

func (xs *XdgSurface) sendConfigure() (uint32, error) {
	serial := xs.base.shell.display.NextSerial()
	return serial, xs.base.client.Sent(xs.resource.SendConfigure(serial))
}

func (xs *XdgSurface) dismissPopups() error {
	var errs []error
	for _, p := range slices.Backward(xs.popups) {
		errs = append(errs, p.dismiss())
	}
	return errors.Join(errs...)
}

func (xs *XdgSurface) Popups() []*Popup {
	return slices.Clone(xs.popups)
}

func (xs *XdgSurface) setSubRole(obj *sxdg.XdgSurface, role string) error {
	sh := xs.base.shell
	if xs.role != nil {
		return obj.ProtocolError(sxdg.XdgSurfaceErrorAlreadyConstructed, "the xdg_surface already has a role object")
	}
	if prev, ok := sh.subRoles[xs.surface]; ok && prev != role {
		return xs.base.resource.ProtocolError(sxdg.XdgWmBaseErrorRole, fmt.Sprintf("the wl_surface was an %s and cannot become an %s", prev, role))
	}
	sh.subRoles[xs.surface] = role
	return nil
}

func (xs *XdgSurface) GetToplevel(obj *sxdg.XdgSurface, res *sxdg.XdgToplevel) (sxdg.XdgToplevelHandler, error) {
	if err := xs.setSubRole(obj, "xdg_toplevel"); err != nil {
		return nil, err
	}
	t := &Toplevel{xs: xs, resource: res}
	xs.role = t
	xs.base.client.OnDestroy(res, t.destroy)
	if res.Base().Version() >= 5 {
		caps := binary.NativeEndian.AppendUint32(nil, uint32(sxdg.XdgToplevelWmCapabilitiesFullscreen))
		caps = binary.NativeEndian.AppendUint32(caps, uint32(sxdg.XdgToplevelWmCapabilitiesMaximize))
		if err := res.SendWmCapabilities(caps); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (xs *XdgSurface) SetWindowGeometry(obj *sxdg.XdgSurface, x, y, w, h int32) error {
	if w <= 0 || h <= 0 {
		return obj.ProtocolError(sxdg.XdgSurfaceErrorInvalidSize, fmt.Sprintf("window geometry %dx%d is not positive", w, h))
	}
	xs.pendingGeometry = image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h))
	xs.geometrySet = true
	return nil
}

func (xs *XdgSurface) AckConfigure(obj *sxdg.XdgSurface, serial uint32) error {
	if xs.role == nil {
		return obj.ProtocolError(sxdg.XdgSurfaceErrorNotConstructed, "the xdg_surface has no role object")
	}
	if !xs.role.ack(serial) {
		return obj.ProtocolError(sxdg.XdgSurfaceErrorInvalidSerial, fmt.Sprintf("serial %d was never sent or is already acked", serial))
	}
	xs.configured = true
	return nil
}

func treeBounds(s *surface.Surface, at image.Point) image.Rectangle {
	b := image.Rectangle{Min: at, Max: at.Add(s.Size())}
	for _, child := range s.Stack() {
		if child != s && child.Subsurface() != nil {
			b = b.Union(treeBounds(child, at.Add(child.Subsurface().Position())))
		}
	}
	return b
}

func (xs *XdgSurface) Geometry() image.Rectangle {
	return xs.geometry
}

func (xs *XdgSurface) Committed(s *surface.Surface) error {
	r := xs.role
	if r == nil {
		return xs.resource.ProtocolError(sxdg.XdgSurfaceErrorNotConstructed, "the xdg_surface has no role object")
	}
	if r.inert() {
		return nil
	}
	if xs.geometrySet {
		xs.geometry = xs.pendingGeometry
	}
	if err := r.applyPending(); err != nil {
		return err
	}
	if s.Buffer() != nil && !xs.configured {
		return xs.resource.ProtocolError(sxdg.XdgSurfaceErrorUnconfiguredBuffer, "a buffer was attached before the first configure was acked")
	}
	if !xs.geometrySet {
		xs.geometry = treeBounds(s, image.Point{})
	}
	switch {
	case !xs.initialSent:
		xs.initialSent = true
		return r.initialCommit()
	case s.Buffer() == nil && xs.mapped:
		return xs.unmap()
	case s.Buffer() != nil && !xs.mapped:
		xs.mapped = true
		return r.mapped()
	case xs.mapped:
		return r.committed()
	}
	return nil
}

func (xs *XdgSurface) unmap() error {
	xs.mapped, xs.configured, xs.initialSent = false, false, false
	return errors.Join(xs.dismissPopups(), xs.role.unmapped())
}
