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

type Listener struct{ fd int }

func ListeningSocket(fd int) (Listener, error) {
	domain, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil {
		return Listener{}, fmt.Errorf("%w: %w", ErrNotListening, err)
	}
	accepting, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil {
		return Listener{}, fmt.Errorf("%w: %w", ErrNotListening, err)
	}
	if domain != unix.AF_UNIX || accepting != 1 {
		return Listener{}, ErrNotListening
	}
	return Listener{fd}, nil
}

func (l Listener) Close() error {
	return unix.Close(l.fd)
}

func (d *Display) AddSandbox(listener Listener, closeFD int, ctx SecurityContext) error {
	listenFD := listener.fd
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
	return sb.d.acceptOn(sb.listenFD, fmt.Sprintf("a %s sandbox socket", sb.ctx.Engine), &sb.ctx)
}

func (sb *sandbox) close() error {
	d := sb.d
	d.sandboxes = slices.DeleteFunc(d.sandboxes, func(x *sandbox) bool { return x == sb })
	return errors.Join(sb.accepts.Remove(), sb.closes.Remove(), unix.Close(sb.listenFD), unix.Close(sb.closeFD))
}
