package shell

import (
	"fmt"

	sxdd "github.com/stubbedev/tilde/internal/protocol/server/xdgdialog"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const dialogVersion = 1

func (sh *Shell) bindDialog(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxdd.NewXdgWmDialogV1(c, id, version, dialogs{sh, c})
	return err
}

type dialogs struct {
	sh *Shell
	c  *server.Client
}

func (dialogs) Destroy(*sxdd.XdgWmDialogV1) error { return nil }

func (d dialogs) GetXdgDialog(obj *sxdd.XdgWmDialogV1, res *sxdd.XdgDialogV1, top *sxdg.XdgToplevel) (sxdd.XdgDialogV1Handler, error) {
	t, ok := top.Handler().(*Toplevel)
	if !ok {
		return nil, fmt.Errorf("shell: xdg_toplevel@%d has handler %T", top.Base().ID(), top.Handler())
	}
	if t.dialog {
		return nil, obj.ProtocolError(sxdd.XdgWmDialogV1ErrorAlreadyUsed, "xdg_toplevel@%d already has an xdg_dialog_v1", top.Base().ID())
	}
	t.dialog = true
	server.OnDestroy(res, func() error {
		t.dialog = false
		return t.setModal(false)
	})
	return dialog{t}, nil
}

type dialog struct{ t *Toplevel }

func (dialog) Destroy(*sxdd.XdgDialogV1) error { return nil }

func (d dialog) SetModal(*sxdd.XdgDialogV1) error { return d.t.setModal(true) }

func (d dialog) UnsetModal(*sxdd.XdgDialogV1) error { return d.t.setModal(false) }

func (t *Toplevel) setModal(on bool) error {
	if t.destroyed || t.modal == on {
		return nil
	}
	t.modal = on
	return t.xs.base.shell.listener.ToplevelChanged(t, ChangedModal)
}

func (t *Toplevel) Modal() bool {
	return t.modal
}
