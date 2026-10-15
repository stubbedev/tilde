package shell

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cwlr "github.com/stubbedev/tilde/internal/protocol/client/wlrlayershell"
	cxd "github.com/stubbedev/tilde/internal/protocol/client/xdgdecoration"
	cxdg "github.com/stubbedev/tilde/internal/protocol/client/xdgshell"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shm"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wlclient"
)

type recorder struct {
	log        []string
	toplevels  []*Toplevel
	popups     []*Popup
	initial    func(t *Toplevel) error
	requests   []Request
	constraint image.Rectangle
	press      uint32
	layers     []*LayerSurface
	layerSize  func(*LayerSurface) image.Point
}

func (r *recorder) note(format string, args ...any) error {
	r.log = append(r.log, fmt.Sprintf(format, args...))
	return nil
}

func (r *recorder) ToplevelInitialCommit(t *Toplevel) error {
	if !slices.Contains(r.toplevels, t) {
		r.toplevels = append(r.toplevels, t)
	}
	_ = r.note("initial")
	if r.initial != nil {
		return r.initial(t)
	}
	return nil
}

func (r *recorder) ToplevelMapped(*Toplevel) error    { return r.note("mapped") }
func (r *recorder) ToplevelCommitted(*Toplevel) error { return r.note("committed") }
func (r *recorder) ToplevelUnmapped(*Toplevel) error  { return r.note("unmapped") }

func (r *recorder) ToplevelChanged(t *Toplevel, c Change) error {
	return r.note("changed %d %q %q", c, t.Title(), t.AppID())
}

func (r *recorder) ToplevelRequest(_ *Toplevel, req Request) error {
	r.requests = append(r.requests, req)
	return nil
}

func (r *recorder) ClientResponsive(_ *server.Client, responsive bool) error {
	return r.note("responsive %t", responsive)
}

func (r *recorder) PopupConstraint(PopupRoot) image.Rectangle { return r.constraint }

func (r *recorder) LayerCommitted(l *LayerSurface) error {
	if !slices.Contains(r.layers, l) {
		r.layers = append(r.layers, l)
	}
	if r.layerSize != nil {
		if err := l.Configure(r.layerSize(l)); err != nil {
			return err
		}
	}
	return r.note("layer committed %t", l.Mapped())
}

func (r *recorder) LayerUnmapped(*LayerSurface) error  { return r.note("layer unmapped") }
func (r *recorder) LayerDestroyed(*LayerSurface) error { return r.note("layer destroyed") }

func (r *recorder) PopupMapped(p *Popup) error {
	r.popups = append(r.popups, p)
	return r.note("popup mapped")
}

func (r *recorder) PopupUnmapped(*Popup) error { return r.note("popup unmapped") }

func (r *recorder) UserActive(_ *server.Client, serial uint32) bool {
	return r.press != 0 && serial == r.press
}

func (r *recorder) GrabChanged() error { return r.note("grab changed") }

func (r *recorder) LockChanged() error { return r.note("lock changed") }

func (r *recorder) LockOutputSize(*sw.WlOutput) (image.Point, bool) { return image.Pt(100, 50), true }

type noSurfaces struct{}

func (noSurfaces) Committed(*surface.Surface) error { return nil }
func (noSurfaces) Destroyed(*surface.Surface) error { return nil }

type env struct {
	t     *testing.T
	loop  *loop.Loop
	shell *Shell
	rec   *recorder
	dir   string
	disp  *server.Display
}

func setup(t *testing.T, configure ...func(*recorder)) *env {
	t.Helper()
	dir, err := os.MkdirTemp("", "tilde-")
	if err != nil {
		t.Fatal(err)
	}
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, loop: l, dir: dir, rec: &recorder{}}
	if e.disp, err = server.New(l, server.Options{RuntimeDir: dir, Report: func(err error) { t.Error(err) }}); err != nil {
		t.Fatal(err)
	}
	if _, err := surface.New(e.disp, noSurfaces{}); err != nil {
		t.Fatal(err)
	}
	if _, err := shm.New(e.disp, []render.Format{render.ARGB8888, render.XRGB8888}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.disp.AddGlobal(sw.WlSeatInterface, 1, func(c *server.Client, id wire.ObjectID, v uint32) error {
		_, err := sw.NewWlSeat(c, id, v, struct{ sw.WlSeatHandler }{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if e.shell, err = New(e.disp, l, e.rec); err != nil {
		t.Fatal(err)
	}
	e.shell.pingTimeout = 30 * time.Millisecond
	for _, f := range configure {
		f(e.rec)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		e.on(func() { l.Stop() })
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
		_ = e.disp.Close()
		_ = l.Close()
		_ = os.RemoveAll(dir)
	})
	return e
}

func (e *env) on(f func()) {
	ran := make(chan struct{})
	if err := e.loop.Post(func() error {
		f()
		close(ran)
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
	<-ran
}

func (e *env) log() []string {
	var out []string
	e.on(func() { out = slices.Clone(e.rec.log) })
	return out
}

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (r registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type shmEvents struct{}

func (shmEvents) Format(*cw.WlShm, cw.WlShmFormat) error { return nil }

type wmEvents struct{ pings []uint32 }

func (w *wmEvents) Ping(_ *cxdg.XdgWmBase, serial uint32) error {
	w.pings = append(w.pings, serial)
	return nil
}

type client struct {
	t    *testing.T
	d    *wlclient.Display
	comp *cw.WlCompositor
	sub  *cw.WlSubcompositor
	shm  *cw.WlShm
	wm   *cxdg.XdgWmBase
	wmE  *wmEvents
	seat *cw.WlSeat
	lsh  *cwlr.ZwlrLayerShellV1
	deco *cxd.ZxdgDecorationManagerV1
}

func (e *env) connect(wmVersion uint32) *client {
	e.t.Helper()
	d, err := wlclient.Connect(filepath.Join(e.dir, e.disp.Name()))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = d.Close() })
	c := &client{t: e.t, d: d, wmE: &wmEvents{}}
	globals := registry{}
	reg, err := d.WlDisplay().GetRegistry(globals)
	if err != nil {
		e.t.Fatal(err)
	}
	c.roundtrip()
	c.comp, err = cw.NewWlCompositor(d, 6)
	must(e.t, err)
	c.sub, err = cw.NewWlSubcompositor(d, 1)
	must(e.t, err)
	c.shm, err = cw.NewWlShm(d, 2, shmEvents{})
	must(e.t, err)
	c.wm, err = cxdg.NewXdgWmBase(d, wmVersion, c.wmE)
	must(e.t, err)
	c.seat, err = cw.NewWlSeat(d, 1, struct{ cw.WlSeatHandler }{})
	must(e.t, err)
	c.lsh, err = cwlr.NewZwlrLayerShellV1(d, 5)
	must(e.t, err)
	c.deco, err = cxd.NewZxdgDecorationManagerV1(d, 1)
	must(e.t, err)
	must(e.t, reg.Bind(globals["wl_compositor"], c.comp), reg.Bind(globals["wl_subcompositor"], c.sub),
		reg.Bind(globals["wl_shm"], c.shm), reg.Bind(globals["xdg_wm_base"], c.wm), reg.Bind(globals["wl_seat"], c.seat),
		reg.Bind(globals["zwlr_layer_shell_v1"], c.lsh), reg.Bind(globals["zxdg_decoration_manager_v1"], c.deco))
	c.roundtrip()
	return c
}

func must(t *testing.T, errs ...error) {
	t.Helper()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

func (c *client) roundtrip() {
	c.t.Helper()
	if err := c.d.Roundtrip(); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) protocolError(code uint32) *wire.ProtocolError {
	c.t.Helper()
	var pe *wire.ProtocolError
	if err := c.d.Roundtrip(); !errors.As(err, &pe) || pe.Code != code {
		c.t.Fatalf("Roundtrip = %v, want protocol error code %d", err, code)
	}
	return pe
}

func (c *client) buffer(w, h int) *cw.WlBuffer {
	c.t.Helper()
	fd, err := unix.MemfdCreate("buf", unix.MFD_CLOEXEC)
	must(c.t, err)
	defer unix.Close(fd)
	must(c.t, unix.Ftruncate(fd, int64(w*h*4)))
	pool, err := c.shm.CreatePool(fd, int32(w*h*4))
	must(c.t, err)
	b, err := pool.CreateBuffer(struct{ cw.WlBufferHandler }{}, 0, int32(w), int32(h), int32(w*4), cw.WlShmFormatXrgb8888)
	must(c.t, err)
	return b
}

type toplevelEvents struct {
	configures []State
	caps       []byte
	closed     bool
}

func (t *toplevelEvents) Configure(_ *cxdg.XdgToplevel, w, h int32, states []byte) error {
	s := State{Size: image.Pt(int(w), int(h))}
	for i := 0; i+4 <= len(states); i += 4 {
		s.States = append(s.States, sxdg.XdgToplevelState(binary.NativeEndian.Uint32(states[i:])))
	}
	t.configures = append(t.configures, s)
	return nil
}

func (t *toplevelEvents) Close(*cxdg.XdgToplevel) error {
	t.closed = true
	return nil
}

func (t *toplevelEvents) ConfigureBounds(*cxdg.XdgToplevel, int32, int32) error { return nil }

func (t *toplevelEvents) WmCapabilities(_ *cxdg.XdgToplevel, caps []byte) error {
	t.caps = caps
	return nil
}

type xdgSurfaceEvents struct{ serials []uint32 }

func (x *xdgSurfaceEvents) Configure(_ *cxdg.XdgSurface, serial uint32) error {
	x.serials = append(x.serials, serial)
	return nil
}

type window struct {
	surf *cw.WlSurface
	xs   *cxdg.XdgSurface
	xsE  *xdgSurfaceEvents
	top  *cxdg.XdgToplevel
	topE *toplevelEvents
}

func (c *client) window() *window {
	c.t.Helper()
	w := &window{xsE: &xdgSurfaceEvents{}, topE: &toplevelEvents{}}
	var err error
	w.surf, err = c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	must(c.t, err)
	w.xs, err = c.wm.GetXdgSurface(w.xsE, w.surf)
	must(c.t, err)
	w.top, err = w.xs.GetToplevel(w.topE)
	must(c.t, err)
	return w
}

func (w *window) lastSerial() uint32 {
	return w.xsE.serials[len(w.xsE.serials)-1]
}

func TestDefaultConfigureAndMap(t *testing.T) {
	e := setup(t)
	c := e.connect(6)
	w := c.window()
	must(t, w.top.SetTitle("hello"), w.top.SetAppID("app"), w.surf.Commit())
	c.roundtrip()
	if len(w.topE.configures) != 1 || w.topE.configures[0].Size != (image.Point{}) || len(w.xsE.serials) != 1 {
		t.Fatalf("configures %v serials %v, want one default 0x0 configure", w.topE.configures, w.xsE.serials)
	}
	if len(w.topE.caps) != 8 {
		t.Fatalf("wm_capabilities %v, want fullscreen and maximize", w.topE.caps)
	}
	must(t, w.xs.AckConfigure(w.lastSerial()), w.xs.SetWindowGeometry(1, 2, 30, 40), w.surf.Attach(c.buffer(40, 50), 0, 0), w.surf.Commit())
	c.roundtrip()
	must(t, w.surf.Commit())
	c.roundtrip()
	want := []string{`changed 1 "hello" ""`, `changed 2 "hello" "app"`, "initial", "mapped", "committed"}
	if got := e.log(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	e.on(func() {
		if g := e.rec.toplevels[0].Geometry(); g != image.Rect(1, 2, 31, 42) {
			t.Errorf("geometry %v", g)
		}
	})
	must(t, w.surf.Attach(nil, 0, 0), w.surf.Commit(), w.surf.Commit())
	c.roundtrip()
	if got := e.log(); !slices.Equal(got[len(got)-2:], []string{"unmapped", "initial"}) {
		t.Fatalf("after unmap: %v, want unmapped then a fresh initial commit", got)
	}
}

func TestStateAppliesAfterAckAndCommit(t *testing.T) {
	e := setup(t, func(r *recorder) {
		r.initial = func(tl *Toplevel) error {
			_, err := tl.Configure(State{Size: image.Pt(640, 480), States: []ToplevelState{sxdg.XdgToplevelStateActivated, sxdg.XdgToplevelStateSuspended}})
			return err
		}
	})
	c := e.connect(5)
	w := c.window()
	must(t, w.surf.Commit())
	c.roundtrip()
	if len(w.topE.configures) != 1 || w.topE.configures[0].Size != image.Pt(640, 480) || !slices.Equal(w.topE.configures[0].States, []sxdg.XdgToplevelState{sxdg.XdgToplevelStateActivated}) {
		t.Fatalf("configures %v: want only the WM's configure, without the v6 suspended state", w.topE.configures)
	}
	must(t, w.xs.AckConfigure(w.lastSerial()))
	c.roundtrip()
	e.on(func() {
		if e.rec.toplevels[0].Current().Size != (image.Point{}) {
			t.Error("an acked configure applied before the commit")
		}
	})
	must(t, w.surf.Attach(c.buffer(640, 480), 0, 0), w.surf.Commit())
	c.roundtrip()
	e.on(func() {
		if cur := e.rec.toplevels[0].Current(); cur.Size != image.Pt(640, 480) || !cur.Has(sxdg.XdgToplevelStateActivated) {
			t.Errorf("current %+v after ack and commit", cur)
		}
		for _, size := range []image.Point{image.Pt(100, 100), image.Pt(200, 200)} {
			if _, err := e.rec.toplevels[0].Configure(State{Size: size}); err != nil {
				t.Error(err)
			}
		}
	})
	c.roundtrip()
	must(t, w.xs.AckConfigure(w.lastSerial()), w.surf.Commit())
	c.roundtrip()
	must(t, w.xs.AckConfigure(w.xsE.serials[1]))
	c.protocolError(uint32(sxdg.XdgSurfaceErrorInvalidSerial))
}

func TestXdgProtocolErrors(t *testing.T) {
	cases := []struct {
		name string
		do   func(c *client)
		code uint32
	}{
		{"buffer before configure", func(c *client) {
			w := c.window()
			must(c.t, w.surf.Commit(), w.surf.Attach(c.buffer(4, 4), 0, 0), w.surf.Commit())
		}, uint32(sxdg.XdgSurfaceErrorUnconfiguredBuffer)},
		{"commit without role", func(c *client) {
			s, err := c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
			must(c.t, err)
			_, err = c.wm.GetXdgSurface(&xdgSurfaceEvents{}, s)
			must(c.t, err, s.Commit())
		}, uint32(sxdg.XdgSurfaceErrorNotConstructed)},
		{"second toplevel", func(c *client) {
			w := c.window()
			_, err := w.xs.GetToplevel(&toplevelEvents{})
			must(c.t, err)
		}, uint32(sxdg.XdgSurfaceErrorAlreadyConstructed)},
		{"subsurface becomes xdg", func(c *client) {
			a, err := c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
			must(c.t, err)
			b, err := c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
			must(c.t, err)
			_, err = c.sub.GetSubsurface(b, a)
			must(c.t, err)
			_, err = c.wm.GetXdgSurface(&xdgSurfaceEvents{}, b)
			must(c.t, err)
		}, uint32(sxdg.XdgWmBaseErrorRole)},
		{"surface with a buffer", func(c *client) {
			s, err := c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
			must(c.t, err, s.Attach(c.buffer(4, 4), 0, 0), s.Commit())
			_, err = c.wm.GetXdgSurface(&xdgSurfaceEvents{}, s)
			must(c.t, err)
		}, uint32(sxdg.XdgWmBaseErrorInvalidSurfaceState)},
		{"xdg_surface destroyed first", func(c *client) { must(c.t, c.window().xs.Destroy()) }, uint32(sxdg.XdgSurfaceErrorDefunctRoleObject)},
		{"wm_base destroyed with surfaces", func(c *client) {
			c.window()
			must(c.t, c.wm.Destroy())
		}, uint32(sxdg.XdgWmBaseErrorDefunctSurfaces)},
		{"parent cycle", func(c *client) {
			a, b := c.window(), c.window()
			must(c.t, a.top.SetParent(b.top), b.top.SetParent(a.top))
		}, uint32(sxdg.XdgToplevelErrorInvalidParent)},
		{"negative size", func(c *client) { must(c.t, c.window().top.SetMinSize(-1, 0)) }, uint32(sxdg.XdgToplevelErrorInvalidSize)},
		{"min above max", func(c *client) {
			w := c.window()
			must(c.t, w.top.SetMinSize(100, 100), w.top.SetMaxSize(50, 200), w.surf.Commit())
		}, uint32(sxdg.XdgToplevelErrorInvalidSize)},
		{"bad resize edge", func(c *client) { must(c.t, c.window().top.Resize(c.seat, 0, 3)) }, uint32(sxdg.XdgToplevelErrorInvalidResizeEdge)},
		{"zero geometry", func(c *client) { must(c.t, c.window().xs.SetWindowGeometry(0, 0, 0, 10)) }, uint32(sxdg.XdgSurfaceErrorInvalidSize)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := setup(t).connect(6)
			tc.do(c)
			c.protocolError(tc.code)
		})
	}
}

func TestRequestsReachTheWindowManager(t *testing.T) {
	e := setup(t)
	c := e.connect(6)
	w := c.window()
	must(t, w.top.SetFullscreen(nil), w.top.UnsetMaximized(), w.top.SetMinimized(), w.top.Resize(c.seat, 7, cxdg.XdgToplevelResizeEdgeBottomRight), w.top.SetMaxSize(800, 600), w.top.SetMinSize(10, 20), w.surf.Commit())
	c.roundtrip()
	e.on(func() {
		want := []Request{SetFullscreen{On: true}, SetMaximized{On: false}, SetMinimized{}, Resize{Serial: 7, Edges: sxdg.XdgToplevelResizeEdgeBottomRight}}
		if !slices.Equal(e.rec.requests, want) {
			t.Errorf("requests %v, want %v", e.rec.requests, want)
		}
		tl := e.rec.toplevels[0]
		if tl.MinSize() != image.Pt(10, 20) || tl.MaxSize() != image.Pt(800, 600) {
			t.Errorf("size limits %v %v", tl.MinSize(), tl.MaxSize())
		}
		if err := tl.SendClose(); err != nil {
			t.Error(err)
		}
	})
	c.roundtrip()
	if !w.topE.closed {
		t.Fatal("close was not delivered")
	}
}

func TestPingMarksUnresponsiveClients(t *testing.T) {
	e := setup(t)
	c := e.connect(6)
	w := c.window()
	must(t, w.surf.Commit())
	c.roundtrip()
	ping := func() {
		e.on(func() {
			if err := e.rec.toplevels[0].Ping(); err != nil {
				t.Error(err)
			}
		})
		c.roundtrip()
	}
	ping()
	must(t, c.wm.Pong(c.wmE.pings[0]))
	c.roundtrip()
	time.Sleep(60 * time.Millisecond)
	if got := e.log(); slices.Contains(got, "responsive false") {
		t.Fatal("a client that answered was marked unresponsive")
	}
	ping()
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Contains(e.log(), "responsive false") {
		if time.Now().After(deadline) {
			t.Fatal("a client that never answered stayed responsive")
		}
		time.Sleep(5 * time.Millisecond)
	}
	must(t, c.wm.Pong(c.wmE.pings[1]))
	c.roundtrip()
	if got := e.log(); got[len(got)-1] != "responsive true" {
		t.Fatalf("a late pong did not restore responsiveness: %v", got)
	}
}

func TestDestroyingAParentHandsItsChildrenOnAndSaysSo(t *testing.T) {
	e := setup(t)
	c := e.connect(6)
	parent, child := c.window(), c.window()
	must(t, parent.top.SetTitle("parent"), child.top.SetTitle("child"), child.top.SetParent(parent.top))
	c.roundtrip()
	must(t, parent.top.Destroy(), parent.xs.Destroy())
	c.roundtrip()
	want := fmt.Sprintf(`changed %d "child" ""`, ChangedParent)
	got := e.log()
	if n := len(slices.DeleteFunc(slices.Clone(got), func(s string) bool { return s != want })); n != 2 {
		t.Fatalf("events %v: want the child's parent change reported again when its parent was destroyed", got)
	}
	e.on(func() {
		for _, tl := range e.rec.toplevels {
			if tl.Title() == "child" && tl.Parent() != nil {
				t.Error("the child still names its destroyed parent")
			}
		}
	})
}
