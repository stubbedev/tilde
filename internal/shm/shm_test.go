package shm

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wlclient"
)

type reader struct {
	pixels [][]byte
}

func (r *reader) Committed(s *surface.Surface) error {
	b, ok := s.Buffer().(*Buffer)
	if !s.Attached() || !ok {
		return nil
	}
	return b.Read(func(data []byte, stride int) error {
		r.pixels = append(r.pixels, slices.Clone(data[:b.Size().X*4]))
		return nil
	})
}

func (r *reader) Destroyed(*surface.Surface) error { return nil }

type env struct {
	t      *testing.T
	loop   *loop.Loop
	disp   *server.Display
	dir    string
	reader *reader
	done   chan error
}

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (r registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type formats struct{ got []cw.WlShmFormat }

func (f *formats) Format(_ *cw.WlShm, format cw.WlShmFormat) error {
	f.got = append(f.got, format)
	return nil
}

type client struct {
	t       *testing.T
	d       *wlclient.Display
	shm     *cw.WlShm
	comp    *cw.WlCompositor
	formats *formats
}

func setup(t *testing.T) *env {
	t.Helper()
	dir, err := os.MkdirTemp("", "tilde-")
	if err != nil {
		t.Fatal(err)
	}
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, loop: l, dir: dir, reader: &reader{}, done: make(chan error, 1)}
	if e.disp, err = server.New(l, server.Options{RuntimeDir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := surface.New(e.disp, e.reader); err != nil {
		t.Fatal(err)
	}
	if _, err := New(e.disp, []render.Format{render.ARGB8888, render.XRGB8888}); err != nil {
		t.Fatal(err)
	}
	go func() { e.done <- l.Run() }()
	t.Cleanup(func() {
		ran := make(chan struct{})
		_ = l.Post(func() error {
			l.Stop()
			close(ran)
			return nil
		})
		if err := <-e.done; err != nil {
			t.Errorf("Run: %v", err)
		}
		_ = e.disp.Close()
		_ = l.Close()
		_ = os.RemoveAll(dir)
	})
	return e
}

func (e *env) connect() *client {
	e.t.Helper()
	d, err := wlclient.Connect(filepath.Join(e.dir, e.disp.Name()))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = d.Close() })
	c := &client{t: e.t, d: d, formats: &formats{}}
	globals := registry{}
	reg, err := d.WlDisplay().GetRegistry(globals)
	if err != nil {
		e.t.Fatal(err)
	}
	c.roundtrip()
	if c.shm, err = cw.NewWlShm(d, 2, c.formats); err != nil {
		e.t.Fatal(err)
	}
	if c.comp, err = cw.NewWlCompositor(d, 6); err != nil {
		e.t.Fatal(err)
	}
	if err := errors.Join(reg.Bind(globals["wl_shm"], c.shm), reg.Bind(globals["wl_compositor"], c.comp)); err != nil {
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

func (c *client) protocolError(code sw.WlShmError) *wire.ProtocolError {
	c.t.Helper()
	var pe *wire.ProtocolError
	if err := c.d.Roundtrip(); !errors.As(err, &pe) || pe.Code != uint32(code) {
		c.t.Fatalf("Roundtrip = %v, want wl_shm error %d", err, code)
	}
	return pe
}

type memfd struct {
	fd      int
	mapping []byte
}

func newMemfd(t *testing.T, size int) *memfd {
	t.Helper()
	fd, err := unix.MemfdCreate("pool", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		t.Fatal(err)
	}
	m, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Munmap(m)
		_ = unix.Close(fd)
	})
	return &memfd{fd: fd, mapping: m}
}

func (c *client) pool(m *memfd) *cw.WlShmPool {
	c.t.Helper()
	p, err := c.shm.CreatePool(m.fd, int32(len(m.mapping)))
	if err != nil {
		c.t.Fatal(err)
	}
	return p
}

func (c *client) buffer(p *cw.WlShmPool, offset, w, h, stride int32, f cw.WlShmFormat) *cw.WlBuffer {
	c.t.Helper()
	b, err := p.CreateBuffer(struct{ cw.WlBufferHandler }{}, offset, w, h, stride, f)
	if err != nil {
		c.t.Fatal(err)
	}
	return b
}

func (c *client) show(b *cw.WlBuffer) {
	c.t.Helper()
	s, err := c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	if err != nil {
		c.t.Fatal(err)
	}
	if err := errors.Join(s.Attach(b, 0, 0), s.Commit()); err != nil {
		c.t.Fatal(err)
	}
}

func TestFormatsComeFromTheRenderer(t *testing.T) {
	c := setup(t).connect()
	if !slices.Equal(c.formats.got, []cw.WlShmFormat{cw.WlShmFormatArgb8888, cw.WlShmFormatXrgb8888}) {
		t.Fatalf("formats = %v", c.formats.got)
	}
	if _, err := New(nil, []render.Format{render.XRGB8888}); err == nil {
		t.Fatal("a renderer without ARGB8888 was accepted")
	}
}

func TestPoolAndBufferValidation(t *testing.T) {
	cases := []struct {
		name string
		do   func(c *client, m *memfd)
		code sw.WlShmError
	}{
		{"empty pool", func(c *client, m *memfd) {
			if _, err := c.shm.CreatePool(m.fd, 0); err != nil {
				t.Fatal(err)
			}
		}, sw.WlShmErrorInvalidStride},
		{"unmappable fd", func(c *client, _ *memfd) {
			var p [2]int
			if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
				t.Fatal(err)
			}
			defer unix.Close(p[0])
			defer unix.Close(p[1])
			if _, err := c.shm.CreatePool(p[0], 4096); err != nil {
				t.Fatal(err)
			}
		}, sw.WlShmErrorInvalidFD},
		{"unsupported format", func(c *client, m *memfd) { c.buffer(c.pool(m), 0, 4, 4, 16, cw.WlShmFormatAbgr8888) }, sw.WlShmErrorInvalidFormat},
		{"short stride", func(c *client, m *memfd) { c.buffer(c.pool(m), 0, 4, 4, 12, cw.WlShmFormatArgb8888) }, sw.WlShmErrorInvalidStride},
		{"past the pool", func(c *client, m *memfd) { c.buffer(c.pool(m), 4096-15, 4, 1, 16, cw.WlShmFormatArgb8888) }, sw.WlShmErrorInvalidStride},
		{"negative offset", func(c *client, m *memfd) { c.buffer(c.pool(m), -4, 1, 1, 4, cw.WlShmFormatArgb8888) }, sw.WlShmErrorInvalidStride},
		{"shrinking resize", func(c *client, m *memfd) {
			if err := c.pool(m).Resize(1024); err != nil {
				t.Fatal(err)
			}
		}, sw.WlShmErrorInvalidFD},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := setup(t).connect()
			tc.do(c, newMemfd(t, 4096))
			c.protocolError(tc.code)
		})
	}
}

func TestReadThroughResizedAndDestroyedPools(t *testing.T) {
	e := setup(t)
	c := e.connect()
	m := newMemfd(t, 4096)
	p := c.pool(m)
	if err := unix.Ftruncate(m.fd, 8192); err != nil {
		t.Fatal(err)
	}
	grown, err := unix.Mmap(m.fd, 0, 8192, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Munmap(grown)
	copy(grown[6000:], []byte{1, 2, 3, 4, 5, 6, 7, 8})
	if err := p.Resize(8192); err != nil {
		t.Fatal(err)
	}
	b := c.buffer(p, 6000, 2, 1, 8, cw.WlShmFormatArgb8888)
	if err := p.Destroy(); err != nil {
		t.Fatal(err)
	}
	c.show(b)
	c.roundtrip()
	var got [][]byte
	ran := make(chan struct{})
	if err := e.loop.Post(func() error {
		got = slices.Clone(e.reader.pixels)
		close(ran)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-ran
	if len(got) != 1 || !slices.Equal(got[0], []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("read %v from a buffer past the old pool size after the pool was destroyed", got)
	}
}

func TestTruncatedPoolDisconnectsOnlyThatClient(t *testing.T) {
	e := setup(t)
	bad, good := e.connect(), e.connect()
	m := newMemfd(t, 4096)
	b := bad.buffer(bad.pool(m), 0, 16, 16, 64, cw.WlShmFormatXrgb8888)
	bad.roundtrip()
	if err := unix.Ftruncate(m.fd, 0); err != nil {
		t.Fatal(err)
	}
	bad.show(b)
	if pe := bad.protocolError(sw.WlShmErrorInvalidFD); pe.Object != b.Base().ID() {
		t.Fatalf("error on object %d, want the buffer %d", pe.Object, b.Base().ID())
	}
	if err := bad.d.Dispatch(); err == nil {
		t.Fatal("the client is still connected after the fault")
	}
	gm := newMemfd(t, 64)
	good.show(good.buffer(good.pool(gm), 0, 4, 4, 16, cw.WlShmFormatArgb8888))
	good.roundtrip()
	select {
	case err := <-e.done:
		e.done <- err
		t.Fatalf("the compositor loop ended: %v", err)
	default:
	}
}
