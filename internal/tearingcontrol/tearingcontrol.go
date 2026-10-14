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
	hints map[*surface.Surface]*hint
}

func New(d *server.Display) (*Manager, error) {
	m := &Manager{hints: map[*surface.Surface]*hint{}}
	if _, err := d.AddGlobal(stc.WpTearingControlManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Async(s *surface.Surface) bool {
	h, ok := m.hints[s]
	return ok && h.async
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := stc.NewWpTearingControlManagerV1(c, id, v, requests{m, c})
	return err
}

type requests struct {
	m *Manager
	c *server.Client
}

func (requests) Destroy(*stc.WpTearingControlManagerV1) error { return nil }

func (r requests) GetTearingControl(obj *stc.WpTearingControlManagerV1, res *stc.WpTearingControlV1, wl *sw.WlSurface) (stc.WpTearingControlV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	h, ok := r.m.hints[s]
	if ok && h.live {
		return nil, obj.ProtocolError(stc.WpTearingControlManagerV1ErrorTearingControlExists, fmt.Sprintf("wl_surface@%d already has a tearing control", wl.Base().ID()))
	}
	if !ok {
		h = &hint{m: r.m, s: s}
		h.hooks = &surface.Hooks{Commit: h.commit}
		s.AddHooks(h.hooks)
		r.m.hints[s] = h
		r.c.OnDestroy(wl, h.forget)
	}
	h.live = true
	r.c.OnDestroy(res, func() error {
		h.live = false
		vsync := false
		h.pending = &vsync
		return nil
	})
	return h, nil
}

type hint struct {
	m        *Manager
	s        *surface.Surface
	hooks    *surface.Hooks
	live     bool
	pending  *bool
	inFlight int
	async    bool
}

func (*hint) Destroy(*stc.WpTearingControlV1) error { return nil }

func (h *hint) SetPresentationHint(_ *stc.WpTearingControlV1, v stc.WpTearingControlV1PresentationHint) error {
	switch v {
	case stc.WpTearingControlV1PresentationHintVsync, stc.WpTearingControlV1PresentationHintAsync:
		async := v == stc.WpTearingControlV1PresentationHintAsync
		h.pending = &async
		return nil
	}
	return fmt.Errorf("tearingcontrol: presentation hint %d is neither vsync nor async", v)
}

func (h *hint) commit(u *surface.Update) error {
	if h.pending == nil {
		return nil
	}
	async := *h.pending
	h.pending = nil
	h.inFlight++
	u.OnApply(func() error {
		h.async = async
		h.inFlight--
		if !h.live && h.pending == nil && h.inFlight == 0 && !h.async {
			return h.forget()
		}
		return nil
	})
	return nil
}

func (h *hint) forget() error {
	h.s.RemoveHooks(h.hooks)
	if h.m.hints[h.s] == h {
		delete(h.m.hints, h.s)
	}
	return nil
}
