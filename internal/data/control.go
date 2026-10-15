package data

import (
	"errors"
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

type controlSource struct{ source }

func (s *controlSource) offer(mime string) bool {
	if !s.fresh() {
		return false
	}
	s.addMime(mime)
	return true
}

func (s *controlSource) destroy() error {
	s.end()
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

func (m *Manager) setControlSelection(k Kind, s *controlSource) error {
	if s == nil {
		return m.SetSelection(k, nil)
	}
	s.use()
	return m.SetSelection(k, s)
}

func (m *Manager) newControlSource(c *server.Client, res wire.Resource, send func(string, int) error, cancel func() error) *controlSource {
	s := &controlSource{source{m: m, client: c, send: send, cancel: cancel}}
	m.controlSources[res] = s
	server.OnDestroy(res, func() error {
		delete(m.controlSources, res)
		return s.destroy()
	})
	return s
}

func (m *Manager) openControl(res wire.Resource, d *controlDevice) error {
	server.OnDestroy(res, func() error {
		m.removeControl(d)
		return nil
	})
	return m.addControl(d)
}

func controlError(obj wire.Resource, code uint32, message string) error {
	return &wire.ProtocolError{Object: obj.Base().ID(), Code: code, Message: message}
}

func controlOffers[D interface {
	SendDataOffer(h H) (O, error)
	SendSelection(o O) error
	SendPrimarySelection(o O) error
}, O interface {
	comparable
	SendOffer(mime string) error
}, H any](m *Manager, res D, handler func(*selectionOffer) H) func(Kind, Source) error {
	return func(k Kind, src Source) error {
		send := res.SendSelection
		if k == Primary {
			send = res.SendPrimarySelection
		}
		return offerTo(src, func() (O, error) { return res.SendDataOffer(handler(m.controlOffer(k, src))) }, send)
	}
}

type controlSourceHandler[R wire.Resource] struct {
	s            *controlSource
	invalidOffer uint32
}

func (h controlSourceHandler[R]) Offer(obj R, mime string) error {
	if !h.s.offer(mime) {
		return controlError(obj, h.invalidOffer, "offer after the source was used")
	}
	return nil
}

func (controlSourceHandler[R]) Destroy(R) error {
	return nil
}

type controlDeviceHandler[R, S wire.Resource] struct {
	m          *Manager
	usedSource uint32
}

func (h controlDeviceHandler[R, S]) set(obj R, k Kind, res S) error {
	s := h.m.controlSources[res]
	if s != nil && !s.fresh() {
		return controlError(obj, h.usedSource, "the source was already used")
	}
	return h.m.setControlSelection(k, s)
}

func (h controlDeviceHandler[R, S]) SetSelection(obj R, res S) error {
	return h.set(obj, Clipboard, res)
}

func (h controlDeviceHandler[R, S]) SetPrimarySelection(obj R, res S) error {
	return h.set(obj, Primary, res)
}

func (controlDeviceHandler[R, S]) Destroy(R) error {
	return nil
}

type controlOfferHandler[R wire.Resource] struct{ o *selectionOffer }

func (h controlOfferHandler[R]) Receive(_ R, mime string, fd int) error {
	return h.o.receive(mime, fd)
}

func (controlOfferHandler[R]) Destroy(R) error {
	return nil
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
	s := r.m.newControlSource(r.c, res, res.SendSend, res.SendCancelled)
	return controlSourceHandler[*swdc.ZwlrDataControlSourceV1]{s, uint32(swdc.ZwlrDataControlSourceV1ErrorInvalidOffer)}, nil
}

func (r wlrControl) GetDataDevice(_ *swdc.ZwlrDataControlManagerV1, res *swdc.ZwlrDataControlDeviceV1, _ *sw.WlSeat) (swdc.ZwlrDataControlDeviceV1Handler, error) {
	d := &controlDevice{client: r.c, primary: res.Base().Version() >= 2, offer: controlOffers(r.m, res, func(o *selectionOffer) swdc.ZwlrDataControlOfferV1Handler {
		return controlOfferHandler[*swdc.ZwlrDataControlOfferV1]{o}
	})}
	h := controlDeviceHandler[*swdc.ZwlrDataControlDeviceV1, *swdc.ZwlrDataControlSourceV1]{r.m, uint32(swdc.ZwlrDataControlDeviceV1ErrorUsedSource)}
	return h, r.m.openControl(res, d)
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
	s := r.m.newControlSource(r.c, res, res.SendSend, res.SendCancelled)
	return controlSourceHandler[*sedc.ExtDataControlSourceV1]{s, uint32(sedc.ExtDataControlSourceV1ErrorInvalidOffer)}, nil
}

func (r extControl) GetDataDevice(_ *sedc.ExtDataControlManagerV1, res *sedc.ExtDataControlDeviceV1, _ *sw.WlSeat) (sedc.ExtDataControlDeviceV1Handler, error) {
	d := &controlDevice{client: r.c, primary: true, offer: controlOffers(r.m, res, func(o *selectionOffer) sedc.ExtDataControlOfferV1Handler {
		return controlOfferHandler[*sedc.ExtDataControlOfferV1]{o}
	})}
	h := controlDeviceHandler[*sedc.ExtDataControlDeviceV1, *sedc.ExtDataControlSourceV1]{r.m, uint32(sedc.ExtDataControlDeviceV1ErrorUsedSource)}
	return h, r.m.openControl(res, d)
}
