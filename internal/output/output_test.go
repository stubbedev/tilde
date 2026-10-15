package output

import (
	"errors"
	"fmt"
	"image"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/edid"
	"github.com/stubbedev/tilde/internal/loop"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cxo "github.com/stubbedev/tilde/internal/protocol/client/xdgoutput"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wlclient"
)

func TestScaleConversions(t *testing.T) {
	cases := []struct {
		s           Scale
		ceil        int32
		dev, logcal int
	}{
		{120, 1, 100, 100},
		{180, 2, 150, 100},
		{150, 2, 125, 100},
		{240, 2, 200, 100},
	}
	for _, c := range cases {
		if c.s.Ceil() != c.ceil || c.s.ToDevice(c.logcal) != c.dev || c.s.ToLogical(c.dev) != c.logcal {
			t.Errorf("scale %d: ceil %d, %d logical -> %d device -> %d logical", c.s, c.s.Ceil(), c.logcal, c.s.ToDevice(c.logcal), c.s.ToLogical(c.dev))
		}
	}
	if Scale(180).ToDevice(-1) != -1 || Scale(180).ToDevice(-2) != -3 || Scale(180).ToDevice(1) != 2 {
		t.Fatalf("1.5 times -1, -2 and 1 round half up to %d, %d and %d, want -1, -3 and 2", Scale(180).ToDevice(-1), Scale(180).ToDevice(-2), Scale(180).ToDevice(1))
	}
}

func TestAdjacentRectsStayAdjacent(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for range 1000 {
		s := Scale(120 + rng.IntN(240))
		x0, x1, x2 := rng.IntN(500), 0, 0
		x1 = x0 + 1 + rng.IntN(300)
		x2 = x1 + 1 + rng.IntN(300)
		a, b := s.Rect(image.Rect(x0, 0, x1, 10)), s.Rect(image.Rect(x1, 0, x2, 10))
		if a.Max.X != b.Min.X {
			t.Fatalf("scale %d: logical %d..%d..%d snaps to %v and %v with a gap or overlap", s, x0, x1, x2, a, b)
		}
	}
}

type fakeOutput struct {
	name string
	mode backend.Mode
}

func (f *fakeOutput) Name() string              { return f.name }
func (f *fakeOutput) Description() string       { return "fake " + f.name }
func (*fakeOutput) Identity() edid.Info         { return edid.Info{} }
func (f *fakeOutput) PhysicalSize() image.Point { return image.Pt(300, 200) }
func (f *fakeOutput) Modes() []backend.Mode     { return []backend.Mode{f.mode} }
func (f *fakeOutput) State() backend.OutputState {
	return backend.OutputState{Power: backend.PoweredOn, Mode: f.mode}
}
func (f *fakeOutput) Commit(backend.OutputState) error { return nil }
func (f *fakeOutput) ScheduleFrame() error             { return nil }
func (f *fakeOutput) Present(render.Region) error      { return nil }
func (f *fakeOutput) CursorPlane() (backend.CursorPlane, error) {
	return nil, backend.ErrNoCursorPlanes
}

func (f *fakeOutput) BeginFrame() (backend.Frame, error) {
	return backend.Frame{}, errors.New("no frames")
}

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (r registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type outputEvents struct{ log []string }

func (e *outputEvents) note(s string) error {
	e.log = append(e.log, s)
	return nil
}

func (e *outputEvents) Geometry(_ *cw.WlOutput, x, y, w, h int32, _ cw.WlOutputSubpixel, _, _ string, _ cw.WlOutputTransform) error {
	return e.note("geometry")
}

func (e *outputEvents) Mode(_ *cw.WlOutput, _ cw.WlOutputMode, w, h, r int32) error {
	return e.note("mode")
}

func (e *outputEvents) Done(*cw.WlOutput) error { return e.note("done") }

func (e *outputEvents) Scale(_ *cw.WlOutput, s int32) error {
	return e.note(fmt.Sprint("scale ", s))
}
func (e *outputEvents) Name(_ *cw.WlOutput, n string) error        { return e.note("name " + n) }
func (e *outputEvents) Description(_ *cw.WlOutput, _ string) error { return e.note("description") }

type xdgEvents struct{ log []string }

func (e *xdgEvents) LogicalPosition(_ *cxo.ZxdgOutputV1, x, y int32) error {
	e.log = append(e.log, fmt.Sprint("position ", x/100))
	return nil
}

func (e *xdgEvents) LogicalSize(_ *cxo.ZxdgOutputV1, w, h int32) error {
	e.log = append(e.log, fmt.Sprintf("size %dx%d", w/100, h/100))
	return nil
}

func (e *xdgEvents) Done(*cxo.ZxdgOutputV1) error {
	e.log = append(e.log, "done")
	return nil
}

func (e *xdgEvents) Name(*cxo.ZxdgOutputV1, string) error        { return nil }
func (e *xdgEvents) Description(*cxo.ZxdgOutputV1, string) error { return nil }

type outputRig struct {
	o       *Output
	on      func(func())
	d       *wlclient.Display
	reg     *cw.WlRegistry
	globals registry
}

func serveOutput(t *testing.T) *outputRig {
	t.Helper()
	dir := t.TempDir()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	disp, err := server.New(l, server.Options{RuntimeDir: dir, Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disp.Close() })
	m, err := New(disp)
	if err != nil {
		t.Fatal(err)
	}
	r := &outputRig{globals: registry{}}
	if r.o, err = m.Add(&fakeOutput{name: "FAKE-1", mode: backend.Mode{Size: image.Pt(300, 300), RefreshMHz: 60000}}, image.Pt(100, 0), 150); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	r.on = func(f func()) {
		ran := make(chan struct{})
		if err := l.Post(func() error { f(); close(ran); return nil }); err != nil {
			t.Fatal(err)
		}
		<-ran
	}
	t.Cleanup(func() {
		r.on(l.Stop)
		<-done
	})
	if r.d, err = wlclient.Connect(filepath.Join(dir, disp.Name())); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.d.Close() })
	if r.reg, err = r.d.WlDisplay().GetRegistry(r.globals); err != nil {
		t.Fatal(err)
	}
	if err := r.d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *outputRig) bind(t *testing.T, wlVersion, xdgVersion uint32, oe *outputEvents, xe *xdgEvents) *cw.WlOutput {
	t.Helper()
	wl, err := cw.NewWlOutput(r.d, wlVersion, oe)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := cxo.NewZxdgOutputManagerV1(r.d, xdgVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(r.reg.Bind(r.globals["wl_output"], wl), r.reg.Bind(r.globals["zxdg_output_manager_v1"], mgr)); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.GetXdgOutput(xe, wl); err != nil {
		t.Fatal(err)
	}
	if err := r.d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return wl
}

func TestXdgOutputDoneFollowsItsVersion(t *testing.T) {
	r := serveOutput(t)
	oe, xe := &outputEvents{}, &xdgEvents{}
	r.bind(t, 1, 2, oe, xe)
	if want := []string{"position 1", "size 2x2", "done"}; !slices.Equal(xe.log, want) {
		t.Fatalf("xdg_output v2 events %v, want %v", xe.log, want)
	}
	r.on(func() {
		if err := r.o.Update(image.Pt(0, 0), 300); err != nil {
			t.Error(err)
		}
	})
	if err := r.d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := xe.log[3:]; !slices.Equal(got, []string{"position 0", "size 1x1", "done"}) {
		t.Fatalf("xdg_output v2 after an update: %v", got)
	}
	r.bind(t, 1, 3, &outputEvents{}, &xdgEvents{})
}

func TestOutputAndXdgOutputEvents(t *testing.T) {
	r := serveOutput(t)
	o, on, d := r.o, r.on, r.d
	oe, xe := &outputEvents{}, &xdgEvents{}
	r.bind(t, 4, 3, oe, xe)
	wantOut := []string{"geometry", "mode", "scale 2", "name FAKE-1", "description", "done", "done"}
	if !slices.Equal(oe.log, wantOut) {
		t.Fatalf("wl_output events %v, want %v (the second done closes the xdg_output's first burst)", oe.log, wantOut)
	}
	if want := []string{"position 1", "size 2x2"}; !slices.Equal(xe.log, want) {
		t.Fatalf("xdg_output events %v, want %v: logical 200x200 at 100,0 and no xdg done at version 3", xe.log, want)
	}
	on(func() {
		if err := o.Update(image.Pt(0, 0), 300); err != nil {
			t.Error(err)
		}
	})
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := oe.log[len(wantOut):]; !slices.Equal(got, []string{"geometry", "mode", "scale 3", "name FAKE-1", "description", "done"}) {
		t.Fatalf("after an update: %v", got)
	}
	if got := xe.log[2:]; !slices.Equal(got, []string{"position 0", "size 1x1"}) {
		t.Fatalf("xdg_output after an update: %v", got)
	}
}

func TestPhysicalClientsSeeOutputsInXSpace(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	disp, err := server.New(l, server.Options{RuntimeDir: t.TempDir(), Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	defer disp.Close()
	m, err := New(disp)
	if err != nil {
		t.Fatal(err)
	}
	hidpi, err := m.Add(&fakeOutput{name: "EDP-1", mode: backend.Mode{Size: image.Pt(2560, 1440), RefreshMHz: 60000}}, image.Pt(0, 0), 240)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := m.Add(&fakeOutput{name: "DP-1", mode: backend.Mode{Size: image.Pt(1920, 1080), RefreshMHz: 60000}}, image.Pt(1280, 0), 120)
	if err != nil {
		t.Fatal(err)
	}
	if hidpi.XRect() != image.Rect(0, 0, 2560, 1440) || plain.XRect() != image.Rect(2560, 0, 4480, 1080) {
		t.Fatalf("X space %v and %v", hidpi.XRect(), plain.XRect())
	}
	if r, s := plain.view(nil); r != plain.Logical() || s != 120 {
		t.Fatalf("an ordinary client sees %v at scale %d", r, s)
	}
	if err := m.Physical(func(*server.Client) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if r, s := hidpi.view(nil); r != image.Rect(0, 0, 2560, 1440) || s != Unit {
		t.Fatalf("a physical client sees the HiDPI output as %v at scale %d", r, s)
	}
	if err := hidpi.Update(image.Pt(0, 0), 120); err != nil {
		t.Fatal(err)
	}
	if plain.XRect() != image.Rect(1280, 0, 3200, 1080) {
		t.Fatalf("after the largest scale dropped to 1, DP-1 sits at %v in X space", plain.XRect())
	}
}
