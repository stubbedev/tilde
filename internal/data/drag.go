package data

import (
	"errors"
	"image"
	"slices"
	"time"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const iconRole = "wl_data_device-icon"

type Drag struct {
	m          *Manager
	client     *server.Client
	source     *dataSource
	origin     *surface.Surface
	serial     uint32
	icon       *surface.Surface
	iconOffset image.Point
	focus      *surface.Surface
	devices    []*sw.WlDataDevice
	offers     []*dataOffer
	offer      *dataOffer
	dropped    bool
	ended      bool
	done       bool
}

type dragIcon struct{ d *Drag }

func (dragIcon) RoleName() string {
	return iconRole
}

func (i dragIcon) Committed(s *surface.Surface) error {
	i.d.iconOffset = i.d.iconOffset.Add(s.Offset())
	return nil
}

func (m *Manager) startDrag(dev *sw.WlDataDevice, s *dataSource, origin, icon *surface.Surface, serial uint32) error {
	if old := m.drag; old != nil && !old.dropped {
		if err := old.Cancel(); err != nil {
			return err
		}
	}
	d := &Drag{m: m, client: origin.Client(), source: s, origin: origin, serial: serial, icon: icon}
	if icon != nil {
		if err := icon.SetRole(dragIcon{d}); err != nil {
			return dev.ProtocolError(sw.WlDataDeviceErrorRole, err.Error())
		}
		server.OnDestroy(icon.Resource(), func() error {
			if d.icon == icon {
				d.icon = nil
			}
			return nil
		})
	}
	m.drag = d
	return m.listener.DragStarted(d)
}

func (d *Drag) Icon() (*surface.Surface, image.Point) {
	if d.ended {
		return nil, image.Point{}
	}
	return d.icon, d.iconOffset
}

func (d *Drag) Origin() *surface.Surface {
	return d.origin
}

func (d *Drag) Serial() uint32 {
	return d.serial
}

func (d *Drag) liveDevices() []*sw.WlDataDevice {
	if d.focus == nil {
		return nil
	}
	devs := d.m.lookup(d.focus.Client())
	if devs == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(d.devices), func(x *sw.WlDataDevice) bool { return !slices.Contains(devs.data, x) })
}

func (d *Drag) leave() error {
	var errs []error
	for _, dev := range d.liveDevices() {
		errs = append(errs, d.focus.Client().Sent(dev.SendLeave()))
	}
	for _, o := range d.offers {
		if o != d.offer {
			o.dead = true
		}
	}
	d.focus, d.devices, d.offers = nil, nil, nil
	return errors.Join(errs...)
}

func (d *Drag) enter(target *surface.Surface, x, y float64) error {
	d.focus = target
	if d.source == nil && target.Client() != d.client {
		return nil
	}
	devs := d.m.lookup(target.Client())
	if devs == nil {
		return nil
	}
	c := target.Client()
	serial := d.m.display.NextSerial()
	var errs []error
	for _, dev := range devs.data {
		var res *sw.WlDataOffer
		if d.source != nil {
			o, err := d.offerTo(dev)
			if err != nil {
				errs = append(errs, c.Sent(err))
				continue
			}
			res = o.resource
		}
		d.devices = append(d.devices, dev)
		errs = append(errs, c.Sent(dev.SendEnter(serial, target.Resource(), wire.FixedFromFloat(x), wire.FixedFromFloat(y), res)))
	}
	return errors.Join(errs...)
}

func (d *Drag) offerTo(dev *sw.WlDataDevice) (*dataOffer, error) {
	o := &dataOffer{source: d.source, m: d.m, client: d.focus.Client(), drag: d}
	res, err := dev.SendDataOffer(o)
	if err != nil {
		return nil, err
	}
	o.resource = res
	for _, mime := range d.source.Mimes() {
		if err := res.SendOffer(mime); err != nil {
			return nil, err
		}
	}
	if res.Base().Version() >= 3 {
		if err := res.SendSourceActions(d.sourceActions()); err != nil {
			return nil, err
		}
	}
	d.offers = append(d.offers, o)
	return o, nil
}

func (d *Drag) sourceActions() Action {
	if d.source == nil || !d.source.actionsSet {
		return sw.WlDataDeviceManagerDndActionCopy
	}
	return d.source.actions
}

func (d *Drag) Motion(target *surface.Surface, x, y float64, at time.Duration) error {
	if d.ended {
		return nil
	}
	if target != d.focus {
		var errs []error
		if d.focus != nil {
			errs = append(errs, d.leave())
		}
		if target != nil {
			errs = append(errs, d.enter(target, x, y))
		}
		return errors.Join(errs...)
	}
	var errs []error
	for _, dev := range d.liveDevices() {
		errs = append(errs, d.focus.Client().Sent(dev.SendMotion(wire.Millis(at), wire.FixedFromFloat(x), wire.FixedFromFloat(y))))
	}
	return errors.Join(errs...)
}

func (d *Drag) target(mime *string) error {
	if d.source == nil {
		return nil
	}
	return d.source.client.Sent(d.source.resource.SendTarget(mime))
}

func choose(available, preferred Action) Action {
	if preferred&available != 0 {
		return preferred
	}
	for _, a := range []Action{sw.WlDataDeviceManagerDndActionCopy, sw.WlDataDeviceManagerDndActionMove, sw.WlDataDeviceManagerDndActionAsk} {
		if available&a != 0 {
			return a
		}
	}
	return sw.WlDataDeviceManagerDndActionNone
}

func (d *Drag) negotiate() error {
	var errs []error
	for _, o := range d.offers {
		if o.dead {
			continue
		}
		chosen := choose(d.sourceActions()&o.actions, o.preferred)
		if chosen == o.chosen {
			continue
		}
		o.chosen = chosen
		errs = append(errs, o.client.Sent(o.resource.SendAction(chosen)))
		if d.source != nil && d.source.version() >= 3 {
			errs = append(errs, d.source.client.Sent(d.source.resource.SendAction(chosen)))
		}
	}
	return errors.Join(errs...)
}

func (d *Drag) acceptedOffer() *dataOffer {
	for _, o := range d.offers {
		if !o.dead && o.accepted != nil && (o.resource.Base().Version() < 3 || o.chosen != sw.WlDataDeviceManagerDndActionNone) {
			return o
		}
	}
	return nil
}

func (d *Drag) Drop() error {
	if d.ended {
		return nil
	}
	if d.source == nil {
		var errs []error
		for _, dev := range d.liveDevices() {
			errs = append(errs, d.focus.Client().Sent(dev.SendDrop()))
		}
		d.dropped = true
		errs = append(errs, d.leave(), d.end())
		d.release()
		return errors.Join(errs...)
	}
	o := d.acceptedOffer()
	if o == nil {
		return d.Cancel()
	}
	d.dropped, d.offer = true, o
	var errs []error
	for _, dev := range d.liveDevices() {
		errs = append(errs, d.focus.Client().Sent(dev.SendDrop()))
	}
	if d.source.version() >= 3 {
		errs = append(errs, d.source.client.Sent(d.source.resource.SendDndDropPerformed()))
	}
	return errors.Join(append(errs, d.leave(), d.end())...)
}

func (d *Drag) end() error {
	if d.ended {
		return nil
	}
	d.ended = true
	if d.icon != nil {
		d.icon.ClearRole(dragIcon{d})
	}
	return d.m.listener.DragEnded(d)
}

func (d *Drag) release() {
	d.done = true
	if d.m.drag == d {
		d.m.drag = nil
	}
}

func (d *Drag) Cancel() error {
	if d.done {
		return nil
	}
	var errs []error
	if !d.ended {
		errs = append(errs, d.leave())
	}
	if d.offer != nil {
		d.offer.dead = true
	}
	if d.source != nil && !d.source.destroyed {
		errs = append(errs, d.source.Cancel())
	}
	errs = append(errs, d.end())
	d.release()
	return errors.Join(errs...)
}

func (d *Drag) finish() error {
	if d.done {
		return nil
	}
	var errs []error
	if d.source != nil && !d.source.destroyed && d.source.version() >= 3 {
		errs = append(errs, d.source.client.Sent(d.source.resource.SendDndFinished()))
	}
	d.release()
	return errors.Join(errs...)
}
