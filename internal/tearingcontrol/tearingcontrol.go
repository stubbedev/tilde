// Package tearingcontrol implements wp_tearing_control_manager_v1: a surface
// hints that its content may be presented with tearing, which the compositor
// honours where the user allows it and the surface fills an output.
package tearingcontrol

import (
	"fmt"

	stc "github.com/stubbedev/tilde/internal/protocol/server/tearingcontrol"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const version = 1

type Manager struct {
	async *surface.Latched[bool]
}

func New(d *server.Display) (*Manager, error) {
	m := &Manager{async: surface.NewLatched(false)}
	if _, err := d.AddGlobal(stc.WpTearingControlManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Async(s *surface.Surface) bool {
	return m.async.Get(s)
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := stc.NewWpTearingControlManagerV1(c, id, v, requests{m})
	return err
}

type requests struct{ m *Manager }

func (requests) Destroy(*stc.WpTearingControlManagerV1) error { return nil }

func (r requests) GetTearingControl(obj *stc.WpTearingControlManagerV1, res *stc.WpTearingControlV1, wl *sw.WlSurface) (stc.WpTearingControlV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	v, ok := r.m.async.Attach(s)
	if !ok {
		return nil, obj.ProtocolError(stc.WpTearingControlManagerV1ErrorTearingControlExists, "wl_surface@%d already has a tearing control", wl.Base().ID())
	}
	server.OnDestroy(res, v.Detach)
	return hint{v}, nil
}

type hint struct{ v *surface.LatchedValue[bool] }

func (hint) Destroy(*stc.WpTearingControlV1) error { return nil }

func (h hint) SetPresentationHint(obj *stc.WpTearingControlV1, v stc.WpTearingControlV1PresentationHint) error {
	if !v.Valid(obj.Base().Version()) {
		return fmt.Errorf("tearingcontrol: presentation hint %d is neither vsync nor async", v)
	}
	h.v.Set(v == stc.WpTearingControlV1PresentationHintAsync)
	return nil
}
