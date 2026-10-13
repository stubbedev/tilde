// Package testclient is a scripted Wayland client for integration tests: it
// maps toplevels, paints shm frames and keeps animating while frame
// callbacks arrive.
package testclient

import (
	"errors"
	"fmt"
	"image"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cxdg "github.com/stubbedev/tilde/internal/protocol/client/xdgshell"
	"github.com/stubbedev/tilde/internal/wlclient"
)

var defaultSize = image.Pt(64, 64)

type Client struct {
	t             testing.TB
	Display       *wlclient.Display
	Compositor    *cw.WlCompositor
	Subcompositor *cw.WlSubcompositor
	Shm           *cw.WlShm
	WM            *cxdg.XdgWmBase
}

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (r registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type shmEvents struct{}

func (shmEvents) Format(*cw.WlShm, cw.WlShmFormat) error { return nil }

type wmEvents struct{}

func (wmEvents) Ping(wm *cxdg.XdgWmBase, serial uint32) error { return wm.Pong(serial) }

func Connect(t testing.TB, socket string) *Client {
	t.Helper()
	d, err := wlclient.Connect(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	c := &Client{t: t, Display: d}
	globals := registry{}
	reg, err := d.WlDisplay().GetRegistry(globals)
	c.must(err)
	c.must(d.Roundtrip())
	c.Compositor, err = cw.NewWlCompositor(d, 6)
	c.must(err)
	c.Subcompositor, err = cw.NewWlSubcompositor(d, 1)
	c.must(err)
	c.Shm, err = cw.NewWlShm(d, 2, shmEvents{})
	c.must(err)
	c.WM, err = cxdg.NewXdgWmBase(d, 6, wmEvents{})
	c.must(err)
	c.must(errors.Join(
		reg.Bind(globals["wl_compositor"], c.Compositor),
		reg.Bind(globals["wl_subcompositor"], c.Subcompositor),
		reg.Bind(globals["wl_shm"], c.Shm),
		reg.Bind(globals["xdg_wm_base"], c.WM),
	))
	c.must(d.Roundtrip())
	return c
}

func (c *Client) must(err error) {
	c.t.Helper()
	if err != nil {
		c.t.Fatal(err)
	}
}

func (c *Client) DispatchUntil(what string, cond func() bool) {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	defer func() { c.must(c.Display.Flush()) }()
	for !cond() {
		if time.Now().After(deadline) {
			c.t.Fatalf("testclient: timed out waiting for %s", what)
		}
		c.must(c.Display.Flush())
		fds := []unix.PollFd{{Fd: int32(c.Display.Conn().FD()), Events: unix.POLLIN}} //nolint:gosec // file descriptors fit in int32
		n, err := unix.Poll(fds, 20)
		if err != nil && !errors.Is(err, unix.EINTR) {
			c.t.Fatalf("testclient: poll: %v", err)
		}
		if n > 0 {
			c.must(c.Display.ReadEvents())
		}
	}
}

type Painter func(pix []byte, stride int, size image.Point, frame int)

func Fill(b, g, r byte) Painter {
	return func(pix []byte, _ int, _ image.Point, _ int) {
		for i := 0; i < len(pix); i += 4 {
			pix[i], pix[i+1], pix[i+2], pix[i+3] = b, g, r, 0xff
		}
	}
}

type buffer struct {
	wl   *cw.WlBuffer
	pix  []byte
	busy bool
}

type release struct{ b *buffer }

func (r release) Release(*cw.WlBuffer) error {
	r.b.busy = false
	return nil
}

func (c *Client) Buffer(size image.Point, frame int, paint Painter) *cw.WlBuffer {
	c.t.Helper()
	b := c.buffers(size, 1)[0]
	paint(b.pix, size.X*4, size, frame)
	return b.wl
}

func (c *Client) buffers(size image.Point, n int) []*buffer {
	c.t.Helper()
	stride := size.X * 4
	frame := stride * size.Y
	fd, err := unix.MemfdCreate("testclient", unix.MFD_CLOEXEC)
	c.must(err)
	defer func() { _ = unix.Close(fd) }()
	c.must(unix.Ftruncate(fd, int64(n*frame)))
	m, err := unix.Mmap(fd, 0, n*frame, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	c.must(err)
	c.t.Cleanup(func() { _ = unix.Munmap(m) })
	pool, err := c.Shm.CreatePool(fd, int32(n*frame)) //nolint:gosec // test buffers are small
	c.must(err)
	out := make([]*buffer, n)
	for i := range out {
		b := &buffer{pix: m[i*frame : (i+1)*frame]}
		b.wl, err = pool.CreateBuffer(release{b}, int32(i*frame), int32(size.X), int32(size.Y), int32(stride), cw.WlShmFormatXrgb8888) //nolint:gosec // test buffers are small
		c.must(err)
		out[i] = b
	}
	c.must(pool.Destroy())
	return out
}

type Window struct {
	c          *Client
	Surface    *cw.WlSurface
	XdgSurface *cxdg.XdgSurface
	Toplevel   *cxdg.XdgToplevel
	paint      Painter
	pending    image.Point
	Size       image.Point
	Frames     int
	Closed     bool
	buffers    []*buffer
	drawn      int
}

func (c *Client) Window(title string, paint Painter) *Window {
	c.t.Helper()
	w := &Window{c: c, paint: paint}
	var err error
	w.Surface, err = c.Compositor.CreateSurface(surfaceEvents{})
	c.must(err)
	w.XdgSurface, err = c.WM.GetXdgSurface(xdgSurfaceEvents{w}, w.Surface)
	c.must(err)
	w.Toplevel, err = w.XdgSurface.GetToplevel(toplevelEvents{w})
	c.must(err)
	c.must(errors.Join(w.Toplevel.SetTitle(title), w.Toplevel.SetAppID("tilde.testclient"), w.Surface.Commit()))
	return w
}

func (w *Window) Mapped() bool {
	return w.drawn > 0
}

func (w *Window) draw() error {
	var b *buffer
	for _, candidate := range w.buffers {
		if !candidate.busy {
			b = candidate
			break
		}
	}
	if b == nil {
		return fmt.Errorf("testclient: the compositor holds every buffer of %v", w.Size)
	}
	w.paint(b.pix, w.Size.X*4, w.Size, w.drawn)
	b.busy = true
	w.drawn++
	if _, err := w.Surface.Frame(frameEvents{w}); err != nil {
		return err
	}
	return errors.Join(
		w.Surface.Attach(b.wl, 0, 0),
		w.Surface.DamageBuffer(0, 0, int32(w.Size.X), int32(w.Size.Y)), //nolint:gosec // test buffers are small
		w.Surface.Commit(),
	)
}

type surfaceEvents struct{}

func (surfaceEvents) Enter(*cw.WlSurface, *cw.WlOutput) error { return nil }

func (surfaceEvents) Leave(*cw.WlSurface, *cw.WlOutput) error { return nil }

func (surfaceEvents) PreferredBufferScale(*cw.WlSurface, int32) error { return nil }

func (surfaceEvents) PreferredBufferTransform(*cw.WlSurface, cw.WlOutputTransform) error { return nil }

type xdgSurfaceEvents struct{ w *Window }

func (e xdgSurfaceEvents) Configure(xs *cxdg.XdgSurface, serial uint32) error {
	w := e.w
	if err := xs.AckConfigure(serial); err != nil {
		return err
	}
	size := w.pending
	if size.X == 0 || size.Y == 0 {
		size = defaultSize
	}
	if size != w.Size || w.buffers == nil {
		w.Size = size
		w.buffers = w.c.buffers(size, 2)
	}
	return w.draw()
}

type toplevelEvents struct{ w *Window }

func (e toplevelEvents) Configure(_ *cxdg.XdgToplevel, width, height int32, _ []byte) error {
	e.w.pending = image.Pt(int(width), int(height))
	return nil
}

func (e toplevelEvents) Close(*cxdg.XdgToplevel) error {
	e.w.Closed = true
	return nil
}

func (toplevelEvents) ConfigureBounds(*cxdg.XdgToplevel, int32, int32) error { return nil }
func (toplevelEvents) WmCapabilities(*cxdg.XdgToplevel, []byte) error        { return nil }

type frameEvents struct{ w *Window }

func (e frameEvents) Done(*cw.WlCallback, uint32) error {
	e.w.Frames++
	return e.w.draw()
}
