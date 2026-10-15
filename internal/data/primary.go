package data

import (
	"fmt"
	"slices"

	sps "github.com/stubbedev/tilde/internal/protocol/server/primaryselection"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

func (m *Manager) bindPrimary(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sps.NewZwpPrimarySelectionDeviceManagerV1(c, id, version, primaryManager{m, c})
	return err
}

type primaryManager struct {
	m *Manager
	c *server.Client
}

func (primaryManager) Destroy(*sps.ZwpPrimarySelectionDeviceManagerV1) error {
	return nil
}

func (r primaryManager) CreateSource(_ *sps.ZwpPrimarySelectionDeviceManagerV1, res *sps.ZwpPrimarySelectionSourceV1) (sps.ZwpPrimarySelectionSourceV1Handler, error) {
	s := &primarySource{m: r.m, client: r.c, resource: res}
	server.OnDestroy(res, s.destroy)
	return s, nil
}

func (r primaryManager) GetDevice(_ *sps.ZwpPrimarySelectionDeviceManagerV1, res *sps.ZwpPrimarySelectionDeviceV1, _ *sw.WlSeat) (sps.ZwpPrimarySelectionDeviceV1Handler, error) {
	d := r.m.devicesOf(r.c)
	track(r.m, d, &d.primaries, res)
	if r.c != r.m.focus {
		return primaryDevice(r), nil
	}
	return primaryDevice(r), r.c.Sent(r.m.offerPrimary(res, r.m.selection[Primary]))
}

type primarySource struct {
	m         *Manager
	client    *server.Client
	resource  *sps.ZwpPrimarySelectionSourceV1
	mimes     []string
	destroyed bool
}

func (s *primarySource) Client() *server.Client {
	return s.client
}

func (s *primarySource) Mimes() []string {
	return s.mimes
}

func (s *primarySource) Send(mime string, fd int) error {
	if s.destroyed {
		return nil
	}
	return s.client.Sent(s.resource.SendSend(mime, fd))
}

func (s *primarySource) Cancel() error {
	if s.destroyed {
		return nil
	}
	return s.client.Sent(s.resource.SendCancelled())
}

func (s *primarySource) Offer(_ *sps.ZwpPrimarySelectionSourceV1, mime string) error {
	if !slices.Contains(s.mimes, mime) {
		s.mimes = append(s.mimes, mime)
	}
	return nil
}

func (s *primarySource) Destroy(*sps.ZwpPrimarySelectionSourceV1) error {
	return nil
}

func (s *primarySource) destroy() error {
	s.destroyed = true
	return s.m.sourceGone(s)
}

type primaryDevice struct {
	m *Manager
	c *server.Client
}

func (primaryDevice) Destroy(*sps.ZwpPrimarySelectionDeviceV1) error {
	return nil
}

func (d primaryDevice) SetSelection(_ *sps.ZwpPrimarySelectionDeviceV1, res *sps.ZwpPrimarySelectionSourceV1, _ uint32) error {
	var s *primarySource
	if res != nil {
		var ok bool
		if s, ok = res.Handler().(*primarySource); !ok {
			return fmt.Errorf("data: zwp_primary_selection_source_v1@%d has handler %T", res.Base().ID(), res.Handler())
		}
	}
	if d.c != d.m.focus {
		if s != nil {
			return s.Cancel()
		}
		return nil
	}
	if s == nil {
		return d.m.SetSelection(Primary, nil)
	}
	return d.m.SetSelection(Primary, s)
}

type primaryOffer struct {
	selectionOffer
}

func (m *Manager) offerPrimary(dev *sps.ZwpPrimarySelectionDeviceV1, src Source) error {
	if src == nil {
		return dev.SendSelection(nil)
	}
	o := &primaryOffer{selectionOffer{source: src}}
	res, err := dev.SendDataOffer(o)
	if err != nil {
		return err
	}
	m.offers[Primary] = append(m.offers[Primary], &o.selectionOffer)
	for _, mime := range src.Mimes() {
		if err := res.SendOffer(mime); err != nil {
			return err
		}
	}
	return dev.SendSelection(res)
}

func (o *primaryOffer) Receive(_ *sps.ZwpPrimarySelectionOfferV1, mime string, fd int) error {
	return o.receive(mime, fd)
}

func (o *primaryOffer) Destroy(*sps.ZwpPrimarySelectionOfferV1) error {
	return nil
}
