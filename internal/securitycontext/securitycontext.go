// Package securitycontext implements wp_security_context_manager_v1: sandbox
// engines such as Flatpak register a socket for their apps, and the server
// tags every client that connects through it, so the compositor can keep
// privileged globals from them.
package securitycontext

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	swsc "github.com/stubbedev/tilde/internal/protocol/server/securitycontext"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const version = 1

var Interface = swsc.WpSecurityContextManagerV1Interface

func New(d *server.Display) error {
	_, err := d.AddGlobal(swsc.WpSecurityContextManagerV1Interface, version, func(c *server.Client, id wire.ObjectID, v uint32) error {
		_, err := swsc.NewWpSecurityContextManagerV1(c, id, v, manager{d, c})
		return err
	})
	return err
}

type manager struct {
	d *server.Display
	c *server.Client
}

func (manager) Destroy(*swsc.WpSecurityContextManagerV1) error { return nil }

func (m manager) CreateListener(obj *swsc.WpSecurityContextManagerV1, res *swsc.WpSecurityContextV1, listenFD, closeFD int) (swsc.WpSecurityContextV1Handler, error) {
	closeBoth := func() error { return errors.Join(unix.Close(listenFD), unix.Close(closeFD)) }
	if _, sandboxed := m.c.SecurityContext(); sandboxed {
		return nil, errors.Join(obj.ProtocolError(swsc.WpSecurityContextManagerV1ErrorNested, "a sandboxed client cannot create a security context"), closeBoth())
	}
	listener, err := server.ListeningSocket(listenFD)
	if err != nil {
		return nil, errors.Join(obj.ProtocolError(swsc.WpSecurityContextManagerV1ErrorInvalidListenFD, fmt.Sprintf("listen_fd: %v", err)), closeBoth())
	}
	ctx := &context{d: m.d, listener: listener, closeFD: closeFD}
	server.OnDestroy(res, func() error {
		if ctx.committed {
			return nil
		}
		return closeBoth()
	})
	return ctx, nil
}

type context struct {
	d         *server.Display
	listener  server.Listener
	closeFD   int
	engine    *string
	appID     *string
	instance  *string
	committed bool
}

func (*context) Destroy(*swsc.WpSecurityContextV1) error { return nil }

func (x *context) setOnce(obj *swsc.WpSecurityContextV1, name, value string, field **string) error {
	if x.committed {
		return obj.ProtocolError(swsc.WpSecurityContextV1ErrorAlreadyUsed, "the security context was already committed")
	}
	if *field != nil {
		return obj.ProtocolError(swsc.WpSecurityContextV1ErrorAlreadySet, fmt.Sprintf("the %s is already set", name))
	}
	*field = &value
	return nil
}

func (x *context) SetSandboxEngine(obj *swsc.WpSecurityContextV1, name string) error {
	return x.setOnce(obj, "sandbox engine", name, &x.engine)
}

func (x *context) SetAppID(obj *swsc.WpSecurityContextV1, appID string) error {
	return x.setOnce(obj, "app ID", appID, &x.appID)
}

func (x *context) SetInstanceID(obj *swsc.WpSecurityContextV1, instanceID string) error {
	return x.setOnce(obj, "instance ID", instanceID, &x.instance)
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (x *context) Commit(obj *swsc.WpSecurityContextV1) error {
	if x.committed {
		return obj.ProtocolError(swsc.WpSecurityContextV1ErrorAlreadyUsed, "the security context was already committed")
	}
	x.committed = true
	meta := server.SecurityContext{Engine: orEmpty(x.engine), AppID: orEmpty(x.appID), InstanceID: orEmpty(x.instance)}
	if err := x.d.AddSandbox(x.listener, x.closeFD, meta); err != nil {
		return errors.Join(err, x.listener.Close(), unix.Close(x.closeFD))
	}
	return nil
}
