package surface

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/loop"
	cfs "github.com/stubbedev/tilde/internal/protocol/client/fractionalscale"
	cvp "github.com/stubbedev/tilde/internal/protocol/client/viewporter"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	sfs "github.com/stubbedev/tilde/internal/protocol/server/fractionalscale"
	svp "github.com/stubbedev/tilde/internal/protocol/server/viewporter"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/region"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wlclient"
)

type commit struct {
	s            *Surface
	attached     bool
	bufferDamage region.Region
	size         image.Point
	damage       region.Region
	offset       image.Point
	scale        int32
	opaque       region.Region
	input        region.Region
	stack        []*Surface
	subPos       image.Point
	source       render.FRect
}

type recorder struct {
	commits   []commit
	destroyed []*Surface
}

func (r *recorder) Committed(s *Surface) error {
	c := commit{s: s, attached: s.Attached(), bufferDamage: s.BufferDamage(), size: s.Size(), damage: s.Damage(), offset: s.Offset(), scale: s.Scale(), opaque: s.Opaque(), input: s.Input(), stack: slices.Collect(s.Stack()), source: s.BufferSource()}
	if s.Subsurface() != nil {
		c.subPos = s.Subsurface().Position()
	}
	r.commits = append(r.commits, c)
	return nil
}

func (r *recorder) Destroyed(s *Surface) error {
	r.destroyed = append(r.destroyed, s)
	return nil
}

type env struct {
	t          *testing.T
	loop       *loop.Loop
	disp       *server.Display
	rec        *recorder
	done       chan error
	client     *wlclient.Display
	comp       *cw.WlCompositor
	subcomp    *cw.WlSubcompositor
	viewporter *cvp.WpViewporter
	fractional *cfs.WpFractionalScaleManagerV1
}

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (r registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

func setup(t *testing.T, compositorVersion uint32) *env {
	t.Helper()
	dir, err := os.MkdirTemp("", "tilde-")
	if err != nil {
		t.Fatal(err)
	}
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, loop: l, rec: &recorder{}, done: make(chan error, 1)}
	if e.disp, err = server.New(l, server.Options{RuntimeDir: dir, Report: func(err error) { t.Error(err) }}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(e.disp, e.rec); err != nil {
		t.Fatal(err)
	}
	go func() { e.done <- l.Run() }()
	t.Cleanup(func() {
		e.on(func() { l.Stop() })
		if err := <-e.done; err != nil {
			t.Errorf("Run: %v", err)
		}
		_ = e.disp.Close()
		_ = l.Close()
		_ = os.RemoveAll(dir)
	})
	if e.client, err = wlclient.Connect(filepath.Join(dir, e.disp.Name())); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.client.Close() })
	globals := registry{}
	reg, err := e.client.WlDisplay().GetRegistry(globals)
	if err != nil {
		t.Fatal(err)
	}
	e.roundtrip()
	if e.comp, err = cw.NewWlCompositor(e.client, compositorVersion); err != nil {
		t.Fatal(err)
	}
	if e.subcomp, err = cw.NewWlSubcompositor(e.client, 1); err != nil {
		t.Fatal(err)
	}
	if e.viewporter, err = cvp.NewWpViewporter(e.client, 1); err != nil {
		t.Fatal(err)
	}
	if e.fractional, err = cfs.NewWpFractionalScaleManagerV1(e.client, 1); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(reg.Bind(globals["wl_compositor"], e.comp), reg.Bind(globals["wl_subcompositor"], e.subcomp),
		reg.Bind(globals["wp_viewporter"], e.viewporter), reg.Bind(globals["wp_fractional_scale_manager_v1"], e.fractional)); err != nil {
		t.Fatal(err)
	}
	e.roundtrip()
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

func (e *env) roundtrip() {
	e.t.Helper()
	if err := e.client.Roundtrip(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) protocolError(code uint32) *wire.ProtocolError {
	e.t.Helper()
	var pe *wire.ProtocolError
	if err := e.client.Roundtrip(); !errors.As(err, &pe) || pe.Code != code {
		e.t.Fatalf("Roundtrip = %v, want protocol error code %d", err, code)
	}
	return pe
}

func (e *env) commits() []commit {
	var out []commit
	e.on(func() { out = slices.Clone(e.rec.commits) })
	return out
}

func find(t *testing.T, commits []commit, s *cw.WlSurface) commit {
	t.Helper()
	for _, c := range slices.Backward(commits) {
		if c.s.Resource().Base().ID() == s.Base().ID() {
			return c
		}
	}
	t.Fatalf("wl_surface@%d never committed", s.Base().ID())
	return commit{}
}

func (e *env) last(s *cw.WlSurface) commit {
	e.t.Helper()
	return find(e.t, e.commits(), s)
}

func (e *env) surface() *cw.WlSurface {
	e.t.Helper()
	s, err := e.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

type testBuffer struct {
	size image.Point
}

func (b testBuffer) Size() image.Point          { return b.size }
func (b testBuffer) Destroy(*sw.WlBuffer) error { return nil }

func (e *env) buffer(w, h int) *cw.WlBuffer {
	e.t.Helper()
	probe := e.surface()
	if err := probe.Commit(); err != nil {
		e.t.Fatal(err)
	}
	e.roundtrip()
	client := e.last(probe).s.Client()
	id, err := e.client.NewID()
	if err != nil {
		e.t.Fatal(err)
	}
	proxy, err := cw.CreateWlBuffer(e.client, id, 1, func(*cw.WlBuffer) (cw.WlBufferHandler, error) {
		return struct{ cw.WlBufferHandler }{}, nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.on(func() {
		if _, err := sw.CreateWlBuffer(client, id, 1, func(*sw.WlBuffer) (sw.WlBufferHandler, error) {
			return testBuffer{image.Pt(w, h)}, nil
		}); err != nil {
			e.t.Error(err)
		}
	})
	return proxy
}

func (e *env) region(add ...image.Rectangle) *cw.WlRegion {
	e.t.Helper()
	g, err := e.comp.CreateRegion()
	if err != nil {
		e.t.Fatal(err)
	}
	for _, r := range add {
		if err := g.Add(int32(r.Min.X), int32(r.Min.Y), int32(r.Dx()), int32(r.Dy())); err != nil {
			e.t.Fatal(err)
		}
	}
	return g
}

func must(t *testing.T, errs ...error) {
	t.Helper()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

func TestStateIsDoubleBuffered(t *testing.T) {
	e := setup(t, 6)
	s := e.surface()
	opaque := e.region(image.Rect(0, 0, 10, 10))
	input := e.region(image.Rect(2, 2, 4, 4))
	must(t, s.SetOpaqueRegion(opaque), s.SetInputRegion(input), s.SetBufferScale(2), s.Damage(1, 1, 3, 3))
	e.roundtrip()
	if len(e.commits()) != 0 {
		t.Fatal("pending state became visible without a commit")
	}
	must(t, opaque.Add(50, 50, 10, 10), s.Attach(e.buffer(40, 20), 0, 0), s.Commit())
	e.roundtrip()
	c := e.last(s)
	if c.size != image.Pt(20, 10) || c.scale != 2 {
		t.Fatalf("size %v at scale %d, want 20x10 at 2", c.size, c.scale)
	}
	if c.opaque.Area() != 100 {
		t.Fatalf("opaque area %d: a wl_region change after set_opaque_region leaked into the surface", c.opaque.Area())
	}
	if c.input.Area() != 4 || !c.damage.Contains(image.Pt(1, 1)) {
		t.Fatalf("input %v damage %v", c.input.Rects(), c.damage.Rects())
	}
	must(t, s.Commit())
	e.roundtrip()
	if c := e.last(s); !c.damage.Empty() || c.size != image.Pt(20, 10) || c.input.Area() != 4 {
		t.Fatalf("second commit: damage %v size %v input %d; damage must reset, the rest persist", c.damage.Rects(), c.size, c.input.Area())
	}
	must(t, s.SetInputRegion(nil), s.Commit())
	e.roundtrip()
	if c := e.last(s); c.input.Area() != 200 {
		t.Fatalf("a null input region covers %d pixels, want the whole 20x10 surface", c.input.Area())
	}
}

func TestBufferDamageMapsToSurfaceCoordinates(t *testing.T) {
	e := setup(t, 6)
	s := e.surface()
	must(t, s.Attach(e.buffer(40, 20), 0, 0), s.SetBufferScale(2), s.SetBufferTransform(cw.WlOutputTransform90), s.DamageBuffer(0, 0, 4, 2), s.Commit())
	e.roundtrip()
	c := e.last(s)
	if c.size != image.Pt(10, 20) {
		t.Fatalf("size %v, want 10x20 for a 40x20 buffer at scale 2 rotated 90 degrees", c.size)
	}
	want := render.Rotate270.Rect(image.Rect(0, 0, 4, 2), image.Pt(40, 20))
	want = image.Rect(want.Min.X/2, want.Min.Y/2, (want.Max.X+1)/2, (want.Max.Y+1)/2)
	if got := c.damage.Bounds(); got != want {
		t.Fatalf("damage %v, want %v", got, want)
	}
	if got := c.bufferDamage.Bounds(); got != image.Rect(0, 0, 4, 2) {
		t.Fatalf("buffer damage %v, want the damaged buffer rect back", got)
	}
	must(t, s.Damage(0, 0, 1, 1), s.Commit())
	e.roundtrip()
	surfaceRect := image.Rect(0, 0, 2, 2)
	if got, want := e.last(s).bufferDamage.Bounds(), render.Rotate90.Rect(surfaceRect, image.Pt(20, 40)); got != want {
		t.Fatalf("surface damage maps to buffer damage %v, want %v", got, want)
	}
	if e.last(s).attached {
		t.Fatal("a commit without attach reports an attached buffer")
	}
}

func TestSurfaceProtocolErrors(t *testing.T) {
	cases := []struct {
		name    string
		version uint32
		send    func(e *env, s *cw.WlSurface) error
		code    sw.WlSurfaceError
	}{
		{"scale zero", 6, func(_ *env, s *cw.WlSurface) error { return s.SetBufferScale(0) }, sw.WlSurfaceErrorInvalidScale},
		{"bad transform", 6, func(_ *env, s *cw.WlSurface) error { return s.SetBufferTransform(9) }, sw.WlSurfaceErrorInvalidTransform},
		{"attach offset at v5", 6, func(e *env, s *cw.WlSurface) error { return s.Attach(e.buffer(4, 4), 1, 0) }, sw.WlSurfaceErrorInvalidOffset},
		{"size not a multiple of scale", 6, func(e *env, s *cw.WlSurface) error {
			return errors.Join(s.Attach(e.buffer(5, 4), 0, 0), s.SetBufferScale(2), s.Commit())
		}, sw.WlSurfaceErrorInvalidSize},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, c.version)
			s := e.surface()
			must(t, c.send(e, s))
			if pe := e.protocolError(uint32(c.code)); pe.Object != s.Base().ID() {
				t.Fatalf("error on object %d, want the surface %d", pe.Object, s.Base().ID())
			}
		})
	}
}

func TestAttachOffsetBeforeVersion5(t *testing.T) {
	e := setup(t, 4)
	s := e.surface()
	must(t, s.Attach(e.buffer(4, 4), 3, -2), s.Commit())
	e.roundtrip()
	if c := e.last(s); c.offset != image.Pt(3, -2) {
		t.Fatalf("offset %v, want (3, -2) from attach on a version 4 surface", c.offset)
	}
}

type frameDone struct{ done []uint32 }

func (f *frameDone) Done(_ *cw.WlCallback, ms uint32) error {
	f.done = append(f.done, ms)
	return nil
}

func TestFrameCallbacksWaitForTheCompositor(t *testing.T) {
	e := setup(t, 6)
	s := e.surface()
	f := &frameDone{}
	cb, err := s.Frame(f)
	must(t, err, s.Commit())
	e.roundtrip()
	if len(f.done) != 0 {
		t.Fatal("frame callback fired before the compositor drew a frame")
	}
	e.on(func() {
		if err := find(t, e.rec.commits, s).s.FrameDone(1234); err != nil {
			t.Error(err)
		}
	})
	e.roundtrip()
	if !slices.Equal(f.done, []uint32{1234}) {
		t.Fatalf("done = %v", f.done)
	}
	if r, ok := e.client.Get(cb.Base().ID()); ok && r != nil {
		t.Fatal("the callback id was not deleted after done")
	}
}

func (e *env) subsurface(child, parent *cw.WlSurface) *cw.WlSubsurface {
	e.t.Helper()
	sub, err := e.subcomp.GetSubsurface(child, parent)
	if err != nil {
		e.t.Fatal(err)
	}
	return sub
}

func (e *env) commitCount(s *cw.WlSurface) int {
	n := 0
	for _, c := range e.commits() {
		if c.s.Resource().Base().ID() == s.Base().ID() {
			n++
		}
	}
	return n
}

func TestSynchronizedSubsurfacesWaitForTheirParent(t *testing.T) {
	e := setup(t, 6)
	parent, child, grandchild := e.surface(), e.surface(), e.surface()
	sub := e.subsurface(child, parent)
	grand := e.subsurface(grandchild, child)
	must(t, grand.SetDesync(), sub.SetPosition(5, 6))
	must(t, child.Damage(0, 0, 1, 1), child.Commit(), grandchild.Commit())
	e.roundtrip()
	if e.commitCount(child) != 0 || e.commitCount(grandchild) != 0 {
		t.Fatal("a synchronized subtree applied before its parent committed")
	}
	must(t, parent.Commit())
	e.roundtrip()
	if e.commitCount(child) != 1 || e.commitCount(grandchild) != 1 {
		t.Fatalf("after the parent commit: child %d, grandchild %d applies, want 1 each", e.commitCount(child), e.commitCount(grandchild))
	}
	if c := e.last(child); c.subPos != image.Pt(5, 6) || !c.damage.Contains(image.Pt(0, 0)) {
		t.Fatalf("child position %v damage %v", c.subPos, c.damage.Rects())
	}
	must(t, child.Damage(2, 2, 1, 1), child.Commit(), sub.SetDesync())
	e.roundtrip()
	if e.commitCount(child) != 2 || !e.last(child).damage.Contains(image.Pt(2, 2)) {
		t.Fatal("set_desync did not apply the cached state")
	}
	must(t, grandchild.Commit())
	e.roundtrip()
	if e.commitCount(grandchild) != 2 {
		t.Fatal("a desync subsurface under a desync parent did not apply on its own commit")
	}
}

func TestSubsurfaceStackingAppliesOnParentCommit(t *testing.T) {
	e := setup(t, 6)
	parent, a, b := e.surface(), e.surface(), e.surface()
	e.subsurface(a, parent)
	sb := e.subsurface(b, parent)
	must(t, parent.Commit())
	e.roundtrip()
	ids := func(stack []*Surface) []wire.ObjectID {
		var out []wire.ObjectID
		for _, s := range stack {
			out = append(out, s.Resource().Base().ID())
		}
		return out
	}
	pid, aid, bid := parent.Base().ID(), a.Base().ID(), b.Base().ID()
	if got := ids(e.last(parent).stack); !slices.Equal(got, []wire.ObjectID{pid, aid, bid}) {
		t.Fatalf("initial stack %v, want parent then a, b on top", got)
	}
	must(t, sb.PlaceBelow(parent))
	e.roundtrip()
	must(t, parent.Commit())
	e.roundtrip()
	if got := ids(e.last(parent).stack); !slices.Equal(got, []wire.ObjectID{bid, pid, aid}) {
		t.Fatalf("stack %v, want b below the parent", got)
	}
	must(t, sb.PlaceAbove(a), parent.Commit())
	e.roundtrip()
	if got := ids(e.last(parent).stack); !slices.Equal(got, []wire.ObjectID{pid, aid, bid}) {
		t.Fatalf("stack %v, want b above a", got)
	}
}

func TestSubsurfaceErrors(t *testing.T) {
	t.Run("own parent", func(t *testing.T) {
		e := setup(t, 6)
		s := e.surface()
		e.subsurface(s, s)
		e.protocolError(uint32(sw.WlSubcompositorErrorBadParent))
	})
	t.Run("cycle", func(t *testing.T) {
		e := setup(t, 6)
		a, b := e.surface(), e.surface()
		e.subsurface(b, a)
		e.subsurface(a, b)
		e.protocolError(uint32(sw.WlSubcompositorErrorBadParent))
	})
	t.Run("second role", func(t *testing.T) {
		e := setup(t, 6)
		a, b, c := e.surface(), e.surface(), e.surface()
		e.subsurface(c, a)
		e.subsurface(c, b)
		e.protocolError(uint32(sw.WlSubcompositorErrorBadSurface))
	})
	t.Run("place relative to a stranger", func(t *testing.T) {
		e := setup(t, 6)
		a, b, stranger := e.surface(), e.surface(), e.surface()
		sub := e.subsurface(b, a)
		must(t, sub.PlaceAbove(stranger))
		e.protocolError(uint32(sw.WlSubsurfaceErrorBadSurface))
	})
}

func TestDestroyingSurfaces(t *testing.T) {
	e := setup(t, 6)
	parent, child := e.surface(), e.surface()
	sub := e.subsurface(child, parent)
	must(t, parent.Commit(), child.Commit(), parent.Commit())
	e.roundtrip()
	must(t, parent.Destroy())
	e.roundtrip()
	var parentGone bool
	e.on(func() {
		parentGone = len(e.rec.destroyed) == 1 && e.rec.destroyed[0].Resource().Base().ID() == parent.Base().ID()
		if s := find(t, e.rec.commits, child).s; s.Subsurface().Parent() != nil || s.Subsurface().Synchronized() {
			t.Error("a subsurface whose parent died still has a parent")
		}
	})
	if !parentGone {
		t.Fatal("destroying the parent was not reported")
	}
	must(t, sub.Destroy(), child.Commit())
	e.roundtrip()
	other := e.surface()
	e.subsurface(child, other)
	e.roundtrip()
	if err := e.client.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		e.on(func() { n = len(e.rec.destroyed) })
		if n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d surfaces reported destroyed after disconnect, want 3", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type fakeRole struct{ name string }

func (r fakeRole) RoleName() string         { return r.name }
func (r fakeRole) Committed(*Surface) error { return nil }

type refusingRole struct{}

func (refusingRole) RoleName() string { return "xdg_toplevel" }
func (refusingRole) Committed(s *Surface) error {
	return s.Resource().ProtocolError(sw.WlSurfaceErrorInvalidSize, "refused")
}

func TestAClientErrorInAHeldUpdateDropsOnlyThatClient(t *testing.T) {
	e := setup(t, 6)
	s := e.surface()
	must(t, s.Commit())
	e.roundtrip()
	var hold *Hold
	e.on(func() {
		surf := find(t, e.rec.commits, s).s
		if err := surf.SetRole(refusingRole{}); err != nil {
			t.Error(err)
		}
		surf.AddHooks(&Hooks{Commit: func(u *Update) error { hold = u.Hold(); return nil }})
	})
	must(t, s.Commit())
	e.roundtrip()
	e.on(func() {
		if err := hold.Release(); err != nil {
			t.Errorf("Release = %v; a client's error reached the loop", err)
		}
	})
	e.protocolError(uint32(sw.WlSurfaceErrorInvalidSize))
}

func TestRolesAreExclusive(t *testing.T) {
	e := setup(t, 6)
	s := e.surface()
	must(t, s.Commit())
	e.roundtrip()
	e.on(func() {
		surf := find(t, e.rec.commits, s).s
		first := fakeRole{"xdg_toplevel"}
		if err := surf.SetRole(first); err != nil {
			t.Error(err)
		}
		if err := surf.SetRole(fakeRole{"xdg_toplevel"}); err == nil {
			t.Error("a second live role object was accepted")
		}
		if err := surf.SetRole(fakeRole{"wl_subsurface"}); err == nil {
			t.Error("a different role was accepted")
		}
		surf.ClearRole(first)
		if err := surf.SetRole(fakeRole{"xdg_toplevel"}); err != nil {
			t.Errorf("the same role after its object died: %v", err)
		}
	})
}

func fx(v float64) wire.Fixed { return wire.FixedFromFloat(v) }

func TestViewportSizesTheSurface(t *testing.T) {
	e := setup(t, 6)
	s := e.surface()
	vp, err := e.viewporter.GetViewport(s)
	must(t, err, s.Attach(e.buffer(40, 20), 0, 0), vp.SetDestination(80, 30), s.Commit())
	e.roundtrip()
	if c := e.last(s); c.size != image.Pt(80, 30) {
		t.Fatalf("size %v, want the 80x30 destination", c.size)
	}
	must(t, vp.SetSource(fx(10), fx(5), fx(20), fx(10)), vp.SetDestination(-1, -1), s.Commit())
	e.roundtrip()
	c := e.last(s)
	if c.size != image.Pt(20, 10) {
		t.Fatalf("size %v, want the integral 20x10 source", c.size)
	}
	if got := c.source; got != (render.FRect{X: 10, Y: 5, W: 20, H: 10}) {
		t.Fatalf("buffer source %+v", got)
	}
	must(t, vp.Destroy(), s.Commit())
	e.roundtrip()
	if c := e.last(s); c.size != image.Pt(40, 20) {
		t.Fatalf("size %v after the viewport went away, want the buffer size", c.size)
	}
}

func TestViewportErrors(t *testing.T) {
	cases := []struct {
		name string
		do   func(e *env, s *cw.WlSurface, vp *cvp.WpViewport) error
		code uint32
	}{
		{"zero destination", func(_ *env, _ *cw.WlSurface, vp *cvp.WpViewport) error { return vp.SetDestination(0, 5) }, uint32(svp.WpViewportErrorBadValue)},
		{"negative source", func(_ *env, _ *cw.WlSurface, vp *cvp.WpViewport) error {
			return vp.SetSource(fx(-2), fx(0), fx(1), fx(1))
		}, uint32(svp.WpViewportErrorBadValue)},
		{"fractional source without destination", func(e *env, s *cw.WlSurface, vp *cvp.WpViewport) error {
			return errors.Join(s.Attach(e.buffer(10, 10), 0, 0), vp.SetSource(fx(0), fx(0), fx(2.5), fx(2)), s.Commit())
		}, uint32(svp.WpViewportErrorBadSize)},
		{"source outside the buffer", func(e *env, s *cw.WlSurface, vp *cvp.WpViewport) error {
			return errors.Join(s.Attach(e.buffer(10, 10), 0, 0), vp.SetSource(fx(5), fx(5), fx(6), fx(5)), s.Commit())
		}, uint32(svp.WpViewportErrorOutOfBuffer)},
		{"surface destroyed", func(_ *env, s *cw.WlSurface, vp *cvp.WpViewport) error {
			return errors.Join(s.Destroy(), vp.SetDestination(5, 5))
		}, uint32(svp.WpViewportErrorNoSurface)},
		{"second viewport", func(e *env, s *cw.WlSurface, _ *cvp.WpViewport) error {
			_, err := e.viewporter.GetViewport(s)
			return err
		}, uint32(svp.WpViewporterErrorViewportExists)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, 6)
			s := e.surface()
			vp, err := e.viewporter.GetViewport(s)
			must(t, err, c.do(e, s, vp))
			e.protocolError(c.code)
		})
	}
}

type scaleEvents struct{ preferred []uint32 }

func (f *scaleEvents) PreferredScale(_ *cfs.WpFractionalScaleV1, scale uint32) error {
	f.preferred = append(f.preferred, scale)
	return nil
}

type bufferScale struct {
	cw.WlSurfaceHandler
	scales []int32
}

func (b *bufferScale) PreferredBufferScale(_ *cw.WlSurface, factor int32) error {
	b.scales = append(b.scales, factor)
	return nil
}

func TestPreferredScale(t *testing.T) {
	e := setup(t, 6)
	bs := &bufferScale{}
	s, err := e.comp.CreateSurface(bs)
	must(t, err)
	fs := &scaleEvents{}
	_, err = e.fractional.GetFractionalScale(fs, s)
	must(t, err, s.Commit())
	e.roundtrip()
	e.on(func() {
		surf := e.rec.commits[0].s
		for _, scale := range []uint32{180, 180, 120} {
			if err := surf.SetPreferredScale(scale); err != nil {
				t.Error(err)
			}
		}
	})
	e.roundtrip()
	if !slices.Equal(fs.preferred, []uint32{180, 120}) || !slices.Equal(bs.scales, []int32{2, 1}) {
		t.Fatalf("fractional %v, integer %v; want 180 then 120, and 2 then 1, without repeats", fs.preferred, bs.scales)
	}
	late := &scaleEvents{}
	other, err := e.comp.CreateSurface(&bufferScale{})
	must(t, err, other.Commit())
	e.roundtrip()
	e.on(func() {
		if err := find(t, e.rec.commits, other).s.SetPreferredScale(240); err != nil {
			t.Error(err)
		}
	})
	_, err = e.fractional.GetFractionalScale(late, other)
	must(t, err)
	e.roundtrip()
	if !slices.Equal(late.preferred, []uint32{240}) {
		t.Fatalf("a fractional object created after the preference got %v", late.preferred)
	}
	_, err = e.fractional.GetFractionalScale(&scaleEvents{}, s)
	must(t, err)
	e.protocolError(uint32(sfs.WpFractionalScaleManagerV1ErrorFractionalScaleExists))
}

func TestHeldUpdatesApplyInOrder(t *testing.T) {
	e := setup(t, 6)
	s := e.surface()
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	e.roundtrip()
	server := e.last(s).s
	var holds []*Hold
	applied := 0
	hooks := &Hooks{
		Commit:  func(u *Update) error { holds = append(holds, u.Hold()); return nil },
		Applied: func() error { applied++; return nil },
	}
	e.on(func() { server.AddHooks(hooks) })
	for _, x := range []int32{0, 10} {
		if err := errors.Join(s.Damage(x, 0, 5, 5), s.Commit()); err != nil {
			t.Fatal(err)
		}
		e.roundtrip()
	}
	if n := len(e.commits()); n != 1 {
		t.Fatalf("%d commits took effect while held", n-1)
	}
	e.on(func() {
		if err := holds[1].Release(); err != nil {
			t.Error(err)
		}
	})
	if n := len(e.commits()); n != 1 {
		t.Fatal("a later update overtook a held one")
	}
	e.on(func() {
		if err := errors.Join(holds[0].Release(), holds[0].Release()); err != nil {
			t.Error(err)
		}
		server.RemoveHooks(hooks)
	})
	commits := e.commits()
	if len(commits) != 3 || applied != 2 {
		t.Fatalf("%d commits and %d applied hooks after the holds were released", len(commits), applied)
	}
	for i, x := range []int{1, 11} {
		if d := commits[1+i].damage; d.Area() != 25 || !d.Contains(image.Pt(x, 1)) {
			t.Fatalf("update %d applied damage %v, want the rectangle at x=%d alone", i, d.Rects(), x-1)
		}
	}
	if err := errors.Join(s.Damage(0, 0, 1, 1), s.Commit()); err != nil {
		t.Fatal(err)
	}
	e.roundtrip()
	if n := len(e.commits()); n != 4 || applied != 2 {
		t.Fatalf("after removing the hooks: %d commits, %d applied hooks", n, applied)
	}
}

func TestGatesHoldTheQueueHead(t *testing.T) {
	e := setup(t, 6)
	s := e.surface()
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	e.roundtrip()
	server := e.last(s).s
	open := false
	var order []int
	next := 0
	hooks := &Hooks{Commit: func(u *Update) error {
		n := next
		next++
		if n == 0 {
			u.Gate(func() bool { return open })
		}
		u.OnApply(func() error { order = append(order, n); return nil })
		return nil
	}}
	e.on(func() { server.AddHooks(hooks) })
	for range 3 {
		if err := s.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	e.roundtrip()
	e.on(func() {
		if err := server.Retry(); err != nil {
			t.Error(err)
		}
	})
	if n := len(e.commits()); n != 1 {
		t.Fatalf("%d updates applied behind a closed gate", n-1)
	}
	e.on(func() {
		open = true
		if err := server.Retry(); err != nil {
			t.Error(err)
		}
	})
	var got []int
	e.on(func() { got = slices.Clone(order) })
	if !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("updates applied in order %v, want [0 1 2]", got)
	}
}

func TestSynchronizedSubsurfacesMergeUpdatesUntilTheParentApplies(t *testing.T) {
	e := setup(t, 6)
	parent, child := e.surface(), e.surface()
	sub, err := e.subcomp.GetSubsurface(child, parent)
	if err != nil {
		t.Fatal(err)
	}
	must(t, child.Commit(), parent.Commit())
	e.roundtrip()
	server := e.last(child).s
	var holds []*Hold
	hooks := &Hooks{Commit: func(u *Update) error { holds = append(holds, u.Hold()); return nil }}
	e.on(func() { server.AddHooks(hooks) })
	must(t, child.Damage(0, 0, 5, 5), child.Commit(), child.Damage(10, 0, 5, 5), child.Commit(), parent.Commit())
	e.roundtrip()
	before := len(e.commits())
	if c := e.last(child); c.damage.Area() != 0 {
		t.Fatalf("a held synchronized update applied with damage %v", c.damage.Rects())
	}
	e.on(func() {
		for _, h := range holds {
			if err := h.Release(); err != nil {
				t.Error(err)
			}
		}
	})
	if n := len(e.commits()); n != before {
		t.Fatal("a synchronized update applied on release without its parent")
	}
	must(t, parent.Commit())
	e.roundtrip()
	if d := e.last(child).damage; d.Area() != 50 {
		t.Fatalf("the merged synchronized update applied damage %v, want both rectangles", d.Rects())
	}
	must(t, sub.Destroy())
}
