// Package wlclient is the client side of a Wayland connection: the object
// map and dispatch that the generated client bindings run on.
package wlclient

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	maxClientID = wire.FirstServerID - 1
)

type Display struct {
	conn    *wire.Conn
	objects map[wire.ObjectID]wire.Resource
	deleted map[wire.ObjectID]struct{}
	free    []wire.ObjectID
	next    wire.ObjectID
	display *wayland.WlDisplay
}

func SocketPath(getenv func(string) string) (string, error) {
	name := getenv("WAYLAND_DISPLAY")
	if name == "" {
		name = "wayland-0"
	}
	if filepath.IsAbs(name) {
		return name, nil
	}
	dir := getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", fmt.Errorf("wlclient: WAYLAND_DISPLAY %q is relative and XDG_RUNTIME_DIR is not set", name)
	}
	return filepath.Join(dir, name), nil
}

func Connect(path string) (*Display, error) {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("wlclient: socket: %w", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wlclient: connect %s: %w", path, err)
	}
	return FromFD(fd)
}

func FromFD(fd int) (*Display, error) {
	var err error
	d := &Display{conn: wire.NewConn(fd), objects: map[wire.ObjectID]wire.Resource{}, deleted: map[wire.ObjectID]struct{}{}}
	if d.display, err = wayland.NewWlDisplay(d, 1, displayEvents{d}); err != nil {
		_ = d.conn.Close()
		return nil, err
	}
	return d, nil
}

func (d *Display) WlDisplay() *wayland.WlDisplay {
	return d.display
}

func (d *Display) Close() error {
	return d.conn.Close()
}

func (d *Display) Conn() *wire.Conn {
	return d.conn
}

func (d *Display) Get(id wire.ObjectID) (wire.Resource, bool) {
	r, ok := d.objects[id]
	if _, dead := r.(*zombie); dead {
		return nil, true
	}
	return r, ok
}

func (d *Display) Add(r wire.Resource) error {
	id := r.Base().ID()
	if id == 0 {
		return fmt.Errorf("wlclient: add %s@0: %w", r.Base().Interface().Name, wire.ErrInvalidNewID)
	}
	if prev, ok := d.objects[id]; ok {
		if _, dead := prev.(*zombie); !dead || !id.ServerSide() {
			return fmt.Errorf("wlclient: add %s@%d: id in use: %w", r.Base().Interface().Name, id, wire.ErrInvalidNewID)
		}
	}
	d.objects[id] = r
	return nil
}

func (d *Display) Remove(id wire.ObjectID) error {
	r, ok := d.objects[id]
	if !ok {
		return fmt.Errorf("wlclient: remove unknown object %d", id)
	}
	if _, gone := d.deleted[id]; gone {
		delete(d.deleted, id)
		d.release(id)
		return nil
	}
	d.objects[id] = &zombie{object: *r.Base()}
	return nil
}

func (d *Display) release(id wire.ObjectID) {
	delete(d.objects, id)
	if !id.ServerSide() {
		d.free = append(d.free, id)
	}
}

func (d *Display) NewID() (wire.ObjectID, error) {
	if n := len(d.free); n > 0 {
		id := d.free[n-1]
		d.free = d.free[:n-1]
		return id, nil
	}
	if d.next == maxClientID {
		return 0, errors.New("wlclient: client object ids exhausted")
	}
	d.next++
	return d.next, nil
}

func (d *Display) Flush() error {
	for {
		err := d.conn.Flush()
		if !errors.Is(err, wire.ErrWouldBlock) {
			return err
		}
		if err := d.poll(unix.POLLOUT); err != nil {
			return err
		}
	}
}

func (d *Display) poll(events int16) error {
	fds := []unix.PollFd{{Fd: int32(d.conn.FD()), Events: events}} //nolint:gosec // file descriptors fit in int32
	for {
		_, err := unix.Poll(fds, -1)
		if !errors.Is(err, unix.EINTR) {
			if err != nil {
				return fmt.Errorf("wlclient: poll: %w", err)
			}
			return nil
		}
	}
}

func closedByPeer(err error) bool {
	return errors.Is(err, unix.EPIPE) || errors.Is(err, unix.ECONNRESET)
}

func (d *Display) Dispatch() error {
	if err := d.Flush(); err != nil && !closedByPeer(err) {
		return err
	}
	if err := d.poll(unix.POLLIN); err != nil {
		return err
	}
	return d.ReadEvents()
}

func (d *Display) ReadEvents() error {
	err := d.conn.Fill()
	if pending := d.DispatchPending(); pending != nil {
		return pending
	}
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("wlclient: server closed the connection: %w", err)
	}
	return err
}

func (d *Display) DispatchPending() error {
	for {
		m, err := d.conn.Next()
		if err != nil || m == nil {
			return err
		}
		r, ok := d.objects[m.ID]
		if !ok {
			_ = m.Finish()
			return fmt.Errorf("wlclient: event for unknown object %d", m.ID)
		}
		if err := r.Dispatch(m); err != nil {
			return err
		}
	}
}

func (d *Display) Roundtrip() error {
	done := &syncDone{}
	if _, err := d.display.Sync(done); err != nil {
		return err
	}
	for !done.done {
		if err := d.Dispatch(); err != nil {
			return err
		}
	}
	return nil
}

type syncDone struct{ done bool }

func (s *syncDone) Done(*wayland.WlCallback, uint32) error {
	s.done = true
	return nil
}

type displayEvents struct{ d *Display }

func (e displayEvents) Error(_ *wayland.WlDisplay, object wire.Resource, code uint32, message string) error {
	var id wire.ObjectID
	if object != nil {
		id = object.Base().ID()
	}
	return &wire.ProtocolError{Object: id, Code: code, Message: message}
}

func (e displayEvents) DeleteID(_ *wayland.WlDisplay, id uint32) error {
	oid := wire.ObjectID(id)
	switch r, ok := e.d.objects[oid]; {
	case !ok:
		return fmt.Errorf("wlclient: delete_id for unknown object %d", id)
	case isZombie(r):
		e.d.release(oid)
	default:
		e.d.deleted[oid] = struct{}{}
	}
	return nil
}

type zombie struct {
	object wire.Object
}

func isZombie(r wire.Resource) bool {
	_, dead := r.(*zombie)
	return dead
}

func (z *zombie) Base() *wire.Object {
	return &z.object
}

func (z *zombie) Dispatch(m *wire.Message) error {
	specs := z.object.Interface().Events
	op := z.object.Expect(m, specs)
	if op < 0 {
		return m.Finish()
	}
	var fds []int
	for _, a := range specs[op].Args {
		switch a.Type {
		case wire.ArgString:
			m.OptStr()
		case wire.ArgArray:
			m.Array()
		case wire.ArgFD:
			fds = append(fds, m.FD())
		case wire.ArgNewID:
			_ = m.Finish()
			return fmt.Errorf("wlclient: %s@%d.%s creates an object on a destroyed object", z.object.Interface().Name, z.object.ID(), specs[op].Name)
		default:
			m.Uint()
		}
	}
	err := m.Finish()
	if err == nil {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}
	return err
}
