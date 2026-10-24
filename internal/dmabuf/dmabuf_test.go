package dmabuf

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/fourcc"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	cld "github.com/stubbedev/lys-wm/internal/protocol/client/linuxdmabuf"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wlclient"
	cfb "github.com/stubbedev/lys-wm/internal/wlclient/feedback"
)

var formats = []gpu.FormatModifier{
	{Format: fourcc.ARGB8888, Modifier: fourcc.Linear},
	{Format: fourcc.ARGB8888, Modifier: fourcc.Invalid},
	{Format: fourcc.XRGB8888, Modifier: fourcc.Linear},
	{Format: fourcc.NV12, Modifier: fourcc.Linear},
}

type importer struct {
	device  error
	mu      sync.Mutex
	checked []gpu.DMABUF
	reject  bool
	gone    []*Buffer
	plane   gpu.ScanoutTarget
	surface *surface.Surface
}

func (i *importer) DMABUFFormats() ([]gpu.FormatModifier, error) { return formats, nil }

const mainDevice = 0xe280

func (i *importer) MainDevice() (uint64, error) { return mainDevice, i.device }

func (i *importer) CheckDMABUF(b gpu.DMABUF) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.checked = append(i.checked, b)
	if i.reject {
		return errors.New("the GPU refuses")
	}
	return nil
}

func (i *importer) SurfaceScanout(s *surface.Surface) gpu.ScanoutTarget {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.surface = s
	return i.plane
}

func (*importer) Committed(*surface.Surface) error { return nil }

func (*importer) Destroyed(*surface.Surface) error { return nil }

func (e *env) scanout(t gpu.ScanoutTarget) {
	e.t.Helper()
	e.imp.mu.Lock()
	e.imp.plane = t
	s := e.imp.surface
	e.imp.mu.Unlock()
	if s == nil {
		e.t.Fatal("no surface asked for feedback")
	}
	if err := e.l.Post(func() error { return e.m.ScanoutChanged(s) }); err != nil {
		e.t.Fatal(err)
	}
}

func (i *importer) BufferDestroyed(b *Buffer) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.gone = append(i.gone, b)
	return nil
}

type env struct {
	t   *testing.T
	dir string
	l   *loop.Loop
	d   *server.Display
	imp *importer
	m   *Manager
}

func setup(t *testing.T, device error) *env {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, dir: testenv.SocketDir(t), l: l, imp: &importer{device: device}}
	if e.d, err = server.New(l, server.Options{RuntimeDir: e.dir, Report: func(err error) { t.Error(err) }}); err != nil {
		t.Fatal(err)
	}
	if _, err := surface.New(e.d, e.imp); err != nil {
		t.Fatal(err)
	}
	if e.m, err = New(e.d, e.imp, e.imp); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		_ = l.Post(func() error {
			l.Stop()
			return nil
		})
		if err := errors.Join(<-done, e.m.Close(), e.d.Close(), l.Close()); err != nil {
			t.Error(err)
		}
	})
	return e
}

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type legacy struct {
	formats   []fourcc.Format
	modifiers []gpu.FormatModifier
}

func (l *legacy) Format(_ *cld.ZwpLinuxDmabufV1, f uint32) error {
	l.formats = append(l.formats, fourcc.Format(f))
	return nil
}

func (l *legacy) Modifier(_ *cld.ZwpLinuxDmabufV1, f, hi, lo uint32) error {
	l.modifiers = append(l.modifiers, gpu.FormatModifier{Format: fourcc.Format(f), Modifier: fourcc.Modifier(wire.Join(hi, lo))})
	return nil
}

type client struct {
	t       *testing.T
	d       *wlclient.Display
	reg     *cw.WlRegistry
	globals registry
	linux   *cld.ZwpLinuxDmabufV1
	legacy  *legacy
}

func (c *client) surface() *cw.WlSurface {
	c.t.Helper()
	cmp, err := cw.NewWlCompositor(c.d, 6)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.reg.Bind(c.globals["wl_compositor"], cmp); err != nil {
		c.t.Fatal(err)
	}
	s, err := cmp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	if err != nil {
		c.t.Fatal(err)
	}
	return s
}

func (e *env) connect(version uint32) *client {
	e.t.Helper()
	d, err := wlclient.Connect(filepath.Join(e.dir, e.d.Name()))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = d.Close() })
	globals := registry{}
	reg, err := d.WlDisplay().GetRegistry(globals)
	if err != nil {
		e.t.Fatal(err)
	}
	c := &client{t: e.t, d: d, reg: reg, globals: globals, legacy: &legacy{}}
	c.roundtrip()
	if c.linux, err = cld.NewZwpLinuxDmabufV1(d, version, c.legacy); err != nil {
		e.t.Fatal(err)
	}
	if err := reg.Bind(globals["zwp_linux_dmabuf_v1"], c.linux); err != nil {
		e.t.Fatal(err)
	}
	c.roundtrip()
	return c
}

func (c *client) roundtrip() {
	c.t.Helper()
	if err := c.d.Roundtrip(); err != nil {
		c.t.Fatal(err)
	}
}

type received struct{ batches []cfb.Feedback }

func (r *received) listener() *cfb.Listener {
	return cfb.NewListener(func(f cfb.Feedback) error {
		r.batches = append(r.batches, f)
		return nil
	})
}

func (r *received) last(n int) ([]cfb.Tranche, bool) {
	if len(r.batches) != n {
		return nil, false
	}
	return r.batches[n-1].Tranches, true
}

func TestFeedbackDescribesTheRenderer(t *testing.T) {
	e := setup(t, nil)
	if e.m.Version() != 5 {
		t.Fatalf("version %d with a main device", e.m.Version())
	}
	c := e.connect(5)
	fb := &received{}
	if _, err := c.linux.GetDefaultFeedback(fb.listener()); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	want := []cfb.Tranche{{Target: mainDevice, Formats: formats}}
	if got, ok := fb.last(1); !ok || fb.batches[0].Main != mainDevice || !slices.EqualFunc(got, want, sameTranche) {
		t.Fatalf("received %+v", fb.batches)
	}
	if len(c.legacy.formats)+len(c.legacy.modifiers) != 0 {
		t.Fatalf("a v5 client got legacy events %+v", c.legacy)
	}
}

func sameTranche(a, b cfb.Tranche) bool {
	return a.Target == b.Target && a.Flags == b.Flags && slices.Equal(a.Formats, b.Formats)
}

func TestASurfaceThatCanBeScannedOutLearnsThePlaneFormats(t *testing.T) {
	e := setup(t, nil)
	c := e.connect(5)
	s := c.surface()
	fb := &received{}
	if _, err := c.linux.GetSurfaceFeedback(fb.listener(), s); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	renderer := cfb.Tranche{Target: mainDevice, Formats: formats}
	if got, ok := fb.last(1); !ok || !slices.EqualFunc(got, []cfb.Tranche{renderer}, sameTranche) {
		t.Fatalf("feedback before any scanout: %+v", fb.batches)
	}
	plane := gpu.ScanoutTarget{Device: 0xe200, Formats: []gpu.FormatModifier{
		{Format: fourcc.XRGB8888, Modifier: fourcc.Linear},
		{Format: fourcc.XRGB8888, Modifier: 7},
		{Format: fourcc.ARGB8888, Modifier: fourcc.Invalid},
	}}
	e.scanout(plane)
	c.roundtrip()
	scan := cfb.Tranche{Target: plane.Device, Formats: []gpu.FormatModifier{plane.Formats[0], plane.Formats[2]}, Flags: cld.ZwpLinuxDmabufFeedbackV1TrancheFlagsScanout}
	if got, ok := fb.last(2); !ok || !slices.EqualFunc(got, []cfb.Tranche{scan, renderer}, sameTranche) {
		t.Fatalf("feedback once the surface can be scanned out: %+v", fb.batches)
	}
	e.scanout(gpu.ScanoutTarget{})
	c.roundtrip()
	if got, ok := fb.last(3); !ok || !slices.EqualFunc(got, []cfb.Tranche{renderer}, sameTranche) {
		t.Fatalf("feedback once the surface can no longer be scanned out: %+v", fb.batches)
	}
}

func TestLegacyEventsWithoutADevice(t *testing.T) {
	e := setup(t, gpu.ErrUnsupported)
	if e.m.Version() != 3 {
		t.Fatalf("version %d without a main device", e.m.Version())
	}
	if c := e.connect(3); !slices.Equal(c.legacy.modifiers, formats) {
		t.Fatalf("modifiers %+v", c.legacy.modifiers)
	}
	if c := e.connect(2); !slices.Equal(c.legacy.formats, []fourcc.Format{fourcc.ARGB8888, fourcc.XRGB8888, fourcc.NV12}) {
		t.Fatalf("formats %+v", c.legacy.formats)
	}
}

func memfd(t *testing.T, size int) int {
	t.Helper()
	fd, err := unix.MemfdCreate("plane", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

type created struct {
	buffer *cw.WlBuffer
	failed bool
}

func (c *created) Created(_ *cld.ZwpLinuxBufferParamsV1, b *cw.WlBuffer) (cw.WlBufferHandler, error) {
	c.buffer = b
	return releases{}, nil
}

func (c *created) Failed(*cld.ZwpLinuxBufferParamsV1) error {
	c.failed = true
	return nil
}

type releases struct{}

func (releases) Release(*cw.WlBuffer) error { return nil }

func TestBuffersAreCheckedAndCreated(t *testing.T) {
	e := setup(t, gpu.ErrUnsupported)
	c := e.connect(3)
	fd := memfd(t, 64*16*4)
	result := &created{}
	p, err := c.linux.CreateParams(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(64, 16, uint32(fourcc.XRGB8888), 0)); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	if result.buffer == nil || result.failed {
		t.Fatalf("create gave %+v", result)
	}
	e.imp.mu.Lock()
	checked := e.imp.checked
	e.imp.mu.Unlock()
	if len(checked) != 1 || checked[0].Format != fourcc.XRGB8888 || checked[0].Planes[0].Stride != 256 {
		t.Fatalf("checked %+v", checked)
	}
	if err := result.buffer.Destroy(); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	e.imp.mu.Lock()
	gone := len(e.imp.gone)
	e.imp.mu.Unlock()
	if gone != 1 {
		t.Fatalf("%d buffers reported destroyed", gone)
	}

	e.imp.mu.Lock()
	e.imp.reject = true
	e.imp.mu.Unlock()
	refused := &created{}
	if p, err = c.linux.CreateParams(refused); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(64, 16, uint32(fourcc.XRGB8888), 0)); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	if !refused.failed || refused.buffer != nil {
		t.Fatalf("a refused import gave %+v", refused)
	}
	if p, err = c.linux.CreateParams(&created{}); err != nil {
		t.Fatal(err)
	}
	if err := p.Add(memfd(t, 64*16*4), 0, 0, 256, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateImmed(releases{}, 64, 16, uint32(fourcc.XRGB8888), 0); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := c.d.Roundtrip(); !errors.As(err, &pe) || pe.Code != uint32(cld.ZwpLinuxBufferParamsV1ErrorInvalidWlBuffer) || !strings.Contains(pe.Message, "format XR24 with modifier linear") {
		t.Fatalf("an immediate buffer the GPU refuses: %v", err)
	}
}

func TestTightlyPackedNV12IsInBounds(t *testing.T) {
	e := setup(t, gpu.ErrUnsupported)
	c := e.connect(3)
	const w, h = 64, 16
	fd := memfd(t, w*h*3/2)
	result := &created{}
	p, err := c.linux.CreateParams(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(p.Add(fd, 0, 0, w, 0, 0), p.Add(fd, 1, w*h, w, 0, 0), p.Create(w, h, uint32(fourcc.NV12), 0)); err != nil {
		t.Fatal(err)
	}
	c.roundtrip()
	if result.buffer == nil || result.failed {
		t.Fatalf("a tightly packed linear NV12 buffer gave %+v", result)
	}
}

func TestParamsErrors(t *testing.T) {
	cases := map[string]struct {
		code  uint32
		build func(t *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error
	}{
		"plane index": {1, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return p.Add(fd, 4, 0, 256, 0, 0)
		}},
		"plane set": {2, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Add(fd, 0, 0, 256, 0, 0))
		}},
		"mixed modifiers": {4, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Add(fd, 1, 0, 256, 0, 1))
		}},
		"missing plane": {3, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 1, 0, 256, 0, 0), p.Create(64, 16, uint32(fourcc.XRGB8888), 0))
		}},
		"too few planes": {3, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 64, 0, 0), p.Create(64, 16, uint32(fourcc.NV12), 0))
		}},
		"unknown format": {4, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(64, 16, 'R'|'G'<<8|'1'<<16|'6'<<24, 0))
		}},
		"unknown modifier": {4, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 1, 2), p.Create(64, 16, uint32(fourcc.XRGB8888), 0))
		}},
		"dimensions": {5, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(0, 16, uint32(fourcc.XRGB8888), 0))
		}},
		"out of bounds": {6, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(64, 64, uint32(fourcc.XRGB8888), 0))
		}},
		"used twice": {0, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			return errors.Join(p.Add(fd, 0, 0, 256, 0, 0), p.Create(64, 16, uint32(fourcc.XRGB8888), 0), p.Create(64, 16, uint32(fourcc.XRGB8888), 0))
		}},
		"refused immediate": {7, func(_ *testing.T, p *cld.ZwpLinuxBufferParamsV1, fd int) error {
			if err := p.Add(fd, 0, 0, 256, 0, 0); err != nil {
				return err
			}
			_, err := p.CreateImmed(releases{}, 64, 16, uint32(fourcc.XRGB8888), cld.ZwpLinuxBufferParamsV1FlagsYInvert)
			return err
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t, gpu.ErrUnsupported)
			c := e.connect(3)
			p, err := c.linux.CreateParams(&created{})
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.build(t, p, memfd(t, 64*16*4)); err != nil {
				t.Fatal(err)
			}
			var pe *wire.ProtocolError
			if err := c.d.Roundtrip(); !errors.As(err, &pe) || pe.Code != tc.code || pe.Object != p.Base().ID() {
				t.Fatalf("%v, want error %d on params", err, tc.code)
			}
			if strings.TrimSpace(pe.Message) == "" {
				t.Fatal("the error has no message")
			}
		})
	}
}
