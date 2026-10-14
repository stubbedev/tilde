package output

import (
	"errors"
	"fmt"
	"image"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/backend"
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
func (f *fakeOutput) PhysicalSize() image.Point { return image.Pt(300, 200) }
func (f *fakeOutput) Modes() []backend.Mode     { return []backend.Mode{f.mode} }
func (f *fakeOutput) State() backend.OutputState {
	return backend.OutputState{Enabled: true, Mode: f.mode}
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

func TestOutputAndXdgOutputEvents(t *testing.T) {
	dir, err := os.MkdirTemp("", "tilde-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	disp, err := server.New(l, server.Options{RuntimeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer disp.Close()
	m, err := New(disp)
	if err != nil {
		t.Fatal(err)
	}
	o, err := m.Add(&fakeOutput{name: "FAKE-1", mode: backend.Mode{Size: image.Pt(300, 300), RefreshMHz: 60000}}, image.Pt(100, 0), 150)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	on := func(f func()) {
		ran := make(chan struct{})
		if err := l.Post(func() error { f(); close(ran); return nil }); err != nil {
			t.Fatal(err)
		}
		<-ran
	}
	defer func() {
		on(l.Stop)
		<-done
	}()
	d, err := wlclient.Connect(filepath.Join(dir, disp.Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	globals := registry{}
	reg, err := d.WlDisplay().GetRegistry(globals)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	oe := &outputEvents{}
	wl, err := cw.NewWlOutput(d, 4, oe)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := cxo.NewZxdgOutputManagerV1(d, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(reg.Bind(globals["wl_output"], wl), reg.Bind(globals["zxdg_output_manager_v1"], mgr)); err != nil {
		t.Fatal(err)
	}
	xe := &xdgEvents{}
	if _, err := mgr.GetXdgOutput(xe, wl); err != nil {
		t.Fatal(err)
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
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
