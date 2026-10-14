package harness

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	displayID       wire.ObjectID = 1
	displaySync     wire.Opcode   = 0
	getRegistry     wire.Opcode   = 1
	registryBind    wire.Opcode   = 0
	registryGlobal  wire.Opcode   = 0
	callbackDone    wire.Opcode   = 0
	fuzzTimeout                   = 2 * time.Second
	createdSelector               = 0xf0
)

var errTimedOut = errors.New("timed out")

type rawClient struct {
	t    *testing.T
	conn *wire.Conn
	next wire.ObjectID
}

func dialRaw(t *testing.T, socket string) *rawClient {
	t.Helper()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Connect(fd, &unix.SockaddrUnix{Name: socket}); err != nil {
		t.Fatal(errors.Join(err, unix.Close(fd)))
	}
	r := &rawClient{t: t, conn: wire.NewConn(fd), next: 2}
	t.Cleanup(func() { _ = r.conn.Close() })
	return r
}

func (r *rawClient) newID() wire.ObjectID {
	id := r.next
	r.next++
	return id
}

func (r *rawClient) send(object wire.ObjectID, opcode wire.Opcode, encode func(*wire.Encoder)) error {
	e := r.conn.Begin(object, opcode)
	encode(e)
	return e.End()
}

func (r *rawClient) receive(deadline time.Time) (*wire.Message, error) {
	var filled error
	for {
		m, err := r.conn.Next()
		if err != nil || m != nil {
			return m, err
		}
		if filled != nil {
			return nil, filled
		}
		pfd := []unix.PollFd{{Fd: int32(r.conn.FD()), Events: unix.POLLIN}}
		n, err := unix.Poll(pfd, int(time.Until(deadline).Milliseconds()))
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, errTimedOut
		}
		filled = r.conn.Fill()
	}
}

func (r *rawClient) sync(deadline time.Time, onEvent func(*wire.Message)) error {
	cb := r.newID()
	if err := errors.Join(r.send(displayID, displaySync, func(e *wire.Encoder) { e.Uint(uint32(cb)) }), r.conn.Flush()); err != nil {
		return err
	}
	for {
		m, err := r.receive(deadline)
		if err != nil {
			return err
		}
		done := m.ID == cb && m.Opcode == callbackDone
		onEvent(m)
		_ = m.Finish()
		if done {
			return nil
		}
	}
}

func (r *rawClient) bindAll() []wire.ObjectID {
	r.t.Helper()
	registry := r.newID()
	if err := r.send(displayID, getRegistry, func(e *wire.Encoder) { e.Uint(uint32(registry)) }); err != nil {
		r.t.Fatal(err)
	}
	type advertised struct {
		name, version uint32
		iface         string
	}
	var globals []advertised
	if err := r.sync(time.Now().Add(fuzzTimeout), func(m *wire.Message) {
		if m.ID == registry && m.Opcode == registryGlobal {
			globals = append(globals, advertised{name: m.Uint(), iface: m.Str(), version: m.Uint()})
		}
	}); err != nil {
		r.t.Fatal(err)
	}
	objects := []wire.ObjectID{displayID, registry}
	for _, g := range globals {
		id := r.newID()
		if err := r.send(registry, registryBind, func(e *wire.Encoder) {
			e.Uint(g.name)
			e.Str(g.iface)
			e.Uint(g.version)
			e.Uint(uint32(id))
		}); err != nil {
			r.t.Fatal(err)
		}
		objects = append(objects, id)
	}
	if err := r.conn.Flush(); err != nil {
		r.t.Fatal(err)
	}
	return objects
}

func (r *rawClient) play(objects []wire.ObjectID, script []byte) {
	for len(script) >= 3 {
		selector, opcode, words := script[0], script[1], int(script[2]%32)
		script = script[3:]
		object := objects[int(selector)%len(objects)]
		if selector >= createdSelector {
			object = r.next + wire.ObjectID(selector-createdSelector)
		}
		payload := make([]byte, words*4)
		script = script[copy(payload, script):]
		err := r.send(object, wire.Opcode(opcode%16), func(e *wire.Encoder) {
			for i := 0; i < len(payload); i += 4 {
				e.Uint(binary.LittleEndian.Uint32(payload[i:]))
			}
		})
		if err != nil {
			return
		}
	}
}

func FuzzRequests(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 1, 50, 0, 0, 0})
	f.Add([]byte{2, 0, 1, 40, 0, 0, 0, 0xf0, 1, 2, 1, 0, 0, 0, 2, 0, 0, 0})
	f.Add([]byte{5, 1, 3, 60, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{1, 0, 4, 3, 0, 0, 0, 255, 255, 255, 255, 1, 0, 0, 0, 70, 0, 0, 0})
	f.Fuzz(func(t *testing.T, script []byte) {
		h := StartUntraced(t, small)
		r := dialRaw(t, h.Socket())
		r.play(r.bindAll(), script)
		if err := r.conn.Flush(); err == nil {
			_ = r.sync(time.Now().Add(fuzzTimeout), func(*wire.Message) {})
		}
		h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
		c := testclient.Connect(t, h.Socket())
		w := c.Window("survivor", quadrants)
		c.DispatchUntil("a window mapped after the fuzzed client", w.Mapped)
	})
}
