package xwayland

import (
	"fmt"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	sxs "github.com/stubbedev/tilde/internal/protocol/server/xwaylandshell"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	shellVersion = 1
	surfaceRole  = "xwayland_surface_v1"
)

type Shell struct {
	global      *server.Global
	bySerial    map[uint64]*surface.Surface
	associated  func(serial uint64, s *surface.Surface) error
	dissociated func(serial uint64) error
}

func newShell(d *server.Display, visible func(*server.Client) bool, associated func(uint64, *surface.Surface) error, dissociated func(uint64) error) (*Shell, error) {
	sh := &Shell{bySerial: map[uint64]*surface.Surface{}, associated: associated, dissociated: dissociated}
	var err error
	if sh.global, err = d.AddGlobalFor(sxs.XwaylandShellV1Interface, shellVersion, visible, sh.bind); err != nil {
		return nil, fmt.Errorf("xwayland: %w", err)
	}
	return sh, nil
}

func (sh *Shell) Surface(serial uint64) (*surface.Surface, bool) {
	s, ok := sh.bySerial[serial]
	return s, ok
}

func (sh *Shell) close() error {
	return sh.global.Remove()
}

func (sh *Shell) bind(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxs.NewXwaylandShellV1(c, id, version, shellRequests{sh, c})
	return err
}

type shellRequests struct {
	sh *Shell
	c  *server.Client
}

func (shellRequests) Destroy(*sxs.XwaylandShellV1) error {
	return nil
}

func (r shellRequests) GetXwaylandSurface(obj *sxs.XwaylandShellV1, _ *sxs.XwaylandSurfaceV1, wl *sw.WlSurface) (sxs.XwaylandSurfaceV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	x := &xsurface{sh: r.sh, s: s}
	if err := s.SetRole(x); err != nil {
		return nil, obj.ProtocolError(sxs.XwaylandShellV1ErrorRole, err.Error())
	}
	r.c.OnDestroy(wl, x.forget)
	return x, nil
}

type xsurface struct {
	sh              *Shell
	s               *surface.Surface
	pending, serial uint64
}

func (*xsurface) RoleName() string {
	return surfaceRole
}

func (x *xsurface) Committed(*surface.Surface) error {
	if x.pending == 0 || x.serial != 0 {
		return nil
	}
	x.serial = x.pending
	x.sh.bySerial[x.serial] = x.s
	return x.sh.associated(x.serial, x.s)
}

func (x *xsurface) SetSerial(obj *sxs.XwaylandSurfaceV1, lo, hi uint32) error {
	serial := uint64(hi)<<32 | uint64(lo)
	switch {
	case serial == 0:
		return obj.ProtocolError(sxs.XwaylandSurfaceV1ErrorInvalidSerial, "serial 0 names no X window")
	case x.pending != 0:
		return obj.ProtocolError(sxs.XwaylandSurfaceV1ErrorAlreadyAssociated, fmt.Sprintf("the surface already has serial %d", x.pending))
	}
	x.pending = serial
	return nil
}

func (*xsurface) Destroy(*sxs.XwaylandSurfaceV1) error {
	return nil
}

func (x *xsurface) forget() error {
	if x.serial == 0 || x.sh.bySerial[x.serial] != x.s {
		return nil
	}
	delete(x.sh.bySerial, x.serial)
	return x.sh.dissociated(x.serial)
}
