// Package output advertises backend outputs to clients as wl_output and
// zxdg_output_v1 objects, with their place in the logical layout and scale.
package output

import (
	"errors"
	"fmt"
	"image"
	"math"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	sxo "github.com/stubbedev/tilde/internal/protocol/server/xdgoutput"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	outputVersion    = 4
	xdgOutputVersion = 3
)

type Scale uint32

const (
	Unit     Scale = 120
	MinScale Scale = Unit / 4
	MaxScale Scale = Unit * 10
)

func ParseScale(f float64) (Scale, error) {
	if math.IsNaN(f) || f < MinScale.Float() || f > MaxScale.Float() {
		return 0, fmt.Errorf("output: scale %g is outside %g to %g", f, MinScale.Float(), MaxScale.Float())
	}
	return Scale(math.Round(f * float64(Unit))), nil
}

func (s Scale) Float() float64 {
	return float64(s) / float64(Unit)
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func (s Scale) Ceil() int32 {
	return int32((s + Unit - 1) / Unit) //nolint:gosec // at most 2^32/120, well inside int32
}

func (s Scale) ToDevice(v int) int {
	return floorDiv(2*v*int(s)+int(Unit), 2*int(Unit))
}

func (s Scale) ToLogical(px int) int {
	return floorDiv(2*px*int(Unit)+int(s), 2*int(s))
}

func (s Scale) DevicePoint(p image.Point) image.Point {
	return image.Pt(s.ToDevice(p.X), s.ToDevice(p.Y))
}

func (s Scale) LogicalPoint(p image.Point) image.Point {
	return image.Pt(s.ToLogical(p.X), s.ToLogical(p.Y))
}

func (s Scale) Rect(r image.Rectangle) image.Rectangle {
	return image.Rect(s.ToDevice(r.Min.X), s.ToDevice(r.Min.Y), s.ToDevice(r.Max.X), s.ToDevice(r.Max.Y))
}

type Manager struct {
	display  *server.Display
	outputs  []*Output
	physical func(*server.Client) bool
}

func New(d *server.Display) (*Manager, error) {
	m := &Manager{display: d}
	if _, err := d.AddGlobal(sxo.ZxdgOutputManagerV1Interface, xdgOutputVersion, m.bindXdg); err != nil {
		return nil, err
	}
	return m, nil
}

type binding struct {
	client *server.Client
	wl     *sw.WlOutput
	xdg    []*sxo.ZxdgOutputV1
}

type Output struct {
	m        *Manager
	backend  backend.Output
	global   *server.Global
	position image.Point
	scale    Scale
	bindings []*binding
}

func (m *Manager) Add(b backend.Output, position image.Point, scale Scale) (*Output, error) {
	if scale == 0 {
		return nil, fmt.Errorf("output: %s has scale 0", b.Name())
	}
	o := &Output{m: m, backend: b, position: position, scale: scale}
	var err error
	if o.global, err = m.display.AddGlobal(sw.WlOutputInterface, outputVersion, o.bind); err != nil {
		return nil, err
	}
	before := m.xScale()
	m.outputs = append(m.outputs, o)
	return o, m.rescaled(before, o)
}

func (m *Manager) Physical(clients func(*server.Client) bool) error {
	m.physical = clients
	return m.resend(func(*Output) bool { return true })
}

func (m *Manager) seesPhysical(c *server.Client) bool {
	return m.physical != nil && m.physical(c)
}

func (m *Manager) xScale() Scale {
	k := Unit
	for _, o := range m.outputs {
		k = max(k, o.scale)
	}
	return k
}

func (m *Manager) resend(which func(*Output) bool) error {
	var errs []error
	for _, o := range m.outputs {
		if !which(o) {
			continue
		}
		for _, b := range o.bindings {
			if m.seesPhysical(b.client) {
				errs = append(errs, b.client.Sent(o.send(b)))
			}
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) Owner(res *sw.WlOutput) *Output {
	for _, o := range m.outputs {
		for _, b := range o.bindings {
			if b.wl == res {
				return o
			}
		}
	}
	return nil
}

func (m *Manager) Outputs() []*Output {
	return slices.Clone(m.outputs)
}

func (o *Output) Backend() backend.Output {
	return o.backend
}

func (o *Output) Scale() Scale {
	return o.scale
}

func (o *Output) Position() image.Point {
	return o.position
}

func (o *Output) LogicalSize() image.Point {
	size := o.backend.State().Mode.Size
	return image.Pt(o.scale.ToLogical(size.X), o.scale.ToLogical(size.Y))
}

func (o *Output) Logical() image.Rectangle {
	return image.Rectangle{Min: o.position, Max: o.position.Add(o.LogicalSize())}
}

func (o *Output) XPosition() image.Point {
	return o.m.xScale().DevicePoint(o.position)
}

func (o *Output) XRect() image.Rectangle {
	at := o.XPosition()
	return image.Rectangle{Min: at, Max: at.Add(o.backend.State().Mode.Size)}
}

func (o *Output) Resources(c *server.Client) []*sw.WlOutput {
	return o.AppendResources(nil, c)
}

func (o *Output) AppendResources(out []*sw.WlOutput, c *server.Client) []*sw.WlOutput {
	for _, b := range o.bindings {
		if b.client == c {
			out = append(out, b.wl)
		}
	}
	return out
}

func (o *Output) Remove() error {
	before := o.m.xScale()
	o.m.outputs = slices.DeleteFunc(o.m.outputs, func(x *Output) bool { return x == o })
	return errors.Join(o.global.Remove(), o.m.rescaled(before, o))
}

func (m *Manager) rescaled(before Scale, done *Output) error {
	if m.xScale() == before {
		return nil
	}
	return m.resend(func(o *Output) bool { return o != done })
}

func (o *Output) Update(position image.Point, scale Scale) error {
	if scale == 0 {
		return fmt.Errorf("output: %s has scale 0", o.backend.Name())
	}
	before := o.m.xScale()
	o.position, o.scale = position, scale
	return errors.Join(o.Changed(), o.m.rescaled(before, o))
}

func (o *Output) Changed() error {
	var errs []error
	for _, b := range o.bindings {
		errs = append(errs, b.client.Sent(o.send(b)))
	}
	return errors.Join(errs...)
}

func i32(v int) (int32, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("output: %d does not fit the protocol", v)
	}
	return int32(v), nil
}

func i32s(vs ...int) ([]int32, error) {
	out := make([]int32, len(vs))
	for i, v := range vs {
		var err error
		if out[i], err = i32(v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (o *Output) bind(c *server.Client, id wire.ObjectID, version uint32) error {
	b := &binding{client: c}
	var err error
	if b.wl, err = sw.NewWlOutput(c, id, version, outputRequests{}); err != nil {
		return err
	}
	server.Track(b.wl, &o.bindings, b)
	return o.send(b)
}

func (o *Output) view(c *server.Client) (image.Rectangle, Scale) {
	if o.m.seesPhysical(c) {
		return o.XRect(), Unit
	}
	return o.Logical(), o.scale
}

func (o *Output) send(b *binding) error {
	state := o.backend.State()
	phys := o.backend.PhysicalSize()
	r, scale := o.view(b.client)
	v32, err := i32s(r.Min.X, r.Min.Y, phys.X, phys.Y, state.Mode.Size.X, state.Mode.Size.Y)
	if err != nil {
		return err
	}
	if err := b.wl.SendGeometry(v32[0], v32[1], v32[2], v32[3], sw.WlOutputSubpixelUnknown, "unknown", o.backend.Name(), sw.WlOutputTransformNormal); err != nil {
		return err
	}
	flags := sw.WlOutputModeCurrent
	if state.Mode.Preferred {
		flags |= sw.WlOutputModePreferred
	}
	if err := b.wl.SendMode(flags, v32[4], v32[5], state.Mode.RefreshMHz); err != nil {
		return err
	}
	v := b.wl.Base().Version()
	if v >= 2 {
		if err := b.wl.SendScale(scale.Ceil()); err != nil {
			return err
		}
	}
	if v >= 4 {
		if err := b.wl.SendName(o.backend.Name()); err != nil {
			return err
		}
		if err := b.wl.SendDescription(o.backend.Description()); err != nil {
			return err
		}
	}
	for _, x := range b.xdg {
		if err := o.sendXdg(b.client, x); err != nil {
			return err
		}
		if x.Base().Version() < xdgDoneOnWlOutput {
			if err := x.SendDone(); err != nil {
				return err
			}
		}
	}
	if v >= 2 {
		return b.wl.SendDone()
	}
	return nil
}

const xdgDoneOnWlOutput = 3

func xdgDone(wl *sw.WlOutput, x *sxo.ZxdgOutputV1) error {
	switch {
	case x.Base().Version() < xdgDoneOnWlOutput:
		return x.SendDone()
	case wl.Base().Version() >= 2:
		return wl.SendDone()
	}
	return nil
}

func (o *Output) sendXdg(c *server.Client, x *sxo.ZxdgOutputV1) error {
	r, _ := o.view(c)
	v32, err := i32s(r.Min.X, r.Min.Y, r.Dx(), r.Dy())
	if err != nil {
		return err
	}
	if err := x.SendLogicalPosition(v32[0], v32[1]); err != nil {
		return err
	}
	if err := x.SendLogicalSize(v32[2], v32[3]); err != nil {
		return err
	}
	if v := x.Base().Version(); v >= 2 {
		if err := x.SendName(o.backend.Name()); err != nil {
			return err
		}
		if err := x.SendDescription(o.backend.Description()); err != nil {
			return err
		}
	}
	return nil
}

type outputRequests struct{}

func (outputRequests) Release(*sw.WlOutput) error {
	return nil
}

func (m *Manager) bindXdg(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxo.NewZxdgOutputManagerV1(c, id, version, xdgManagerRequests{m})
	return err
}

type xdgManagerRequests struct{ m *Manager }

func (r xdgManagerRequests) Destroy(*sxo.ZxdgOutputManagerV1) error {
	return nil
}

func (r xdgManagerRequests) GetXdgOutput(_ *sxo.ZxdgOutputManagerV1, x *sxo.ZxdgOutputV1, wl *sw.WlOutput) (sxo.ZxdgOutputV1Handler, error) {
	for _, o := range r.m.outputs {
		for _, b := range o.bindings {
			if b.wl != wl {
				continue
			}
			server.Track(x, &b.xdg, x)
			if err := o.sendXdg(b.client, x); err != nil {
				return nil, err
			}
			return xdgRequests{}, xdgDone(wl, x)
		}
	}
	return xdgRequests{}, nil
}

type xdgRequests struct{}

func (xdgRequests) Destroy(*sxo.ZxdgOutputV1) error {
	return nil
}
