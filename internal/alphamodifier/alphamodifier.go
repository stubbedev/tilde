// Package alphamodifier implements wp_alpha_modifier_v1: clients fade a
// surface by an opacity factor the compositor applies when it draws it.
package alphamodifier

import (
	"math"

	swam "github.com/stubbedev/lys-wm/internal/protocol/server/alphamodifier"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 1

type Manager struct {
	alpha *surface.Latched[float32]
}

func New(d *server.Display) (*Manager, error) {
	m := &Manager{alpha: surface.NewLatched[float32](1)}
	if _, err := d.AddGlobal(swam.WpAlphaModifierV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Alpha(s *surface.Surface) float32 {
	return m.alpha.Get(s)
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := swam.NewWpAlphaModifierV1(c, id, v, requests{m})
	return err
}

type requests struct{ m *Manager }

func (requests) Destroy(*swam.WpAlphaModifierV1) error { return nil }

func factorOf(f uint32) float32 {
	return float32(float64(f) / math.MaxUint32)
}

func (r requests) GetSurface(obj *swam.WpAlphaModifierV1, res *swam.WpAlphaModifierSurfaceV1, wl *sw.WlSurface) (swam.WpAlphaModifierSurfaceV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	v, ok := r.m.alpha.Attach(s)
	if !ok {
		return nil, obj.ProtocolError(swam.WpAlphaModifierV1ErrorAlreadyConstructed, "wl_surface@%d already has an alpha modifier", wl.Base().ID())
	}
	server.OnDestroy(res, v.Detach)
	return modified{v}, nil
}

type modified struct {
	v *surface.LatchedValue[float32]
}

func (modified) Destroy(*swam.WpAlphaModifierSurfaceV1) error { return nil }

func (a modified) SetMultiplier(obj *swam.WpAlphaModifierSurfaceV1, factor uint32) error {
	if a.v.Gone() {
		return obj.ProtocolError(swam.WpAlphaModifierSurfaceV1ErrorNoSurface, "the wl_surface was destroyed")
	}
	a.v.Set(factorOf(factor))
	return nil
}
