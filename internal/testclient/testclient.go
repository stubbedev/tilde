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

	ccs "github.com/stubbedev/tilde/internal/protocol/client/cursorshape"
	cfs "github.com/stubbedev/tilde/internal/protocol/client/fractionalscale"
	cvp "github.com/stubbedev/tilde/internal/protocol/client/viewporter"
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
	Viewporter    *cvp.WpViewporter
	Fractional    *cfs.WpFractionalScaleManagerV1
	CursorShape   *ccs.WpCursorShapeManagerV1
	Outputs       []*cw.WlOutput
	registry      *cw.WlRegistry
	globals       *registry
}

type global struct{ name, version uint32 }

type registry struct {
	byName  map[string]global
	outputs []uint32
}

func (r *registry) Global(_ *cw.WlRegistry, name uint32, iface string, version uint32) error {
	if iface == "wl_output" {
		r.outputs = append(r.outputs, name)
	}
	r.byName[iface] = global{name, version}
	return nil
}

func (r *registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

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
	reg, err := d.WlDisplay().GetRegistry(&registry{byName: map[string]global{}})
	c.must(err)
	c.must(d.Roundtrip())
	globals, ok := reg.Handler().(*registry)
	if !ok {
		t.Fatalf("testclient: registry handler is %T", reg.Handler())
	}
	c.registry, c.globals = reg, globals
	c.Compositor, err = cw.NewWlCompositor(d, 6)
	c.must(err)
	c.Subcompositor, err = cw.NewWlSubcompositor(d, 1)
	c.must(err)
	c.Shm, err = cw.NewWlShm(d, 2, shmEvents{})
	c.must(err)
	c.WM, err = cxdg.NewXdgWmBase(d, 6, wmEvents{})
	c.must(err)
	c.Viewporter, err = cvp.NewWpViewporter(d, 1)
	c.must(err)
	c.Fractional, err = cfs.NewWpFractionalScaleManagerV1(d, 1)
	c.must(err)
	c.CursorShape, err = ccs.NewWpCursorShapeManagerV1(d, 2)
	c.must(err)
	c.must(errors.Join(
		reg.Bind(globals.byName["wl_compositor"].name, c.Compositor),
		reg.Bind(globals.byName["wl_subcompositor"].name, c.Subcompositor),
		reg.Bind(globals.byName["wl_shm"].name, c.Shm),
		reg.Bind(globals.byName["xdg_wm_base"].name, c.WM),
		reg.Bind(globals.byName["wp_viewporter"].name, c.Viewporter),
		reg.Bind(globals.byName["wp_fractional_scale_manager_v1"].name, c.Fractional),
		reg.Bind(globals.byName["wp_cursor_shape_manager_v1"].name, c.CursorShape),
	))
	for _, name := range globals.outputs {
		o, err := cw.NewWlOutput(d, 4, outputEvents{})
		c.must(err)
		c.must(reg.Bind(name, o))
		c.Outputs = append(c.Outputs, o)
	}
	c.must(d.Roundtrip())
	return c
}

type outputEvents struct{}

func (outputEvents) Geometry(*cw.WlOutput, int32, int32, int32, int32, cw.WlOutputSubpixel, string, string, cw.WlOutputTransform) error {
	return nil
}
func (outputEvents) Mode(*cw.WlOutput, cw.WlOutputMode, int32, int32, int32) error { return nil }
func (outputEvents) Done(*cw.WlOutput) error                                       { return nil }
func (outputEvents) Scale(*cw.WlOutput, int32) error                               { return nil }
func (outputEvents) Name(*cw.WlOutput, string) error                               { return nil }
func (outputEvents) Description(*cw.WlOutput, string) error                        { return nil }

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

type Scaling uint8

const (
	Unscaled Scaling = iota
	IntegerScale
	FractionalScale
)

type Window struct {
	c            *Client
	Surface      *cw.WlSurface
	XdgSurface   *cxdg.XdgSurface
	Toplevel     *cxdg.XdgToplevel
	Popup        *cxdg.XdgPopup
	Placed       image.Rectangle
	Dismissed    bool
	destroyed    bool
	paint        Painter
	scaling      Scaling
	viewport     *cvp.WpViewport
	pending      image.Point
	Logical      image.Point
	Size         image.Point
	BufferScale  int32
	Preferred120 uint32
	Entered      int
	Frames       int
	Closed       bool
	buffers      []*buffer
	drawn        int
}

func (c *Client) Window(title string, paint Painter) *Window {
	c.t.Helper()
	return c.ScaledWindow(title, Unscaled, paint)
}

func (c *Client) ScaledWindow(title string, scaling Scaling, paint Painter) *Window {
	c.t.Helper()
	w := &Window{c: c, paint: paint, scaling: scaling, BufferScale: 1, Preferred120: 120}
	var err error
	w.Surface, err = c.Compositor.CreateSurface(surfaceEvents{w})
	c.must(err)
	if scaling == FractionalScale {
		w.viewport, err = c.Viewporter.GetViewport(w.Surface)
		c.must(err)
		_, err = c.Fractional.GetFractionalScale(fractionalEvents{w}, w.Surface)
		c.must(err)
	}
	w.XdgSurface, err = c.WM.GetXdgSurface(xdgSurfaceEvents{w}, w.Surface)
	c.must(err)
	w.Toplevel, err = w.XdgSurface.GetToplevel(toplevelEvents{w})
	c.must(err)
	c.must(errors.Join(w.Toplevel.SetTitle(title), w.Toplevel.SetAppID("tilde.testclient"), w.Surface.Commit()))
	return w
}

type Placement struct {
	Size       image.Point
	Anchor     image.Rectangle
	Edge       cxdg.XdgPositionerAnchor
	Gravity    cxdg.XdgPositionerGravity
	Adjustment cxdg.XdgPositionerConstraintAdjustment
}

func (c *Client) Positioner(p Placement) *cxdg.XdgPositioner {
	c.t.Helper()
	pos, err := c.WM.CreatePositioner()
	c.must(err)
	c.must(errors.Join(
		pos.SetSize(int32(p.Size.X), int32(p.Size.Y)),                                                               //nolint:gosec // test sizes are small
		pos.SetAnchorRect(int32(p.Anchor.Min.X), int32(p.Anchor.Min.Y), int32(p.Anchor.Dx()), int32(p.Anchor.Dy())), //nolint:gosec // test sizes are small
		pos.SetAnchor(p.Edge),
		pos.SetGravity(p.Gravity),
		pos.SetConstraintAdjustment(p.Adjustment),
	))
	return pos
}

type Grab struct {
	Seat   *cw.WlSeat
	Serial uint32
}

func (c *Client) Popup(parent *Window, p Placement, grab *Grab, paint Painter) *Window {
	c.t.Helper()
	w := &Window{c: c, paint: paint, BufferScale: 1, Preferred120: 120}
	var err error
	w.Surface, err = c.Compositor.CreateSurface(surfaceEvents{w})
	c.must(err)
	w.XdgSurface, err = c.WM.GetXdgSurface(xdgSurfaceEvents{w}, w.Surface)
	c.must(err)
	pos := c.Positioner(p)
	w.Popup, err = w.XdgSurface.GetPopup(popupEvents{w}, parent.XdgSurface, pos)
	c.must(err)
	c.must(pos.Destroy())
	if grab != nil {
		c.must(w.Popup.Grab(grab.Seat, grab.Serial))
	}
	c.must(w.Surface.Commit())
	return w
}

func (w *Window) Destroy() error {
	w.destroyed = true
	var role error
	if w.Popup != nil {
		role = w.Popup.Destroy()
	} else {
		role = w.Toplevel.Destroy()
	}
	return errors.Join(role, w.XdgSurface.Destroy(), w.Surface.Destroy())
}

func (w *Window) Mapped() bool {
	return w.drawn > 0
}

func (w *Window) draw() error {
	if w.destroyed {
		return nil
	}
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

type quietSurface struct{}

func (quietSurface) Enter(*cw.WlSurface, *cw.WlOutput) error { return nil }

func (quietSurface) Leave(*cw.WlSurface, *cw.WlOutput) error { return nil }

func (quietSurface) PreferredBufferScale(*cw.WlSurface, int32) error { return nil }

func (quietSurface) PreferredBufferTransform(*cw.WlSurface, cw.WlOutputTransform) error { return nil }

func (c *Client) Surface() *cw.WlSurface {
	c.t.Helper()
	s, err := c.Compositor.CreateSurface(quietSurface{})
	c.must(err)
	return s
}

type surfaceEvents struct{ w *Window }

func (e surfaceEvents) Enter(*cw.WlSurface, *cw.WlOutput) error {
	e.w.Entered++
	return nil
}

func (e surfaceEvents) Leave(*cw.WlSurface, *cw.WlOutput) error {
	e.w.Entered--
	return nil
}

func (e surfaceEvents) PreferredBufferScale(_ *cw.WlSurface, factor int32) error {
	if e.w.scaling == IntegerScale {
		e.w.BufferScale = factor
	}
	return nil
}

func (surfaceEvents) PreferredBufferTransform(*cw.WlSurface, cw.WlOutputTransform) error { return nil }

type fractionalEvents struct{ w *Window }

func (e fractionalEvents) PreferredScale(_ *cfs.WpFractionalScaleV1, scale uint32) error {
	e.w.Preferred120 = scale
	return nil
}

type xdgSurfaceEvents struct{ w *Window }

func (e xdgSurfaceEvents) Configure(xs *cxdg.XdgSurface, serial uint32) error {
	w := e.w
	if err := xs.AckConfigure(serial); err != nil {
		return err
	}
	logical := w.pending
	if logical.X == 0 || logical.Y == 0 {
		logical = defaultSize
	}
	w.Logical = logical
	size := logical.Mul(int(w.BufferScale))
	if w.scaling == FractionalScale {
		size = image.Pt((logical.X*int(w.Preferred120)+60)/120, (logical.Y*int(w.Preferred120)+60)/120)
		if err := w.viewport.SetDestination(int32(logical.X), int32(logical.Y)); err != nil { //nolint:gosec // test sizes are small
			return err
		}
	}
	if w.BufferScale != 1 {
		if err := w.Surface.SetBufferScale(w.BufferScale); err != nil {
			return err
		}
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

type popupEvents struct{ w *Window }

func (e popupEvents) Configure(_ *cxdg.XdgPopup, x, y, width, height int32) error {
	e.w.pending = image.Pt(int(width), int(height))
	e.w.Placed = image.Rect(int(x), int(y), int(x+width), int(y+height))
	return nil
}

func (e popupEvents) PopupDone(*cxdg.XdgPopup) error {
	e.w.Dismissed = true
	return nil
}

func (popupEvents) Repositioned(*cxdg.XdgPopup, uint32) error { return nil }

type frameEvents struct{ w *Window }

func (e frameEvents) Done(*cw.WlCallback, uint32) error {
	e.w.Frames++
	return e.w.draw()
}

func closeFD(fd int) error {
	return unix.Close(fd)
}
