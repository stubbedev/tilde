package syncobj

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/dmabuf"
	"github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/loop"
	cld "github.com/stubbedev/tilde/internal/protocol/client/linuxdmabuf"
	cls "github.com/stubbedev/tilde/internal/protocol/client/linuxdrmsyncobj"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wlclient"
)

func device(t *testing.T) *drm.Device {
	t.Helper()
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil || len(nodes) == 0 {
		t.Skip("no DRM render node")
	}
	d, err := drm.Open(nodes[0])
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if v, err := d.Cap(drm.CapSyncobjTimeline); err != nil || v == 0 {
		t.Skipf("%s has no timeline syncobjs: %v", nodes[0], err)
	}
	return d
}

type importer struct{}

func (importer) DMABUFFormats() ([]gpu.FormatModifier, error) {
	return []gpu.FormatModifier{{Format: gpu.FormatXRGB8888, Modifier: gpu.ModifierLinear}}, nil
}

func (importer) MainDevice() (uint64, error) { return 0, gpu.ErrUnsupported }

func (importer) CheckDMABUF(gpu.DMABUF) error { return nil }

func (importer) BufferDestroyed(*dmabuf.Buffer) error { return nil }

type fencer struct{ d *drm.Device }

func (f fencer) SyncFile() (int, error) {
	s, err := f.d.CreateSyncobj(true)
	if err != nil {
		return -1, err
	}
	fd, err := f.d.ExportSyncFile(s, 0)
	return fd, errors.Join(err, f.d.DestroySyncobj(s))
}

type recorder struct {
	mu      sync.Mutex
	commits int
}

func (r *recorder) Committed(*surface.Surface) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commits++
	return nil
}

func (r *recorder) Destroyed(*surface.Surface) error { return nil }

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commits
}

type env struct {
	t   *testing.T
	dev *drm.Device
	rec *recorder
	c   *wlclient.Display
	cmp *cw.WlCompositor
	buf *cld.ZwpLinuxDmabufV1
	mgr *cls.WpLinuxDrmSyncobjManagerV1
}

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type noEvents struct{}

func (noEvents) Format(*cld.ZwpLinuxDmabufV1, uint32) error                   { return nil }
func (noEvents) Modifier(*cld.ZwpLinuxDmabufV1, uint32, uint32, uint32) error { return nil }

func setup(t *testing.T) *env {
	t.Helper()
	dev := device(t)
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	d, err := server.New(l, server.Options{RuntimeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, dev: dev, rec: &recorder{}}
	if _, err := surface.New(d, e.rec); err != nil {
		t.Fatal(err)
	}
	m, err := dmabuf.New(d, importer{}, importer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(d, l, dev, fencer{dev}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		_ = l.Post(func() error {
			l.Stop()
			return nil
		})
		if err := errors.Join(<-done, m.Close(), d.Close(), l.Close()); err != nil {
			t.Error(err)
		}
	})
	if e.c, err = wlclient.Connect(filepath.Join(dir, d.Name())); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.c.Close() })
	globals := registry{}
	reg, err := e.c.WlDisplay().GetRegistry(globals)
	if err != nil {
		t.Fatal(err)
	}
	e.roundtrip()
	var errs [3]error
	e.cmp, errs[0] = cw.NewWlCompositor(e.c, 6)
	e.buf, errs[1] = cld.NewZwpLinuxDmabufV1(e.c, 3, noEvents{})
	e.mgr, errs[2] = cls.NewWpLinuxDrmSyncobjManagerV1(e.c, 1)
	if err := errors.Join(errors.Join(errs[:]...), reg.Bind(globals["wl_compositor"], e.cmp), reg.Bind(globals["zwp_linux_dmabuf_v1"], e.buf), reg.Bind(globals["wp_linux_drm_syncobj_manager_v1"], e.mgr)); err != nil {
		t.Fatal(err)
	}
	e.roundtrip()
	return e
}

func (e *env) roundtrip() {
	e.t.Helper()
	if err := e.c.Roundtrip(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) timeline() (drm.Syncobj, *cls.WpLinuxDrmSyncobjTimelineV1) {
	e.t.Helper()
	s, err := e.dev.CreateSyncobj(false)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = e.dev.DestroySyncobj(s) })
	fd, err := e.dev.ExportSyncobj(s)
	if err != nil {
		e.t.Fatal(err)
	}
	defer unix.Close(fd)
	tl, err := e.mgr.ImportTimeline(fd)
	if err != nil {
		e.t.Fatal(err)
	}
	return s, tl
}

type released struct{}

func (released) Release(*cw.WlBuffer) error { return nil }

type noParams struct{}

func (noParams) Created(*cld.ZwpLinuxBufferParamsV1, *cw.WlBuffer) (cw.WlBufferHandler, error) {
	return nil, errors.New("unexpected created")
}
func (noParams) Failed(*cld.ZwpLinuxBufferParamsV1) error { return errors.New("unexpected failed") }

func (e *env) dmabuf() *cw.WlBuffer {
	e.t.Helper()
	fd, err := unix.MemfdCreate("plane", unix.MFD_CLOEXEC)
	if err != nil {
		e.t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 16*16*4); err != nil {
		e.t.Fatal(err)
	}
	p, err := e.buf.CreateParams(noParams{})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := p.Add(fd, 0, 0, 64, 0, 0); err != nil {
		e.t.Fatal(err)
	}
	b, err := p.CreateImmed(released{}, 16, 16, gpu.FormatXRGB8888, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *env) surface() (*cw.WlSurface, *cls.WpLinuxDrmSyncobjSurfaceV1) {
	e.t.Helper()
	s, err := e.cmp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	if err != nil {
		e.t.Fatal(err)
	}
	ss, err := e.mgr.GetSurface(s)
	if err != nil {
		e.t.Fatal(err)
	}
	return s, ss
}

func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
		e.roundtrip()
	}
}

func (e *env) signaledAt(s drm.Syncobj, point uint64) bool {
	e.t.Helper()
	v, err := e.dev.Query(s)
	if err != nil {
		e.t.Fatal(err)
	}
	return v >= point
}

func TestCommitsWaitForTheirAcquirePoint(t *testing.T) {
	e := setup(t)
	acquire, atl := e.timeline()
	release, rtl := e.timeline()
	s, ss := e.surface()
	first := e.dmabuf()
	for _, err := range []error{ss.SetAcquirePoint(atl, 0, 1), ss.SetReleasePoint(rtl, 0, 1), s.Attach(first, 0, 0), s.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	e.roundtrip()
	time.Sleep(20 * time.Millisecond)
	e.roundtrip()
	if n := e.rec.count(); n != 0 {
		t.Fatalf("a commit took effect before its acquire point: %d commits", n)
	}
	if err := e.dev.Signal(acquire, 1); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the commit after its acquire point", func() bool { return e.rec.count() == 1 })
	if e.signaledAt(release, 1) {
		t.Fatal("the shown buffer's release point signalled")
	}
	second := e.dmabuf()
	for _, err := range []error{ss.SetAcquirePoint(atl, 0, 2), ss.SetReleasePoint(rtl, 0, 2), s.Attach(second, 0, 0), s.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	third := e.dmabuf()
	for _, err := range []error{ss.SetAcquirePoint(atl, 0, 3), ss.SetReleasePoint(rtl, 0, 3), s.Attach(third, 0, 0), s.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	e.roundtrip()
	if err := e.dev.Signal(acquire, 3); err != nil {
		t.Fatal(err)
	}
	e.waitFor("both commits", func() bool { return e.rec.count() == 2 })
	e.waitFor("the replaced and skipped buffers released", func() bool { return e.signaledAt(release, 2) })
	if e.signaledAt(release, 3) {
		t.Fatal("the buffer on screen was released")
	}
	if err := s.Destroy(); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the last buffer released with its surface", func() bool { return e.signaledAt(release, 3) })
}

func TestProtocolErrors(t *testing.T) {
	type step func(e *env, s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, tl *cls.WpLinuxDrmSyncobjTimelineV1) error
	cases := map[string]struct {
		code      uint32
		onManager bool
		run       step
	}{
		"no buffer": {3, false, func(_ *env, s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, tl *cls.WpLinuxDrmSyncobjTimelineV1) error {
			return errors.Join(ss.SetAcquirePoint(tl, 0, 1), ss.SetReleasePoint(tl, 0, 2), s.Commit())
		}},
		"no acquire point": {4, false, func(e *env, s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, tl *cls.WpLinuxDrmSyncobjTimelineV1) error {
			return errors.Join(ss.SetReleasePoint(tl, 0, 2), s.Attach(e.dmabuf(), 0, 0), s.Commit())
		}},
		"no release point": {5, false, func(e *env, s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, tl *cls.WpLinuxDrmSyncobjTimelineV1) error {
			return errors.Join(ss.SetAcquirePoint(tl, 0, 1), s.Attach(e.dmabuf(), 0, 0), s.Commit())
		}},
		"conflicting points": {6, false, func(e *env, s *cw.WlSurface, ss *cls.WpLinuxDrmSyncobjSurfaceV1, tl *cls.WpLinuxDrmSyncobjTimelineV1) error {
			return errors.Join(ss.SetAcquirePoint(tl, 0, 4), ss.SetReleasePoint(tl, 0, 4), s.Attach(e.dmabuf(), 0, 0), s.Commit())
		}},
		"surface exists": {0, true, func(e *env, s *cw.WlSurface, _ *cls.WpLinuxDrmSyncobjSurfaceV1, _ *cls.WpLinuxDrmSyncobjTimelineV1) error {
			_, err := e.mgr.GetSurface(s)
			return err
		}},
		"invalid timeline": {1, true, func(e *env, _ *cw.WlSurface, _ *cls.WpLinuxDrmSyncobjSurfaceV1, _ *cls.WpLinuxDrmSyncobjTimelineV1) error {
			fd, err := unix.MemfdCreate("not-a-syncobj", unix.MFD_CLOEXEC)
			if err != nil {
				return err
			}
			defer unix.Close(fd)
			_, err = e.mgr.ImportTimeline(fd)
			return err
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			_, tl := e.timeline()
			s, ss := e.surface()
			if err := tc.run(e, s, ss, tl); err != nil {
				t.Fatal(err)
			}
			want := ss.Base().ID()
			if tc.onManager {
				want = e.mgr.Base().ID()
			}
			var pe *wire.ProtocolError
			if err := e.c.Roundtrip(); !errors.As(err, &pe) || pe.Code != tc.code || pe.Object != want {
				t.Fatalf("%v, want error %d on object %d", err, tc.code, want)
			}
		})
	}
}
