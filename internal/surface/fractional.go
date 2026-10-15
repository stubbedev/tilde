package surface

import (
	sfs "github.com/stubbedev/tilde/internal/protocol/server/fractionalscale"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const fractionalVersion = 1

func bindFractional(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sfs.NewWpFractionalScaleManagerV1(c, id, version, fractionalManagerRequests{})
	return err
}

type fractionalManagerRequests struct{}

func (fractionalManagerRequests) Destroy(*sfs.WpFractionalScaleManagerV1) error {
	return nil
}

func (fractionalManagerRequests) GetFractionalScale(obj *sfs.WpFractionalScaleManagerV1, res *sfs.WpFractionalScaleV1, wl *sw.WlSurface) (sfs.WpFractionalScaleV1Handler, error) {
	s, err := From(wl)
	if err != nil {
		return nil, err
	}
	if s.fractional != nil {
		return nil, obj.ProtocolError(sfs.WpFractionalScaleManagerV1ErrorFractionalScaleExists, "wl_surface@%d already has a fractional scale object", wl.Base().ID())
	}
	f := &fractional{resource: res}
	s.fractional = f
	server.OnDestroy(res, func() error {
		if s.fractional == f {
			s.fractional = nil
		}
		return nil
	})
	if s.preferred != 0 {
		if err := res.SendPreferredScale(s.preferred); err != nil {
			return nil, err
		}
	}
	return f, nil
}

type fractional struct {
	resource *sfs.WpFractionalScaleV1
}

func (*fractional) Destroy(*sfs.WpFractionalScaleV1) error {
	return nil
}
