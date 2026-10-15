package compositor

import (
	ssb "github.com/stubbedev/tilde/internal/protocol/server/systembell"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const bellVersion = 1

func (c *Compositor) bindBell(cl *server.Client, id wire.ObjectID, version uint32) error {
	_, err := ssb.NewXdgSystemBellV1(cl, id, version, bell{c})
	return err
}

type bell struct{ c *Compositor }

func (bell) Destroy(*ssb.XdgSystemBellV1) error { return nil }

func (b bell) Ring(_ *ssb.XdgSystemBellV1, wl *sw.WlSurface) error {
	if wl == nil {
		return nil
	}
	s, err := surface.From(wl)
	if err != nil {
		return err
	}
	c := b.c
	for _, w := range c.windows {
		if w.top.Surface() == s {
			return c.markUrgent(w)
		}
	}
	return nil
}
