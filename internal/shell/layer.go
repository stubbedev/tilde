package shell

import (
	"errors"
	"fmt"
	"image"
	"math"
	"math/bits"
	"slices"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	swlr "github.com/stubbedev/tilde/internal/protocol/server/wlrlayershell"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	layerShellVersion = 5
	layerRole         = "zwlr_layer_surface_v1"
)

type (
	Layer         = swlr.ZwlrLayerShellV1Layer
	Edges         = swlr.ZwlrLayerSurfaceV1Anchor
	Interactivity = swlr.ZwlrLayerSurfaceV1KeyboardInteractivity
)

const allEdges = swlr.ZwlrLayerSurfaceV1AnchorTop | swlr.ZwlrLayerSurfaceV1AnchorBottom | swlr.ZwlrLayerSurfaceV1AnchorLeft | swlr.ZwlrLayerSurfaceV1AnchorRight

type Margin struct {
	Top, Right, Bottom, Left int
}

type LayerState struct {
	Layer         Layer
	Size          image.Point
	Anchor        Edges
	ExclusiveZone int
	ExclusiveEdge Edges
	Margin        Margin
	Interactivity Interactivity
}

type LayerSurface struct {
	shell       *Shell
	client      *server.Client
	manager     *swlr.ZwlrLayerShellV1
	resource    *swlr.ZwlrLayerSurfaceV1
	surface     *surface.Surface
	output      *sw.WlOutput
	namespace   string
	pending     LayerState
	current     LayerState
	configures  configures[image.Point]
	sentSize    image.Point
	sent        bool
	configured  bool
	initialSent bool
	mapped      bool
	closed      bool
	destroyed   bool
	popups      []*Popup
}

func (sh *Shell) bindLayerShell(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := swlr.NewZwlrLayerShellV1(c, id, version, layerShell{sh, c})
	return err
}

type layerShell struct {
	sh *Shell
	c  *server.Client
}

func (layerShell) Destroy(*swlr.ZwlrLayerShellV1) error {
	return nil
}

func (m layerShell) GetLayerSurface(obj *swlr.ZwlrLayerShellV1, res *swlr.ZwlrLayerSurfaceV1, wl *sw.WlSurface, out *sw.WlOutput, layer Layer, namespace string) (swlr.ZwlrLayerSurfaceV1Handler, error) {
	if layer > swlr.ZwlrLayerShellV1LayerOverlay {
		return nil, obj.ProtocolError(swlr.ZwlrLayerShellV1ErrorInvalidLayer, fmt.Sprintf("layer %d", layer))
	}
	surf, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	if surf.HasBuffer() {
		return nil, obj.ProtocolError(swlr.ZwlrLayerShellV1ErrorAlreadyConstructed, "the wl_surface already has a buffer")
	}
	l := &LayerSurface{shell: m.sh, client: m.c, manager: obj, resource: res, surface: surf, output: out, namespace: namespace}
	l.pending.Layer = layer
	if err := surf.SetRole(l); err != nil {
		return nil, obj.ProtocolError(swlr.ZwlrLayerShellV1ErrorRole, err.Error())
	}
	m.c.OnDestroy(res, l.destroy)
	m.c.OnDestroy(wl, l.destroy)
	return l, nil
}

func (l *LayerSurface) RoleName() string {
	return layerRole
}

func (l *LayerSurface) Surface() *surface.Surface {
	return l.surface
}

func (l *LayerSurface) Output() *sw.WlOutput {
	return l.output
}

func (l *LayerSurface) Namespace() string {
	return l.namespace
}

func (l *LayerSurface) Current() LayerState {
	return l.current
}

func (l *LayerSurface) Mapped() bool {
	return l.mapped
}

func (l *LayerSurface) Live() bool {
	return l.initialSent && !l.closed && !l.destroyed
}

func (l *LayerSurface) Popups() []*Popup {
	return slices.Clone(l.popups)
}

func (l *LayerSurface) children() *[]*Popup {
	return &l.popups
}

func (l *LayerSurface) root() PopupRoot {
	return l
}

func (l *LayerSurface) offset() image.Point {
	return image.Point{}
}

func (l *LayerSurface) Configure(size image.Point) error {
	if l.closed || l.destroyed || !l.initialSent || (l.sent && size == l.sentSize) {
		return nil
	}
	if size.X < 0 || size.Y < 0 || size.X > math.MaxUint32 || size.Y > math.MaxUint32 {
		return fmt.Errorf("shell: layer surface size %v does not fit the protocol", size)
	}
	serial := l.shell.display.NextSerial()
	if err := l.client.Sent(l.resource.SendConfigure(serial, uint32(size.X), uint32(size.Y))); err != nil {
		return err
	}
	l.configures.push(serial, size)
	l.sentSize, l.sent = size, true
	return nil
}

func (l *LayerSurface) Close() error {
	if l.closed || l.destroyed {
		return nil
	}
	l.closed = true
	errs := []error{l.dismissPopups()}
	if l.mapped {
		l.mapped = false
		errs = append(errs, l.shell.listener.LayerUnmapped(l))
	}
	return errors.Join(append(errs, l.client.Sent(l.resource.SendClosed()))...)
}

func (l *LayerSurface) dismissPopups() error {
	var errs []error
	for _, p := range slices.Backward(l.popups) {
		errs = append(errs, p.dismiss())
	}
	return errors.Join(errs...)
}

func (l *LayerSurface) validate(s LayerState) error {
	const horizontal = swlr.ZwlrLayerSurfaceV1AnchorLeft | swlr.ZwlrLayerSurfaceV1AnchorRight
	const vertical = swlr.ZwlrLayerSurfaceV1AnchorTop | swlr.ZwlrLayerSurfaceV1AnchorBottom
	switch {
	case s.Size.X == 0 && s.Anchor&horizontal != horizontal:
		return l.resource.ProtocolError(swlr.ZwlrLayerSurfaceV1ErrorInvalidSize, "a width of 0 needs both the left and the right anchor")
	case s.Size.Y == 0 && s.Anchor&vertical != vertical:
		return l.resource.ProtocolError(swlr.ZwlrLayerSurfaceV1ErrorInvalidSize, "a height of 0 needs both the top and the bottom anchor")
	case s.ExclusiveEdge != 0 && s.Anchor&s.ExclusiveEdge == 0:
		return l.resource.ProtocolError(swlr.ZwlrLayerSurfaceV1ErrorInvalidExclusiveEdge, fmt.Sprintf("exclusive edge %d is not among the anchors %d", s.ExclusiveEdge, s.Anchor))
	}
	return nil
}

func (l *LayerSurface) Committed(s *surface.Surface) error {
	if l.closed {
		return nil
	}
	if err := l.validate(l.pending); err != nil {
		return err
	}
	l.current = l.pending
	if _, ok := l.configures.take(); ok {
		l.configured = true
	}
	if s.Buffer() != nil && !l.configured {
		return l.resource.ProtocolError(swlr.ZwlrLayerSurfaceV1ErrorInvalidSurfaceState, "a buffer was attached before the first configure was acked")
	}
	switch {
	case !l.initialSent:
		l.initialSent = true
	case s.Buffer() == nil && l.mapped:
		l.mapped, l.configured, l.initialSent, l.sent = false, false, false, false
		l.configures.reset()
		return errors.Join(l.dismissPopups(), l.shell.listener.LayerUnmapped(l))
	case s.Buffer() != nil && !l.mapped:
		l.mapped = true
	}
	return l.shell.listener.LayerCommitted(l)
}

func (l *LayerSurface) destroy() error {
	if l.destroyed {
		return nil
	}
	l.destroyed = true
	errs := []error{l.dismissPopups()}
	if l.mapped {
		l.mapped = false
		errs = append(errs, l.shell.listener.LayerUnmapped(l))
	}
	l.surface.ClearRole(l)
	return errors.Join(append(errs, l.shell.listener.LayerDestroyed(l))...)
}

func (l *LayerSurface) SetSize(_ *swlr.ZwlrLayerSurfaceV1, w, h uint32) error {
	l.pending.Size = image.Pt(int(w), int(h))
	return nil
}

func (l *LayerSurface) SetAnchor(obj *swlr.ZwlrLayerSurfaceV1, anchor Edges) error {
	if anchor&^allEdges != 0 {
		return obj.ProtocolError(swlr.ZwlrLayerSurfaceV1ErrorInvalidAnchor, fmt.Sprintf("anchor %d", anchor))
	}
	l.pending.Anchor = anchor
	return nil
}

func (l *LayerSurface) SetExclusiveZone(_ *swlr.ZwlrLayerSurfaceV1, zone int32) error {
	l.pending.ExclusiveZone = int(zone)
	return nil
}

func (l *LayerSurface) SetMargin(_ *swlr.ZwlrLayerSurfaceV1, top, right, bottom, left int32) error {
	l.pending.Margin = Margin{Top: int(top), Right: int(right), Bottom: int(bottom), Left: int(left)}
	return nil
}

func (l *LayerSurface) SetKeyboardInteractivity(obj *swlr.ZwlrLayerSurfaceV1, k Interactivity) error {
	if k > swlr.ZwlrLayerSurfaceV1KeyboardInteractivityOnDemand || (k == swlr.ZwlrLayerSurfaceV1KeyboardInteractivityOnDemand && obj.Base().Version() < 4) {
		return obj.ProtocolError(swlr.ZwlrLayerSurfaceV1ErrorInvalidKeyboardInteractivity, fmt.Sprintf("keyboard interactivity %d at version %d", k, obj.Base().Version()))
	}
	l.pending.Interactivity = k
	return nil
}

func (l *LayerSurface) GetPopup(_ *swlr.ZwlrLayerSurfaceV1, res *sxdg.XdgPopup) error {
	p, ok := res.Handler().(*Popup)
	if !ok {
		return fmt.Errorf("shell: xdg_popup@%d has handler %T", res.Base().ID(), res.Handler())
	}
	if p.parent != nil || p.xs.initialSent {
		return p.xs.base.resource.ProtocolError(sxdg.XdgWmBaseErrorInvalidPopupParent, "the popup already has a parent or its initial commit")
	}
	p.adopt(l)
	return nil
}

func (l *LayerSurface) AckConfigure(obj *swlr.ZwlrLayerSurfaceV1, serial uint32) error {
	if !l.configures.ack(serial) {
		return obj.ProtocolError(swlr.ZwlrLayerSurfaceV1ErrorInvalidSurfaceState, fmt.Sprintf("serial %d was never sent or is already acked", serial))
	}
	return nil
}

func (l *LayerSurface) Destroy(*swlr.ZwlrLayerSurfaceV1) error {
	return nil
}

func (l *LayerSurface) SetLayer(_ *swlr.ZwlrLayerSurfaceV1, layer Layer) error {
	if layer > swlr.ZwlrLayerShellV1LayerOverlay {
		return l.manager.ProtocolError(swlr.ZwlrLayerShellV1ErrorInvalidLayer, fmt.Sprintf("layer %d", layer))
	}
	l.pending.Layer = layer
	return nil
}

func (l *LayerSurface) SetExclusiveEdge(obj *swlr.ZwlrLayerSurfaceV1, edge Edges) error {
	if edge&^allEdges != 0 || bits.OnesCount32(uint32(edge)) > 1 {
		return obj.ProtocolError(swlr.ZwlrLayerSurfaceV1ErrorInvalidExclusiveEdge, fmt.Sprintf("exclusive edge %d is not a single edge", edge))
	}
	l.pending.ExclusiveEdge = edge
	return nil
}
