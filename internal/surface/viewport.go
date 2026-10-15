package surface

import (
	"image"

	svp "github.com/stubbedev/tilde/internal/protocol/server/viewporter"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const viewporterVersion = 1

func bindViewporter(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := svp.NewWpViewporter(c, id, version, viewporterRequests{})
	return err
}

type viewporterRequests struct{}

func (viewporterRequests) Destroy(*svp.WpViewporter) error {
	return nil
}

func (viewporterRequests) GetViewport(obj *svp.WpViewporter, res *svp.WpViewport, wl *sw.WlSurface) (svp.WpViewportHandler, error) {
	s, err := From(wl)
	if err != nil {
		return nil, err
	}
	if s.viewport != nil {
		return nil, obj.ProtocolError(svp.WpViewporterErrorViewportExists, "wl_surface@%d already has a viewport", wl.Base().ID())
	}
	v := &viewport{s: s, resource: res}
	s.viewport = v
	server.OnDestroy(res, func() error {
		if s.viewport == v {
			s.viewport = nil
			s.pending.set |= fieldSource | fieldDest
			s.pending.source, s.pending.dest = opt[render.FRect]{}, opt[image.Point]{}
		}
		return nil
	})
	return v, nil
}

type viewport struct {
	s        *Surface
	resource *svp.WpViewport
}

func (v *viewport) alive() error {
	if v.s.destroyed {
		return v.resource.ProtocolError(svp.WpViewportErrorNoSurface, "the wl_surface was destroyed")
	}
	return nil
}

func (v *viewport) Destroy(*svp.WpViewport) error {
	return nil
}

func (v *viewport) SetSource(_ *svp.WpViewport, x, y, w, h wire.Fixed) error {
	if err := v.alive(); err != nil {
		return err
	}
	unset := wire.FixedFromFloat(-1)
	st := &v.s.pending
	st.set |= fieldSource
	if x == unset && y == unset && w == unset && h == unset {
		st.source = opt[render.FRect]{}
		return nil
	}
	if x < 0 || y < 0 || w <= 0 || h <= 0 {
		return v.resource.ProtocolError(svp.WpViewportErrorBadValue, "invalid source %v,%v %vx%v", x.Float(), y.Float(), w.Float(), h.Float())
	}
	st.source = some(render.FRect{X: x.Float(), Y: y.Float(), W: w.Float(), H: h.Float()})
	return nil
}

func (v *viewport) SetDestination(_ *svp.WpViewport, w, h int32) error {
	if err := v.alive(); err != nil {
		return err
	}
	st := &v.s.pending
	st.set |= fieldDest
	if w == -1 && h == -1 {
		st.dest = opt[image.Point]{}
		return nil
	}
	if w <= 0 || h <= 0 {
		return v.resource.ProtocolError(svp.WpViewportErrorBadValue, "invalid destination %dx%d", w, h)
	}
	st.dest = some(image.Pt(int(w), int(h)))
	return nil
}

func (v *viewport) badSize(src render.FRect) error {
	return v.resource.ProtocolError(svp.WpViewportErrorBadSize, "source %vx%v is not integral and no destination is set", src.W, src.H)
}

func (v *viewport) outOfBuffer(src render.FRect, size image.Point) error {
	return v.resource.ProtocolError(svp.WpViewportErrorOutOfBuffer, "source %+v extends outside the %v buffer", src, size)
}
