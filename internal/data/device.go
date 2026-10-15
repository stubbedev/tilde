package data

import (
	"errors"
	"fmt"
	"slices"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

type Action = sw.WlDataDeviceManagerDndAction

const allActions = sw.WlDataDeviceManagerDndActionCopy | sw.WlDataDeviceManagerDndActionMove | sw.WlDataDeviceManagerDndActionAsk

func (m *Manager) bindData(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sw.NewWlDataDeviceManager(c, id, version, dataManager{m, c})
	return err
}

type dataManager struct {
	m *Manager
	c *server.Client
}

func (dataManager) Release(*sw.WlDataDeviceManager) error {
	return nil
}

func (r dataManager) CreateDataSource(_ *sw.WlDataDeviceManager, res *sw.WlDataSource) (sw.WlDataSourceHandler, error) {
	s := &dataSource{m: r.m, client: r.c, resource: res}
	r.c.OnDestroy(res, s.destroy)
	return s, nil
}

func (r dataManager) GetDataDevice(_ *sw.WlDataDeviceManager, res *sw.WlDataDevice, _ *sw.WlSeat) (sw.WlDataDeviceHandler, error) {
	d := r.m.devicesOf(r.c)
	track(r.m, d, &d.data, res)
	if r.c != r.m.focus {
		return dataDevice(r), nil
	}
	return dataDevice(r), r.c.Sent(r.m.offerData(r.c, res, r.m.selection[Clipboard]))
}

type dataSource struct {
	m          *Manager
	client     *server.Client
	resource   *sw.WlDataSource
	mimes      []string
	actions    Action
	actionsSet bool
	used       bool
	destroyed  bool
}

func (s *dataSource) Client() *server.Client {
	return s.client
}

func (s *dataSource) Mimes() []string {
	return s.mimes
}

func (s *dataSource) Send(mime string, fd int) error {
	if s.destroyed {
		return nil
	}
	return s.client.Sent(s.resource.SendSend(mime, fd))
}

func (s *dataSource) Cancel() error {
	if s.destroyed {
		return nil
	}
	return s.client.Sent(s.resource.SendCancelled())
}

func (s *dataSource) version() uint32 {
	return s.resource.Base().Version()
}

func (s *dataSource) Offer(_ *sw.WlDataSource, mime string) error {
	if !slices.Contains(s.mimes, mime) {
		s.mimes = append(s.mimes, mime)
	}
	return nil
}

func (s *dataSource) SetActions(obj *sw.WlDataSource, actions Action) error {
	switch {
	case actions&^allActions != 0:
		return obj.ProtocolError(sw.WlDataSourceErrorInvalidActionMask, fmt.Sprintf("actions %d", actions))
	case s.actionsSet || s.used:
		return obj.ProtocolError(sw.WlDataSourceErrorInvalidSource, "set_actions must come once, before the source is used")
	}
	s.actions, s.actionsSet = actions, true
	return nil
}

func (s *dataSource) Destroy(*sw.WlDataSource) error {
	return nil
}

func (s *dataSource) destroy() error {
	s.destroyed = true
	errs := []error{s.m.sourceGone(s)}
	if d := s.m.drag; d != nil && d.source == s {
		errs = append(errs, d.Cancel())
	}
	return errors.Join(errs...)
}

type dataDevice struct {
	m *Manager
	c *server.Client
}

func (dataDevice) Release(*sw.WlDataDevice) error {
	return nil
}

func sourceOf(res *sw.WlDataSource) (*dataSource, error) {
	if res == nil {
		return nil, nil
	}
	s, ok := res.Handler().(*dataSource)
	if !ok {
		return nil, fmt.Errorf("data: wl_data_source@%d has handler %T", res.Base().ID(), res.Handler())
	}
	return s, nil
}

func (d dataDevice) SetSelection(obj *sw.WlDataDevice, res *sw.WlDataSource, _ uint32) error {
	s, err := sourceOf(res)
	if err != nil {
		return err
	}
	if s != nil && s.actionsSet {
		return res.ProtocolError(sw.WlDataSourceErrorInvalidSource, "a drag-and-drop source cannot be the selection")
	}
	if d.c != d.m.focus {
		if s != nil {
			return s.Cancel()
		}
		return nil
	}
	if s == nil {
		return d.m.SetSelection(Clipboard, nil)
	}
	s.used = true
	return d.m.SetSelection(Clipboard, s)
}

func (d dataDevice) StartDrag(obj *sw.WlDataDevice, res *sw.WlDataSource, origin *sw.WlSurface, icon *sw.WlSurface, serial uint32) error {
	s, err := sourceOf(res)
	if err != nil {
		return err
	}
	if s != nil && s.used {
		return obj.ProtocolError(sw.WlDataDeviceErrorUsedSource, "the source was already used")
	}
	from, err := surface.From(origin)
	if err != nil {
		return err
	}
	var iconSurface *surface.Surface
	if icon != nil {
		if iconSurface, err = surface.From(icon); err != nil {
			return err
		}
	}
	if s != nil {
		s.used = true
	}
	if from.Client() != d.c || !d.m.listener.CanDrag(from, serial) {
		if s != nil {
			return s.Cancel()
		}
		return nil
	}
	return d.m.startDrag(obj, s, from, iconSurface, serial)
}

type dataOffer struct {
	selectionOffer
	m         *Manager
	client    *server.Client
	resource  *sw.WlDataOffer
	drag      *Drag
	accepted  *string
	actions   Action
	preferred Action
	chosen    Action
	finished  bool
}

func (m *Manager) offerData(c *server.Client, dev *sw.WlDataDevice, src Source) error {
	if src == nil {
		return dev.SendSelection(nil)
	}
	o := &dataOffer{source: src, m: m, client: c}
	res, err := dev.SendDataOffer(o)
	if err != nil {
		return err
	}
	o.resource = res
	m.offers[Clipboard] = append(m.offers[Clipboard], &o.selectionOffer)
	for _, mime := range src.Mimes() {
		if err := res.SendOffer(mime); err != nil {
			return err
		}
	}
	return dev.SendSelection(res)
}

func (o *dataOffer) Accept(_ *sw.WlDataOffer, _ uint32, mime *string) error {
	if o.drag == nil || o.dead || o.drag.dropped {
		return nil
	}
	o.accepted = mime
	return o.drag.target(mime)
}

func (o *dataOffer) Receive(_ *sw.WlDataOffer, mime string, fd int) error {
	return o.receive(mime, fd)
}

func (o *dataOffer) Finish(obj *sw.WlDataOffer) error {
	d := o.drag
	if d == nil || !d.dropped || d.offer != o || o.accepted == nil || o.chosen == sw.WlDataDeviceManagerDndActionNone {
		return obj.ProtocolError(sw.WlDataOfferErrorInvalidFinish, "finish before a drop with an accepted type and action")
	}
	if o.chosen == sw.WlDataDeviceManagerDndActionAsk {
		return obj.ProtocolError(sw.WlDataOfferErrorInvalidFinish, "finish while the action is still ask")
	}
	o.finished = true
	return d.finish()
}

func (o *dataOffer) SetActions(obj *sw.WlDataOffer, actions, preferred Action) error {
	switch {
	case actions&^allActions != 0:
		return obj.ProtocolError(sw.WlDataOfferErrorInvalidActionMask, fmt.Sprintf("actions %d", actions))
	case preferred&^allActions != 0 || (preferred != 0 && preferred&(preferred-1) != 0):
		return obj.ProtocolError(sw.WlDataOfferErrorInvalidAction, fmt.Sprintf("preferred action %d is not a single action", preferred))
	case o.drag == nil:
		return obj.ProtocolError(sw.WlDataOfferErrorInvalidOffer, "set_actions on a selection offer")
	}
	o.actions, o.preferred = actions, preferred
	return o.drag.negotiate()
}

func (o *dataOffer) Destroy(*sw.WlDataOffer) error {
	d := o.drag
	if d == nil || !d.dropped || d.offer != o || o.finished {
		return nil
	}
	if o.resource.Base().Version() < 3 {
		return d.finish()
	}
	return d.Cancel()
}
