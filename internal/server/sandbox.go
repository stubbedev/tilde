package server

import (
	"errors"
	"fmt"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/wire"
)

type SecurityContext struct {
	Engine     string
	AppID      string
	InstanceID string
}

var ErrNotListening = errors.New("not a listening Unix socket")

type sandbox struct {
	d        *Display
	ctx      SecurityContext
	listenFD int
	closeFD  int
	accepts  *loop.Source
	closes   *loop.Source
}

func (c *Client) SecurityContext() (SecurityContext, bool) {
	if c.context == nil {
		return SecurityContext{}, false
	}
	return *c.context, true
}

func (c *Client) unsandboxed() bool {
	return c.context == nil
}

func (d *Display) Restrict(ifaces ...*wire.Interface) {
	for _, iface := range ifaces {
		if !slices.Contains(d.restricted, iface) {
			d.restricted = append(d.restricted, iface)
		}
	}
	for _, g := range d.globals {
		g.visible = d.visibility(g.iface, g.visible)
	}
}

func (d *Display) visibility(iface *wire.Interface, visible func(*Client) bool) func(*Client) bool {
	if !slices.Contains(d.restricted, iface) {
		return visible
	}
	return func(c *Client) bool { return c.unsandboxed() && visible(c) }
}

func ListeningSocket(fd int) error {
	domain, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotListening, err)
	}
	accepting, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotListening, err)
	}
	if domain != unix.AF_UNIX || accepting != 1 {
		return ErrNotListening
	}
	return nil
}

func (d *Display) AddSandbox(listenFD, closeFD int, ctx SecurityContext) error {
	if err := ListeningSocket(listenFD); err != nil {
		return err
	}
	if err := unix.SetNonblock(listenFD, true); err != nil {
		return fmt.Errorf("server: make the sandbox socket non-blocking: %w", err)
	}
	sb := &sandbox{d: d, ctx: ctx, listenFD: listenFD, closeFD: closeFD}
	var err error
	if sb.accepts, err = d.loop.AddFD(listenFD, loop.Readable, sb.accept); err != nil {
		return err
	}
	if sb.closes, err = d.loop.AddFD(closeFD, loop.Readable, func(loop.Events) error { return sb.close() }); err != nil {
		return errors.Join(err, sb.accepts.Remove())
	}
	d.sandboxes = append(d.sandboxes, sb)
	return nil
}

func (sb *sandbox) accept(loop.Events) error {
	for {
		fd, _, err := unix.Accept4(sb.listenFD, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		switch {
		case errors.Is(err, unix.EAGAIN):
			return nil
		case errors.Is(err, unix.EINTR), errors.Is(err, unix.ECONNABORTED):
			continue
		case err != nil:
			return fmt.Errorf("server: accept on a %s sandbox socket: %w", sb.ctx.Engine, err)
		}
		ctx := sb.ctx
		if _, err := sb.d.addClient(fd, &ctx); err != nil {
			return errors.Join(err, unix.Close(fd))
		}
	}
}

func (sb *sandbox) close() error {
	d := sb.d
	d.sandboxes = slices.DeleteFunc(d.sandboxes, func(x *sandbox) bool { return x == sb })
	return errors.Join(sb.accepts.Remove(), sb.closes.Remove(), unix.Close(sb.listenFD), unix.Close(sb.closeFD))
}
