package data

import (
	"errors"
	"fmt"
	"slices"

	sedc "github.com/stubbedev/tilde/internal/protocol/server/extdatacontrol"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	swdc "github.com/stubbedev/tilde/internal/protocol/server/wlrdatacontrol"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	wlrControlVersion = 2
	extControlVersion = 1
)

type controlSource struct {
	m         *Manager
	client    *server.Client
	mimes     []string
	send      func(mime string, fd int) error
	cancel    func() error
	used      bool
	destroyed bool
}

func (s *controlSource) Client() *server.Client {
	return s.client
}

func (s *controlSource) Mimes() []string {
	return s.mimes
}

func (s *controlSource) Send(mime string, fd int) error {
	if s.destroyed {
		return nil
	}
	return s.client.Sent(s.send(mime, fd))
}

func (s *controlSource) Cancel() error {
	if s.destroyed {
		return nil
	}
	return s.client.Sent(s.cancel())
}

func (s *controlSource) offer(mime string) bool {
	if s.used {
		return false
	}
	if !slices.Contains(s.mimes, mime) {
		s.mimes = append(s.mimes, mime)
	}
	return true
}

func (s *controlSource) destroy() error {
	s.destroyed = true
	return s.m.sourceGone(s)
}

type controlDevice struct {
	client  *server.Client
	primary bool
	offer   func(k Kind, src Source) error
}

func (m *Manager) addControl(d *controlDevice) error {
	m.controls = append(m.controls, d)
	var errs []error
	for k := range kinds {
		if k == Primary && !d.primary {
			continue
		}
		errs = append(errs, d.client.Sent(d.offer(k, m.selection[k])))
	}
	return errors.Join(errs...)
}

func (m *Manager) removeControl(d *controlDevice) {
	m.controls = slices.DeleteFunc(m.controls, func(x *controlDevice) bool { return x == d })
}

func (m *Manager) offerControls(k Kind) error {
	for _, o := range m.controlOffers[k] {
		o.dead = true
	}
	m.controlOffers[k] = m.controlOffers[k][:0]
	var errs []error
	for _, d := range m.controls {
		if k == Primary && !d.primary {
			continue
		}
		errs = append(errs, d.client.Sent(d.offer(k, m.selection[k])))
	}
	return errors.Join(errs...)
}

func (m *Manager) controlOffer(k Kind, src Source) *selectionOffer {
	o := &selectionOffer{source: src}
	m.controlOffers[k] = append(m.controlOffers[k], o)
	return o
}

func offerTo[O interface {
	comparable
	SendOffer(mime string) error
}](src Source, create func() (O, error), send func(O) error) error {
	var none O
	if src == nil {
		return send(none)
	}
	o, err := create()
	if err != nil {
		return err
	}
	for _, mime := range src.Mimes() {
		if err := o.SendOffer(mime); err != nil {
			return err
		}
	}
	return send(o)
}

func (m *Manager) setControlSelection(k Kind, s *controlSource) error {
	if s == nil {
		return m.SetSelection(k, nil)
	}
	s.used = true
	return m.SetSelection(k, s)
}

func (m *Manager) bindWlrControl(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := swdc.NewZwlrDataControlManagerV1(c, id, version, wlrControl{m, c})
	return err
}

type wlrControl struct {
	m *Manager
	c *server.Client
}

func (wlrControl) Destroy(*swdc.ZwlrDataControlManagerV1) error {
	return nil
}

func (r wlrControl) CreateDataSource(_ *swdc.ZwlrDataControlManagerV1, res *swdc.ZwlrDataControlSourceV1) (swdc.ZwlrDataControlSourceV1Handler, error) {
	s := &controlSource{m: r.m, client: r.c, send: res.SendSend, cancel: res.SendCancelled}
	r.c.OnDestroy(res, s.destroy)
	return wlrSource{s}, nil
}

func (r wlrControl) GetDataDevice(_ *swdc.ZwlrDataControlManagerV1, res *swdc.ZwlrDataControlDeviceV1, _ *sw.WlSeat) (swdc.ZwlrDataControlDeviceV1Handler, error) {
	d := &controlDevice{client: r.c, primary: res.Base().Version() >= 2}
	d.offer = func(k Kind, src Source) error {
		send := res.SendSelection
		if k == Primary {
			send = res.SendPrimarySelection
		}
		return offerTo(src, func() (*swdc.ZwlrDataControlOfferV1, error) {
			return res.SendDataOffer(wlrOffer{r.m.controlOffer(k, src)})
		}, send)
	}
	r.c.OnDestroy(res, func() error {
		r.m.removeControl(d)
		return nil
	})
	return wlrDevice{r.m, d}, r.m.addControl(d)
}

type wlrSource struct{ s *controlSource }

func (w wlrSource) Offer(obj *swdc.ZwlrDataControlSourceV1, mime string) error {
	if !w.s.offer(mime) {
		return obj.ProtocolError(swdc.ZwlrDataControlSourceV1ErrorInvalidOffer, "offer after the source was used")
	}
	return nil
}

func (wlrSource) Destroy(*swdc.ZwlrDataControlSourceV1) error {
	return nil
}

func wlrSourceOf(res *swdc.ZwlrDataControlSourceV1) (*controlSource, error) {
	if res == nil {
		return nil, nil
	}
	h, ok := res.Handler().(wlrSource)
	if !ok {
		return nil, fmt.Errorf("data: data-control source@%d has handler %T", res.Base().ID(), res.Handler())
	}
	return h.s, nil
}

type wlrDevice struct {
	m *Manager
	d *controlDevice
}

func (wlrDevice) Destroy(*swdc.ZwlrDataControlDeviceV1) error {
	return nil
}

func (w wlrDevice) set(obj *swdc.ZwlrDataControlDeviceV1, k Kind, res *swdc.ZwlrDataControlSourceV1) error {
	s, err := wlrSourceOf(res)
	if err != nil {
		return err
	}
	if s != nil && s.used {
		return obj.ProtocolError(swdc.ZwlrDataControlDeviceV1ErrorUsedSource, "the source was already used")
	}
	return w.m.setControlSelection(k, s)
}

func (w wlrDevice) SetSelection(obj *swdc.ZwlrDataControlDeviceV1, res *swdc.ZwlrDataControlSourceV1) error {
	return w.set(obj, Clipboard, res)
}

func (w wlrDevice) SetPrimarySelection(obj *swdc.ZwlrDataControlDeviceV1, res *swdc.ZwlrDataControlSourceV1) error {
	return w.set(obj, Primary, res)
}

type wlrOffer struct{ o *selectionOffer }

func (w wlrOffer) Receive(_ *swdc.ZwlrDataControlOfferV1, mime string, fd int) error {
	return w.o.receive(mime, fd)
}

func (wlrOffer) Destroy(*swdc.ZwlrDataControlOfferV1) error {
	return nil
}

func (m *Manager) bindExtControl(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sedc.NewExtDataControlManagerV1(c, id, version, extControl{m, c})
	return err
}

type extControl struct {
	m *Manager
	c *server.Client
}

func (extControl) Destroy(*sedc.ExtDataControlManagerV1) error {
	return nil
}

func (r extControl) CreateDataSource(_ *sedc.ExtDataControlManagerV1, res *sedc.ExtDataControlSourceV1) (sedc.ExtDataControlSourceV1Handler, error) {
	s := &controlSource{m: r.m, client: r.c, send: res.SendSend, cancel: res.SendCancelled}
	r.c.OnDestroy(res, s.destroy)
	return extSource{s}, nil
}

func (r extControl) GetDataDevice(_ *sedc.ExtDataControlManagerV1, res *sedc.ExtDataControlDeviceV1, _ *sw.WlSeat) (sedc.ExtDataControlDeviceV1Handler, error) {
	d := &controlDevice{client: r.c, primary: true}
	d.offer = func(k Kind, src Source) error {
		send := res.SendSelection
		if k == Primary {
			send = res.SendPrimarySelection
		}
		return offerTo(src, func() (*sedc.ExtDataControlOfferV1, error) {
			return res.SendDataOffer(extOffer{r.m.controlOffer(k, src)})
		}, send)
	}
	r.c.OnDestroy(res, func() error {
		r.m.removeControl(d)
		return nil
	})
	return extDevice{r.m, d}, r.m.addControl(d)
}

type extSource struct{ s *controlSource }

func (e extSource) Offer(obj *sedc.ExtDataControlSourceV1, mime string) error {
	if !e.s.offer(mime) {
		return obj.ProtocolError(sedc.ExtDataControlSourceV1ErrorInvalidOffer, "offer after the source was used")
	}
	return nil
}

func (extSource) Destroy(*sedc.ExtDataControlSourceV1) error {
	return nil
}

func extSourceOf(res *sedc.ExtDataControlSourceV1) (*controlSource, error) {
	if res == nil {
		return nil, nil
	}
	h, ok := res.Handler().(extSource)
	if !ok {
		return nil, fmt.Errorf("data: data-control source@%d has handler %T", res.Base().ID(), res.Handler())
	}
	return h.s, nil
}

type extDevice struct {
	m *Manager
	d *controlDevice
}

func (extDevice) Destroy(*sedc.ExtDataControlDeviceV1) error {
	return nil
}

func (e extDevice) set(obj *sedc.ExtDataControlDeviceV1, k Kind, res *sedc.ExtDataControlSourceV1) error {
	s, err := extSourceOf(res)
	if err != nil {
		return err
	}
	if s != nil && s.used {
		return obj.ProtocolError(sedc.ExtDataControlDeviceV1ErrorUsedSource, "the source was already used")
	}
	return e.m.setControlSelection(k, s)
}

func (e extDevice) SetSelection(obj *sedc.ExtDataControlDeviceV1, res *sedc.ExtDataControlSourceV1) error {
	return e.set(obj, Clipboard, res)
}

func (e extDevice) SetPrimarySelection(obj *sedc.ExtDataControlDeviceV1, res *sedc.ExtDataControlSourceV1) error {
	return e.set(obj, Primary, res)
}

type extOffer struct{ o *selectionOffer }

func (e extOffer) Receive(_ *sedc.ExtDataControlOfferV1, mime string, fd int) error {
	return e.o.receive(mime, fd)
}

func (extOffer) Destroy(*sedc.ExtDataControlOfferV1) error {
	return nil
}
