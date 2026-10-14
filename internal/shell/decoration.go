package shell

import (
	"fmt"

	sxd "github.com/stubbedev/tilde/internal/protocol/server/xdgdecoration"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const decorationVersion = 1

func (sh *Shell) bindDecoration(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxd.NewZxdgDecorationManagerV1(c, id, version, decorationManager{})
	return err
}

type decorationManager struct{}

func (decorationManager) Destroy(*sxd.ZxdgDecorationManagerV1) error {
	return nil
}

func (decorationManager) GetToplevelDecoration(_ *sxd.ZxdgDecorationManagerV1, res *sxd.ZxdgToplevelDecorationV1, top *sxdg.XdgToplevel) (sxd.ZxdgToplevelDecorationV1Handler, error) {
	t, ok := top.Handler().(*Toplevel)
	if !ok {
		return nil, fmt.Errorf("shell: xdg_toplevel@%d has handler %T", top.Base().ID(), top.Handler())
	}
	switch {
	case t.decoration != nil:
		return nil, res.ProtocolError(sxd.ZxdgToplevelDecorationV1ErrorAlreadyConstructed, "the xdg_toplevel already has a decoration object")
	case t.xs.surface.Buffer() != nil:
		return nil, res.ProtocolError(sxd.ZxdgToplevelDecorationV1ErrorUnconfiguredBuffer, "the xdg_toplevel already has a buffer")
	}
	t.decoration = res
	t.xs.base.client.OnDestroy(res, func() error {
		t.decoration = nil
		return nil
	})
	d := decoration{t}
	return d, d.configure()
}

type decoration struct{ t *Toplevel }

func (d decoration) configure() error {
	t := d.t
	if err := t.xs.base.client.Sent(t.decoration.SendConfigure(sxd.ZxdgToplevelDecorationV1ModeServerSide)); err != nil {
		return err
	}
	if !t.xs.initialSent {
		return nil
	}
	_, err := t.Configure(t.requested)
	return err
}

func (decoration) Destroy(*sxd.ZxdgToplevelDecorationV1) error {
	return nil
}

func (d decoration) SetMode(obj *sxd.ZxdgToplevelDecorationV1, mode sxd.ZxdgToplevelDecorationV1Mode) error {
	if mode != sxd.ZxdgToplevelDecorationV1ModeClientSide && mode != sxd.ZxdgToplevelDecorationV1ModeServerSide {
		return obj.ProtocolError(sxd.ZxdgToplevelDecorationV1ErrorInvalidMode, fmt.Sprintf("mode %d", mode))
	}
	return d.configure()
}

func (d decoration) UnsetMode(*sxd.ZxdgToplevelDecorationV1) error {
	return d.configure()
}
