package server

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	firstServerID = 0xff000000
	lastServerID  = 0xffffffff
)

type Client struct {
	display    *Display
	conn       *wire.Conn
	source     *loop.Source
	creds      unix.Ucred
	objects    map[wire.ObjectID]wire.Resource
	lastID     wire.ObjectID
	wlDisplay  *sw.WlDisplay
	registries []*sw.WlRegistry
	hooks      map[wire.ObjectID][]func()
	writable   bool
	gone       bool
}

func (d *Display) addClient(fd int) error {
	creds, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return fmt.Errorf("server: SO_PEERCRED on client fd %d: %w", fd, err)
	}
	c := &Client{
		display: d,
		conn:    wire.NewConn(fd),
		creds:   *creds,
		objects: map[wire.ObjectID]wire.Resource{},
		hooks:   map[wire.ObjectID][]func(){},
		lastID:  firstServerID - 1,
	}
	if d.trace != nil {
		c.conn.SetTracer(c.trace)
	}
	if c.wlDisplay, err = sw.NewWlDisplay(c, 1, 1, displayRequests{c}); err != nil {
		return err
	}
	if c.source, err = d.loop.AddFD(fd, loop.Readable, c.handle); err != nil {
		return err
	}
	d.clients = append(d.clients, c)
	return nil
}

func (c *Client) Display() *Display {
	return c.display
}

func (c *Client) OnDestroy(r wire.Resource, f func()) {
	id := r.Base().ID()
	c.hooks[id] = append(c.hooks[id], f)
}

func (c *Client) runHooks(id wire.ObjectID) {
	hooks := c.hooks[id]
	delete(c.hooks, id)
	for _, f := range hooks {
		f()
	}
}

func (c *Client) Creds() unix.Ucred {
	return c.creds
}

func (c *Client) Conn() *wire.Conn {
	return c.conn
}

func (c *Client) Get(id wire.ObjectID) (wire.Resource, bool) {
	r, ok := c.objects[id]
	return r, ok
}

func (c *Client) Add(r wire.Resource) error {
	id := r.Base().ID()
	switch {
	case id == 0:
		return fmt.Errorf("server: new %s with id 0: %w", r.Base().Interface().Name, wire.ErrInvalidNewID)
	case id >= firstServerID && id > c.lastID:
		return fmt.Errorf("server: new %s with unallocated server id %d: %w", r.Base().Interface().Name, id, wire.ErrInvalidNewID)
	}
	if _, used := c.objects[id]; used {
		return fmt.Errorf("server: new %s with id %d already in use: %w", r.Base().Interface().Name, id, wire.ErrInvalidNewID)
	}
	c.objects[id] = r
	return nil
}

func (c *Client) Remove(id wire.ObjectID) error {
	if _, ok := c.objects[id]; !ok {
		return fmt.Errorf("server: remove unknown object %d", id)
	}
	c.runHooks(id)
	delete(c.objects, id)
	if id < firstServerID {
		return c.wlDisplay.SendDeleteID(uint32(id))
	}
	return nil
}

func (c *Client) NewID() (wire.ObjectID, error) {
	if c.lastID == lastServerID {
		return 0, errors.New("server: server object ids exhausted for this client")
	}
	c.lastID++
	return c.lastID, nil
}

func (c *Client) handle(ev loop.Events) error {
	if ev&loop.Writable != 0 {
		if err := c.flush(); err != nil {
			c.destroy()
			return nil
		}
	}
	if ev&loop.Readable != 0 {
		err := c.conn.Fill()
		if c.dispatch(); c.gone {
			return nil
		}
		if err != nil {
			c.destroy()
			return nil
		}
	}
	if ev&(loop.Hangup|loop.Error) != 0 {
		c.destroy()
	}
	return nil
}

func (c *Client) dispatch() {
	for !c.gone {
		m, err := c.conn.Next()
		if err != nil {
			c.fail(err)
			return
		}
		if m == nil {
			return
		}
		r, ok := c.objects[m.ID]
		if !ok {
			_ = m.Finish()
			c.fail(invalidObject(c.wlDisplay, "invalid object %d", m.ID))
			return
		}
		if err := r.Dispatch(m); err != nil {
			c.fail(err)
			return
		}
	}
}

func (c *Client) fail(err error) {
	object, code := wire.Resource(c.wlDisplay), sw.WlDisplayErrorImplementation
	var pe *wire.ProtocolError
	var me *wire.MessageError
	switch {
	case c.conn.Err() != nil:
		c.destroy()
		return
	case errors.As(err, &pe):
		object = c.resource(pe.Object)
		_ = c.wlDisplay.SendError(object, pe.Code, pe.Message)
		c.destroy()
		return
	case errors.As(err, &me):
		object, code = c.resource(me.Object), sw.WlDisplayErrorInvalidMethod
		for _, target := range []error{wire.ErrUnknownObject, wire.ErrWrongInterface, wire.ErrNullObject, wire.ErrInvalidNewID} {
			if errors.Is(err, target) {
				code = sw.WlDisplayErrorInvalidObject
			}
		}
	case errors.Is(err, wire.ErrInvalidNewID):
		code = sw.WlDisplayErrorInvalidObject
	}
	_ = c.wlDisplay.SendError(object, uint32(code), err.Error())
	c.destroy()
}

func (c *Client) resource(id wire.ObjectID) wire.Resource {
	if r, ok := c.objects[id]; ok {
		return r
	}
	return c.wlDisplay
}

func (c *Client) flush() error {
	if err := c.conn.Err(); err != nil {
		return err
	}
	err := c.conn.Flush()
	switch {
	case errors.Is(err, wire.ErrWouldBlock):
		if !c.writable {
			c.writable = true
			return c.source.Modify(loop.Readable | loop.Writable)
		}
		return nil
	case err != nil:
		return err
	}
	if c.writable {
		c.writable = false
		return c.source.Modify(loop.Readable)
	}
	return nil
}

func (c *Client) destroy() {
	if c.gone {
		return
	}
	c.gone = true
	for _, id := range slices.Sorted(maps.Keys(c.hooks)) {
		c.runHooks(id)
	}
	_ = c.conn.Flush()
	_ = c.source.Remove()
	_ = c.conn.Close()
	c.display.removeClient(c)
}

func (c *Client) trace(incoming bool, id wire.ObjectID, opcode wire.Opcode, body []byte) {
	d := c.display
	b := fmt.Appendf(d.traceBuf[:0], "[%10.3f] {%d} ", float64(time.Since(d.start).Microseconds())/1000, c.creds.Pid)
	if !incoming {
		b = append(b, " -> "...)
	}
	r, ok := c.objects[id]
	if !ok {
		b = fmt.Appendf(b, "unknown@%d.%d(?)\n", id, opcode)
	} else {
		iface := r.Base().Interface()
		specs := iface.Requests
		if !incoming {
			specs = iface.Events
		}
		if int(opcode) < len(specs) {
			b = fmt.Appendf(b, "%s@%d.%s(", iface.Name, id, specs[opcode].Name)
			b = wire.AppendArgs(b, specs[opcode].Args, body)
			b = append(b, ")\n"...)
		} else {
			b = fmt.Appendf(b, "%s@%d.%d(?)\n", iface.Name, id, opcode)
		}
	}
	d.traceBuf = b
	_, _ = d.trace.Write(b)
}
