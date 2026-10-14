// Package server accepts Wayland clients and runs the core protocol objects.
package server

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	maxSocketPath     = len(unix.RawSockaddrUnix{}.Path) - 1
	maxDisplays       = 32
	globalRemoveGrace = 5 * time.Second
)

type Options struct {
	RuntimeDir string
	Trace      io.Writer
}

type Display struct {
	loop       *loop.Loop
	trace      io.Writer
	traceBuf   []byte
	start      time.Time
	name       string
	socketPath string
	lockPath   string
	listenFD   int
	lockFD     int
	bound      bool
	listener   *loop.Source
	clients    []*Client
	dead       []*Client
	globals    []*Global
	nextGlobal uint32
	serial     uint32
	closed     bool
}

func New(l *loop.Loop, opts Options) (*Display, error) {
	if opts.RuntimeDir == "" {
		return nil, errors.New("server: no runtime directory: XDG_RUNTIME_DIR is not set")
	}
	d := &Display{loop: l, trace: opts.Trace, start: time.Now(), listenFD: -1, lockFD: -1}
	if err := d.bind(opts.RuntimeDir); err != nil {
		return nil, err
	}
	var err error
	if d.listener, err = l.AddFD(d.listenFD, loop.Readable, d.accept); err != nil {
		_ = d.release()
		return nil, err
	}
	l.BeforeWait(d.flush)
	return d, nil
}

func (d *Display) bind(dir string) error {
	for n := range maxDisplays {
		name := fmt.Sprintf("wayland-%d", n)
		path := filepath.Join(dir, name)
		if len(path) > maxSocketPath {
			return fmt.Errorf("server: socket path %s is %d bytes; a Unix socket allows %d", path, len(path), maxSocketPath)
		}
		lockFD, err := unix.Open(path+".lock", unix.O_CREAT|unix.O_CLOEXEC|unix.O_RDWR, 0o660)
		if err != nil {
			return fmt.Errorf("server: open lock %s.lock: %w", path, err)
		}
		if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			_ = unix.Close(lockFD)
			if errors.Is(err, unix.EWOULDBLOCK) {
				continue
			}
			return fmt.Errorf("server: lock %s.lock: %w", path, err)
		}
		d.name, d.socketPath, d.lockPath, d.lockFD = name, path, path+".lock", lockFD
		if err := unix.Unlink(path); err != nil && !errors.Is(err, unix.ENOENT) {
			_ = d.release()
			return fmt.Errorf("server: remove stale socket %s: %w", path, err)
		}
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
		if err != nil {
			_ = d.release()
			return fmt.Errorf("server: socket: %w", err)
		}
		d.listenFD = fd
		if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
			_ = d.release()
			return fmt.Errorf("server: bind %s: %w", path, err)
		}
		d.bound = true
		if err := unix.Listen(fd, 128); err != nil {
			_ = d.release()
			return fmt.Errorf("server: listen on %s: %w", path, err)
		}
		return nil
	}
	return fmt.Errorf("server: wayland-0 to wayland-%d in %s are all in use", maxDisplays-1, dir)
}

func (d *Display) release() error {
	var errs []error
	if d.bound {
		errs = append(errs, unix.Unlink(d.socketPath))
		d.bound = false
	}
	if d.listenFD >= 0 {
		errs = append(errs, unix.Close(d.listenFD))
		d.listenFD = -1
	}
	if d.lockFD >= 0 {
		errs = append(errs, unix.Unlink(d.lockPath), unix.Close(d.lockFD))
		d.lockFD = -1
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("server: release %s: %w", d.socketPath, err)
	}
	return nil
}

func (d *Display) Name() string {
	return d.name
}

func (d *Display) Path() string {
	return d.socketPath
}

func (d *Display) NextSerial() uint32 {
	d.serial++
	return d.serial
}

func (d *Display) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	var errs []error
	for len(d.clients) > 0 {
		errs = append(errs, d.clients[0].destroy())
	}
	return errors.Join(append(errs, d.listener.Remove(), d.release())...)
}

func (d *Display) accept(loop.Events) error {
	for {
		fd, _, err := unix.Accept4(d.listenFD, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		switch {
		case errors.Is(err, unix.EAGAIN):
			return nil
		case errors.Is(err, unix.EINTR), errors.Is(err, unix.ECONNABORTED):
			continue
		case err != nil:
			return fmt.Errorf("server: accept on %s: %w", d.socketPath, err)
		}
		if _, err := d.AddClient(fd); err != nil {
			_ = unix.Close(fd)
			return err
		}
	}
}

func (d *Display) flush() error {
	for _, c := range d.clients {
		if err := c.flush(); err != nil {
			d.dead = append(d.dead, c)
		}
	}
	var errs []error
	for i, c := range d.dead {
		errs = append(errs, c.destroy())
		d.dead[i] = nil
	}
	d.dead = d.dead[:0]
	return errors.Join(errs...)
}

func (d *Display) removeClient(c *Client) {
	if i := slices.Index(d.clients, c); i >= 0 {
		d.clients = slices.Delete(d.clients, i, i+1)
	}
}

type BindFunc func(c *Client, id wire.ObjectID, version uint32) error

type Global struct {
	display *Display
	name    uint32
	iface   *wire.Interface
	version uint32
	bind    BindFunc
	visible func(*Client) bool
	removed bool
}

func everyone(*Client) bool { return true }

func (d *Display) AddGlobal(iface *wire.Interface, version uint32, bind BindFunc) (*Global, error) {
	return d.AddGlobalFor(iface, version, everyone, bind)
}

func (d *Display) AddGlobalFor(iface *wire.Interface, version uint32, visible func(*Client) bool, bind BindFunc) (*Global, error) {
	if err := wire.CheckVersion(iface, version); err != nil {
		return nil, fmt.Errorf("server: add global: %w", err)
	}
	d.nextGlobal++
	g := &Global{display: d, name: d.nextGlobal, iface: iface, version: version, bind: bind, visible: visible}
	d.globals = append(d.globals, g)
	for _, c := range d.clients {
		if !visible(c) {
			continue
		}
		for _, r := range c.registries {
			_ = r.SendGlobal(g.name, iface.Name, version)
		}
	}
	return g, nil
}

func (g *Global) Name() uint32 {
	return g.name
}

func (g *Global) Remove() error {
	if g.removed {
		return nil
	}
	g.removed = true
	d := g.display
	for _, c := range d.clients {
		if !g.visible(c) {
			continue
		}
		for _, r := range c.registries {
			_ = r.SendGlobalRemove(g.name)
		}
	}
	var timer *loop.Timer
	timer, err := d.loop.AddTimer(func() error {
		if i := slices.Index(d.globals, g); i >= 0 {
			d.globals = slices.Delete(d.globals, i, i+1)
		}
		return timer.Remove()
	})
	if err != nil {
		return fmt.Errorf("server: remove global %s (%d): %w", g.iface.Name, g.name, err)
	}
	return timer.Arm(globalRemoveGrace)
}

func (d *Display) global(name uint32) *Global {
	for _, g := range d.globals {
		if g.name == name {
			return g
		}
	}
	return nil
}

func invalidObject(object wire.Resource, format string, args ...any) error {
	return &wire.ProtocolError{Object: object.Base().ID(), Code: uint32(sw.WlDisplayErrorInvalidObject), Message: fmt.Sprintf(format, args...)}
}
