package surface

import (
	"fmt"
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
		return nil, obj.ProtocolError(svp.WpViewporterErrorViewportExists, fmt.Sprintf("wl_surface@%d already has a viewport", wl.Base().ID()))
	}
	v := &viewport{s: s, resource: res}
	s.viewport = v
	server.OnDestroy(res, func() error {
		if s.viewport == v {
			s.viewport = nil
			s.pending.set |= fieldSource | fieldDest
			s.pending.hasSource, s.pending.hasDest = false, false
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
		st.hasSource = false
		return nil
	}
	if x < 0 || y < 0 || w <= 0 || h <= 0 {
		return v.resource.ProtocolError(svp.WpViewportErrorBadValue, fmt.Sprintf("invalid source %v,%v %vx%v", x.Float(), y.Float(), w.Float(), h.Float()))
	}
	st.source, st.hasSource = render.FRect{X: x.Float(), Y: y.Float(), W: w.Float(), H: h.Float()}, true
	return nil
}

func (v *viewport) SetDestination(_ *svp.WpViewport, w, h int32) error {
	if err := v.alive(); err != nil {
		return err
	}
	st := &v.s.pending
	st.set |= fieldDest
	if w == -1 && h == -1 {
		st.hasDest = false
		return nil
	}
	if w <= 0 || h <= 0 {
		return v.resource.ProtocolError(svp.WpViewportErrorBadValue, fmt.Sprintf("invalid destination %dx%d", w, h))
	}
	st.dest, st.hasDest = image.Pt(int(w), int(h)), true
	return nil
}

func (v *viewport) badSize(src render.FRect) error {
	return v.resource.ProtocolError(svp.WpViewportErrorBadSize, fmt.Sprintf("source %vx%v is not integral and no destination is set", src.W, src.H))
}

func (v *viewport) outOfBuffer(src render.FRect, size image.Point) error {
	return v.resource.ProtocolError(svp.WpViewportErrorOutOfBuffer, fmt.Sprintf("source %+v extends outside the %v buffer", src, size))
}
