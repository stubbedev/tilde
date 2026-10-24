package compositor

import (
	"image"

	"github.com/stubbedev/lys-wm/internal/backend"
	spw "github.com/stubbedev/lys-wm/internal/protocol/server/pointerwarp"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const pointerWarpVersion = 1

func (c *Compositor) addPointerWarp(d *server.Display) error {
	_, err := d.AddGlobal(spw.WpPointerWarpV1Interface, pointerWarpVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := spw.NewWpPointerWarpV1(cl, id, version, pointerWarp{c, cl})
		return err
	})
	return err
}

type pointerWarp struct {
	c  *Compositor
	cl *server.Client
}

func (pointerWarp) Destroy(*spw.WpPointerWarpV1) error { return nil }

func (w pointerWarp) WarpPointer(_ *spw.WpPointerWarpV1, wl *sw.WlSurface, _ *sw.WlPointer, x, y wire.Fixed, serial uint32) error {
	c := w.c
	s, err := surface.From(wl)
	if err != nil {
		return err
	}
	local := point{x.Float(), y.Float()}
	inside := local.pixel().In(image.Rectangle{Max: s.Size()})
	org, shown := c.originOf(s)
	if !c.seat.PointerEntered(w.cl, serial) || !inside || !shown {
		return nil
	}
	return c.moveCursor(backend.Now(), org.global(local))
}
