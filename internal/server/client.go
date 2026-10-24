package server

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/loop"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/wire"
)

type Client struct {
	display    *Display
	conn       *wire.Conn
	source     *loop.Source
	creds      unix.Ucred
	objects    map[wire.ObjectID]wire.Resource
	lastID     wire.ObjectID
	reserved   map[wire.ObjectID]struct{}
	wlDisplay  *sw.WlDisplay
	registries []*sw.WlRegistry
	hooks      map[wire.ObjectID][]func() error
	writable   bool
	dropping   bool
	gone       bool
	context    *SecurityContext
}

func (d *Display) AddClient(fd int) (*Client, error) {
	return d.addClient(fd, nil)
}

func (d *Display) addClient(fd int, context *SecurityContext) (*Client, error) {
	creds, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return nil, fmt.Errorf("server: SO_PEERCRED on client fd %d: %w", fd, err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, fmt.Errorf("server: make client fd %d non-blocking: %w", fd, err)
	}
	c := &Client{
		display: d,
		conn:    wire.NewConn(fd),
		creds:   *creds,
		objects: map[wire.ObjectID]wire.Resource{},
		hooks:   map[wire.ObjectID][]func() error{},
		lastID:  wire.FirstServerID - 1,
		context: context,
	}
	c.conn.DeferSendErrors()
	if d.trace != nil {
		c.conn.SetTracer(c.trace)
	}
	if c.wlDisplay, err = sw.NewWlDisplay(c, 1, 1, displayRequests{c}); err != nil {
		return nil, err
	}
	if c.source, err = d.loop.AddFD(fd, loop.Readable, c.handle); err != nil {
		return nil, err
	}
	d.clients = append(d.clients, c)
	return c, nil
}

func OnDestroy(r wire.Resource, f func() error) {
	c := clientOf(r)
	id := r.Base().ID()
	c.hooks[id] = append(c.hooks[id], f)
}

func Track[T comparable](r wire.Resource, list *[]T, x T) {
	*list = append(*list, x)
	OnDestroy(r, func() error {
		*list = slices.DeleteFunc(*list, func(y T) bool { return y == x })
		return nil
	})
}

func clientOf(r wire.Resource) *Client {
	c, ok := r.Base().Peer().(*Client)
	if !ok {
		panic(fmt.Sprintf("server: %s@%d belongs to %T, not to a server client", r.Base().Interface().Name, r.Base().ID(), r.Base().Peer()))
	}
	return c
}

func (c *Client) runHooks(id wire.ObjectID) error {
	hooks := c.hooks[id]
	delete(c.hooks, id)
	var errs []error
	for _, f := range hooks {
		errs = append(errs, f())
	}
	return errors.Join(errs...)
}

func (c *Client) Fail(err error) error {
	if c.gone || c.dropping {
		return nil
	}
	c.dropping = true
	return c.display.loop.Post(func() error { return c.fail(err) })
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
	case id.ServerSide():
		if _, ok := c.reserved[id]; !ok {
			return fmt.Errorf("server: new %s with server id %d that lys-wm did not hand out: %w", r.Base().Interface().Name, id, wire.ErrInvalidNewID)
		}
		delete(c.reserved, id)
	}
	if _, used := c.objects[id]; used {
		return fmt.Errorf("server: new %s with id %d already in use: %w", r.Base().Interface().Name, id, wire.ErrInvalidNewID)
	}
	c.objects[id] = r
	return nil
}

func (c *Client) Remove(id wire.ObjectID) error {
	r, ok := c.objects[id]
	if !ok {
		return fmt.Errorf("server: remove unknown object %d", id)
	}
	r.Base().MarkRemoved()
	hooks := c.runHooks(id)
	delete(c.objects, id)
	if !id.ServerSide() && !c.gone {
		return errors.Join(hooks, c.wlDisplay.SendDeleteID(uint32(id)))
	}
	return hooks
}

func (c *Client) NewID() (wire.ObjectID, error) {
	if c.lastID == wire.LastServerID {
		return 0, errors.New("server: server object ids exhausted for this client")
	}
	c.lastID++
	if c.reserved == nil {
		c.reserved = map[wire.ObjectID]struct{}{}
	}
	c.reserved[c.lastID] = struct{}{}
	return c.lastID, nil
}

func (c *Client) handle(ev loop.Events) error {
	if ev&loop.Writable != 0 {
		if err := c.flush(); err != nil {
			return c.destroy()
		}
	}
	if ev&loop.Readable != 0 {
		err := c.conn.Fill()
		if teardown := c.dispatch(); c.gone {
			return teardown
		}
		if err != nil {
			return c.destroy()
		}
	}
	if ev&(loop.Hangup|loop.Error) != 0 {
		return c.destroy()
	}
	return nil
}

func (c *Client) dispatch() error {
	for !c.gone {
		m, err := c.conn.Next()
		if err != nil {
			return c.fail(err)
		}
		if m == nil {
			return nil
		}
		r, ok := c.objects[m.ID]
		if !ok {
			_ = m.Finish()
			return c.fail(invalidObject(c.wlDisplay, "invalid object %d", m.ID))
		}
		if err := r.Dispatch(m); err != nil {
			return c.fail(err)
		}
	}
	return nil
}

func (c *Client) fail(err error) error {
	object, code := wire.Resource(c.wlDisplay), sw.WlDisplayErrorImplementation
	var pe *wire.ProtocolError
	var me *wire.MessageError
	switch {
	case c.conn.Err() != nil:
		return c.destroy()
	case errors.As(err, &pe):
		object = c.resource(pe.Object)
		_ = c.wlDisplay.SendError(object, pe.Code, pe.Message)
		return c.destroy()
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
	return c.destroy()
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

func (c *Client) destroy() error {
	if c.gone {
		return nil
	}
	c.gone = true
	var errs []error
	for _, id := range slices.Sorted(maps.Keys(c.hooks)) {
		errs = append(errs, c.runHooks(id))
	}
	_ = c.conn.Flush()
	errs = append(errs, c.source.Remove(), c.conn.Close())
	c.display.removeClient(c)
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("server: tear down the client of pid %d: %w", c.creds.Pid, err)
	}
	return nil
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
