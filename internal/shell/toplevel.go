package shell

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"slices"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	sxd "github.com/stubbedev/tilde/internal/protocol/server/xdgdecoration"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/surface"
)

type ToplevelState = sxdg.XdgToplevelState

type State struct {
	Size   image.Point
	Bounds image.Point
	States []ToplevelState
}

func (s State) Has(st ToplevelState) bool {
	return slices.Contains(s.States, st)
}

type Toplevel struct {
	xs         *XdgSurface
	resource   *sxdg.XdgToplevel
	title      string
	appID      string
	parent     *Toplevel
	min, max   image.Point
	pendingMin image.Point
	pendingMax image.Point
	minSet     bool
	maxSet     bool
	requested  State
	current    State
	configures configures[State]
	decoration *sxd.ZxdgToplevelDecorationV1
	destroyed  bool
}

func (t *Toplevel) Surface() *surface.Surface {
	return t.xs.surface
}

func (t *Toplevel) Geometry() image.Rectangle {
	return t.xs.geometry
}

func (t *Toplevel) Mapped() bool {
	return t.xs.mapped
}

func (t *Toplevel) Title() string {
	return t.title
}

func (t *Toplevel) AppID() string {
	return t.appID
}

func (t *Toplevel) Parent() *Toplevel {
	return t.parent
}

func (t *Toplevel) MinSize() image.Point {
	return t.min
}

func (t *Toplevel) MaxSize() image.Point {
	return t.max
}

func (t *Toplevel) Current() State {
	return t.current
}

func (t *Toplevel) Requested() State {
	return t.requested
}

func (t *Toplevel) Configure(s State) (uint32, error) {
	if t.destroyed {
		return 0, errors.New("shell: configure of a destroyed xdg_toplevel")
	}
	width, height, err := wireSize(s.Size)
	if err != nil {
		return 0, err
	}
	boundsW, boundsH, err := wireSize(s.Bounds)
	if err != nil {
		return 0, err
	}
	c := t.xs.base.client
	t.requested = s
	if t.resource.Base().Version() >= 4 && s.Bounds != (image.Point{}) {
		if err := c.Sent(t.resource.SendConfigureBounds(boundsW, boundsH)); err != nil {
			return 0, err
		}
	}
	states := make([]byte, 0, 4*len(s.States))
	for _, st := range s.States {
		if (st >= sxdg.XdgToplevelStateTiledLeft && st <= sxdg.XdgToplevelStateTiledBottom && t.resource.Base().Version() < 2) ||
			(st == sxdg.XdgToplevelStateSuspended && t.resource.Base().Version() < 6) ||
			(st >= sxdg.XdgToplevelStateConstrainedLeft && st <= sxdg.XdgToplevelStateConstrainedBottom && t.resource.Base().Version() < 7) {
			continue
		}
		states = binary.NativeEndian.AppendUint32(states, uint32(st))
	}
	if err := c.Sent(t.resource.SendConfigure(width, height, states)); err != nil {
		return 0, err
	}
	serial, err := t.xs.sendConfigure()
	if err != nil {
		return 0, err
	}
	t.configures.push(serial, s)
	return serial, nil
}

func wirePoint(p image.Point) (int32, int32, error) {
	if p.X < math.MinInt32 || p.Y < math.MinInt32 || p.X > math.MaxInt32 || p.Y > math.MaxInt32 {
		return 0, 0, fmt.Errorf("shell: point %v does not fit the protocol", p)
	}
	return int32(p.X), int32(p.Y), nil
}

func wireSize(p image.Point) (int32, int32, error) {
	if p.X < 0 || p.Y < 0 || p.X > math.MaxInt32 || p.Y > math.MaxInt32 {
		return 0, 0, fmt.Errorf("shell: size %v does not fit the protocol", p)
	}
	return int32(p.X), int32(p.Y), nil
}

func (t *Toplevel) SendClose() error {
	return t.xs.base.client.Sent(t.resource.SendClose())
}

func (t *Toplevel) Ping() error {
	return t.xs.base.Ping()
}

func (t *Toplevel) applyPending() error {
	minSize, maxSize := t.min, t.max
	if t.minSet {
		minSize = t.pendingMin
	}
	if t.maxSet {
		maxSize = t.pendingMax
	}
	if maxSize.X > 0 && minSize.X > maxSize.X || maxSize.Y > 0 && minSize.Y > maxSize.Y {
		return t.resource.ProtocolError(sxdg.XdgToplevelErrorInvalidSize, fmt.Sprintf("minimum size %v exceeds maximum size %v", minSize, maxSize))
	}
	t.min, t.max, t.minSet, t.maxSet = minSize, maxSize, false, false
	if s, ok := t.configures.take(); ok {
		t.current = s
	}
	return nil
}

func (t *Toplevel) ack(serial uint32) bool {
	return t.configures.ack(serial)
}

func (t *Toplevel) initialCommit() error {
	if err := t.xs.base.shell.listener.ToplevelInitialCommit(t); err != nil {
		return err
	}
	if len(t.configures.pending) == 0 {
		_, err := t.Configure(t.requested)
		return err
	}
	return nil
}

func (t *Toplevel) mapped() error {
	return t.xs.base.shell.listener.ToplevelMapped(t)
}

func (t *Toplevel) unmapped() error {
	t.configures.reset()
	return t.xs.base.shell.listener.ToplevelUnmapped(t)
}

func (t *Toplevel) committed() error {
	return t.xs.base.shell.listener.ToplevelCommitted(t)
}

func (t *Toplevel) inert() bool {
	return false
}

func (t *Toplevel) Popups() []*Popup {
	return t.xs.Popups()
}

func (t *Toplevel) destroy() error {
	t.destroyed = true
	xs := t.xs
	var errs []error
	if xs.mapped {
		errs = append(errs, xs.unmap())
	}
	errs = append(errs, xs.dismissPopups())
	xs.role, xs.configured, xs.initialSent = nil, false, false
	for _, other := range xs.base.surfaces {
		if o, ok := other.role.(*Toplevel); ok && o.parent == t {
			o.parent = t.parent
		}
	}
	return errors.Join(append(errs, xs.base.shell.toplevelGone(t))...)
}

func (t *Toplevel) Destroy(*sxdg.XdgToplevel) error {
	if t.decoration != nil {
		return t.decoration.ProtocolError(sxd.ZxdgToplevelDecorationV1ErrorOrphaned, "the xdg_toplevel was destroyed before its decoration object")
	}
	return nil
}

func (t *Toplevel) SetParent(obj *sxdg.XdgToplevel, parent *sxdg.XdgToplevel) error {
	if parent == nil {
		t.parent = nil
		return t.xs.base.shell.listener.ToplevelChanged(t, ChangedParent)
	}
	p, ok := parent.Handler().(*Toplevel)
	if !ok {
		return fmt.Errorf("shell: xdg_toplevel@%d has handler %T", parent.Base().ID(), parent.Handler())
	}
	for a := p; a != nil; a = a.parent {
		if a == t {
			return obj.ProtocolError(sxdg.XdgToplevelErrorInvalidParent, "the parent would make the toplevel its own ancestor")
		}
	}
	t.parent = p
	return t.xs.base.shell.listener.ToplevelChanged(t, ChangedParent)
}

func (t *Toplevel) SetTitle(_ *sxdg.XdgToplevel, title string) error {
	if title == t.title {
		return nil
	}
	t.title = title
	return t.xs.base.shell.listener.ToplevelChanged(t, ChangedTitle)
}

func (t *Toplevel) SetAppID(_ *sxdg.XdgToplevel, appID string) error {
	if appID == t.appID {
		return nil
	}
	t.appID = appID
	return t.xs.base.shell.listener.ToplevelChanged(t, ChangedAppID)
}

func (t *Toplevel) request(r Request) error {
	return t.xs.base.shell.listener.ToplevelRequest(t, r)
}

func (t *Toplevel) ShowWindowMenu(_ *sxdg.XdgToplevel, _ *sw.WlSeat, serial uint32, x, y int32) error {
	return t.request(ShowWindowMenu{Serial: serial, At: image.Pt(int(x), int(y))})
}

func (t *Toplevel) Move(_ *sxdg.XdgToplevel, _ *sw.WlSeat, serial uint32) error {
	return t.request(Move{Serial: serial})
}

func (t *Toplevel) Resize(obj *sxdg.XdgToplevel, _ *sw.WlSeat, serial uint32, edges sxdg.XdgToplevelResizeEdge) error {
	switch edges {
	case sxdg.XdgToplevelResizeEdgeNone, sxdg.XdgToplevelResizeEdgeTop, sxdg.XdgToplevelResizeEdgeBottom, sxdg.XdgToplevelResizeEdgeLeft,
		sxdg.XdgToplevelResizeEdgeTopLeft, sxdg.XdgToplevelResizeEdgeBottomLeft, sxdg.XdgToplevelResizeEdgeRight,
		sxdg.XdgToplevelResizeEdgeTopRight, sxdg.XdgToplevelResizeEdgeBottomRight:
		return t.request(Resize{Serial: serial, Edges: edges})
	}
	return obj.ProtocolError(sxdg.XdgToplevelErrorInvalidResizeEdge, fmt.Sprintf("invalid resize edge %d", edges))
}

func sizeArg(obj *sxdg.XdgToplevel, w, h int32) (image.Point, error) {
	if w < 0 || h < 0 {
		return image.Point{}, obj.ProtocolError(sxdg.XdgToplevelErrorInvalidSize, fmt.Sprintf("size %dx%d is negative", w, h))
	}
	return image.Pt(int(w), int(h)), nil
}

func (t *Toplevel) SetMaxSize(obj *sxdg.XdgToplevel, w, h int32) error {
	p, err := sizeArg(obj, w, h)
	t.pendingMax, t.maxSet = p, err == nil
	return err
}

func (t *Toplevel) SetMinSize(obj *sxdg.XdgToplevel, w, h int32) error {
	p, err := sizeArg(obj, w, h)
	t.pendingMin, t.minSet = p, err == nil
	return err
}

func (t *Toplevel) SetMaximized(*sxdg.XdgToplevel) error {
	return t.request(SetMaximized{On: true})
}

func (t *Toplevel) UnsetMaximized(*sxdg.XdgToplevel) error {
	return t.request(SetMaximized{On: false})
}

func (t *Toplevel) SetFullscreen(*sxdg.XdgToplevel, *sw.WlOutput) error {
	return t.request(SetFullscreen{On: true})
}

func (t *Toplevel) UnsetFullscreen(*sxdg.XdgToplevel) error {
	return t.request(SetFullscreen{On: false})
}

func (t *Toplevel) SetMinimized(*sxdg.XdgToplevel) error {
	return t.request(SetMinimized{})
}
