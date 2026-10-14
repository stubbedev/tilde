// Package alphamodifier implements wp_alpha_modifier_v1: clients fade a
// surface by an opacity factor the compositor applies when it draws it.
package alphamodifier

import (
	"fmt"
	"math"

	swam "github.com/stubbedev/tilde/internal/protocol/server/alphamodifier"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const version = 1

type Manager struct {
	surfaces map[*surface.Surface]*modified
}

func New(d *server.Display) (*Manager, error) {
	m := &Manager{surfaces: map[*surface.Surface]*modified{}}
	if _, err := d.AddGlobal(swam.WpAlphaModifierV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Alpha(s *surface.Surface) float32 {
	if a, ok := m.surfaces[s]; ok {
		return a.current
	}
	return 1
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := swam.NewWpAlphaModifierV1(c, id, v, requests{m, c})
	return err
}

type requests struct {
	m *Manager
	c *server.Client
}

func (requests) Destroy(*swam.WpAlphaModifierV1) error { return nil }

type modified struct {
	m        *Manager
	s        *surface.Surface
	hooks    *surface.Hooks
	live     bool
	pending  *float32
	inFlight int
	current  float32
	gone     bool
}

func factorOf(f uint32) float32 {
	return float32(float64(f) / math.MaxUint32)
}

func (r requests) GetSurface(obj *swam.WpAlphaModifierV1, res *swam.WpAlphaModifierSurfaceV1, wl *sw.WlSurface) (swam.WpAlphaModifierSurfaceV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	a, ok := r.m.surfaces[s]
	if ok && a.live {
		return nil, obj.ProtocolError(swam.WpAlphaModifierV1ErrorAlreadyConstructed, fmt.Sprintf("wl_surface@%d already has an alpha modifier", wl.Base().ID()))
	}
	if !ok {
		a = &modified{m: r.m, s: s, current: 1}
		a.hooks = &surface.Hooks{Commit: a.commit}
		s.AddHooks(a.hooks)
		r.m.surfaces[s] = a
		r.c.OnDestroy(wl, a.surfaceGone)
	}
	a.live = true
	r.c.OnDestroy(res, func() error {
		a.live = false
		if !a.gone {
			one := float32(1)
			a.pending = &one
		}
		return nil
	})
	return a, nil
}

func (a *modified) Destroy(*swam.WpAlphaModifierSurfaceV1) error { return nil }

func (a *modified) SetMultiplier(obj *swam.WpAlphaModifierSurfaceV1, factor uint32) error {
	if a.gone {
		return obj.ProtocolError(swam.WpAlphaModifierSurfaceV1ErrorNoSurface, "the wl_surface was destroyed")
	}
	f := factorOf(factor)
	a.pending = &f
	return nil
}

func (a *modified) commit(u *surface.Update) error {
	if a.pending == nil {
		return nil
	}
	f := *a.pending
	a.pending = nil
	a.inFlight++
	u.OnApply(func() error {
		a.current = f
		a.inFlight--
		if !a.live && a.pending == nil && a.inFlight == 0 && a.current == 1 {
			a.forget()
		}
		return nil
	})
	return nil
}

func (a *modified) surfaceGone() error {
	a.gone = true
	a.forget()
	return nil
}

func (a *modified) forget() {
	a.s.RemoveHooks(a.hooks)
	if a.m.surfaces[a.s] == a {
		delete(a.m.surfaces, a.s)
	}
}
