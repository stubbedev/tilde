package wlclient

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/wire"
)

func pair(t *testing.T) (*Display, *wire.Conn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	d := &Display{conn: wire.NewConn(fds[0]), objects: map[wire.ObjectID]wire.Resource{}}
	if d.display, err = wayland.NewWlDisplay(d, 1, displayEvents{d}); err != nil {
		t.Fatal(err)
	}
	server := wire.NewConn(fds[1])
	t.Cleanup(func() {
		_ = d.Close()
		_ = server.Close()
	})
	return d, server
}

func emit(t *testing.T, server *wire.Conn, id wire.ObjectID, op wire.Opcode, encode func(*wire.Encoder)) {
	t.Helper()
	e := server.Begin(id, op)
	encode(e)
	if err := e.End(); err != nil {
		t.Fatal(err)
	}
	if err := server.Flush(); err != nil {
		t.Fatal(err)
	}
}

type keyboard struct{ wayland.WlKeyboardHandler }

func TestZombieDrainsEventsAndClosesTheirFDs(t *testing.T) {
	d, server := pair(t)
	kb, err := wayland.NewWlKeyboard(d, 9, keyboard{})
	if err != nil {
		t.Fatal(err)
	}
	id := kb.Base().ID()
	if err := kb.Release(); err != nil {
		t.Fatal(err)
	}
	if r, ok := d.Get(id); r != nil || !ok {
		t.Fatalf("Get on a destroyed object = %v, %v; want nil, true until delete_id", r, ok)
	}
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	emit(t, server, id, 0, func(e *wire.Encoder) {
		e.Uint(uint32(wayland.WlKeyboardKeymapFormatXkbV1))
		e.FD(p[0])
		e.Uint(4096)
	})
	emit(t, server, 1, 1, func(e *wire.Encoder) { e.Uint(uint32(id)) })
	if err := d.Dispatch(); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Get(id); ok {
		t.Fatal("delete_id did not free the zombie")
	}
	if got, err := d.NewID(); err != nil || got != id {
		t.Fatalf("NewID = %d, %v; want the freed id %d", got, err, id)
	}
	var st unix.Stat_t
	for fd := 3; fd < 64; fd++ {
		if fd == p[0] || fd == p[1] || fd == d.conn.FD() || fd == server.FD() {
			continue
		}
		if unix.Fstat(fd, &st) == nil && st.Ino == pipeInode(t, p[0]) {
			t.Fatalf("the keymap fd sent to a destroyed keyboard leaked as fd %d", fd)
		}
	}
}

func pipeInode(t *testing.T, fd int) uint64 {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	return st.Ino
}

func TestProtocolErrorEvent(t *testing.T) {
	d, server := pair(t)
	emit(t, server, 1, 0, func(e *wire.Encoder) {
		e.Object(1)
		e.Uint(3)
		e.Str("boom")
	})
	var pe *wire.ProtocolError
	if err := d.Dispatch(); !errors.As(err, &pe) || pe.Object != 1 || pe.Code != 3 || pe.Message != "boom" {
		t.Fatalf("Dispatch = %v, want the protocol error", err)
	}
}

func TestAddRejectsLiveClientIDs(t *testing.T) {
	d, _ := pair(t)
	if _, err := wayland.CreateWlCallback(d, 1, 1, func(*wayland.WlCallback) (wayland.WlCallbackHandler, error) {
		return struct{ wayland.WlCallbackHandler }{}, nil
	}); !errors.Is(err, wire.ErrInvalidNewID) {
		t.Fatalf("Add over wl_display = %v, want ErrInvalidNewID", err)
	}
}

func TestSocketPath(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	cases := []struct {
		vars map[string]string
		want string
	}{
		{map[string]string{"WAYLAND_DISPLAY": "wayland-1", "XDG_RUNTIME_DIR": "/run/user/1"}, "/run/user/1/wayland-1"},
		{map[string]string{"XDG_RUNTIME_DIR": "/run/user/1"}, "/run/user/1/wayland-0"},
		{map[string]string{"WAYLAND_DISPLAY": "/tmp/host.sock"}, "/tmp/host.sock"},
	}
	for _, c := range cases {
		if got, err := SocketPath(env(c.vars)); err != nil || got != c.want {
			t.Errorf("SocketPath(%v) = %q, %v; want %q", c.vars, got, err, c.want)
		}
	}
	if _, err := SocketPath(env(map[string]string{"WAYLAND_DISPLAY": "wayland-1"})); err == nil {
		t.Error("a relative display without XDG_RUNTIME_DIR resolved")
	}
}
