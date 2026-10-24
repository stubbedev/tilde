package compositor

import (
	"errors"
	"image"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/loop"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/soft"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wlclient"
)

type spyTexture struct {
	render.Texture
	updates   []render.Region
	destroyed bool
}

func (t *spyTexture) Update(stride int, data []byte, damage render.Region) error {
	t.updates = append(t.updates, damage)
	return t.Texture.Update(stride, data, damage)
}

func (t *spyTexture) Destroy() error {
	t.destroyed = true
	return t.Texture.Destroy()
}

type spyRenderer struct {
	*soft.Renderer
	textures []*spyTexture
}

func (r *spyRenderer) NewTexture(f render.Format, size image.Point, stride int, data []byte) (render.Texture, error) {
	tex, err := r.Renderer.NewTexture(f, size, stride, data)
	if err != nil {
		return nil, err
	}
	spy := &spyTexture{Texture: tex}
	r.textures = append(r.textures, spy)
	return spy, nil
}

type fakeBackend struct{ r *spyRenderer }

func (b fakeBackend) Start(backend.Handler) error { return nil }
func (b fakeBackend) Renderer() render.Renderer   { return b.r }
func (b fakeBackend) Close() error                { return nil }

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (r registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type shmEvents struct{}

func (shmEvents) Format(*cw.WlShm, cw.WlShmFormat) error { return nil }

type releases struct{ n int }

func (r *releases) Release(*cw.WlBuffer) error {
	r.n++
	return nil
}

type rig struct {
	t        *testing.T
	loop     *loop.Loop
	comp     *Compositor
	renderer *spyRenderer
	client   *wlclient.Display
	shm      *cw.WlShm
	wlComp   *cw.WlCompositor
}

func setup(t *testing.T) *rig {
	t.Helper()
	dir := testenv.SocketDir(t)
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, loop: l, renderer: &spyRenderer{Renderer: soft.New()}}
	disp, err := server.New(l, server.Options{RuntimeDir: dir, Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	if r.comp, err = New(l, disp, fakeBackend{r.renderer}, DefaultConfig(testenv.Keymap(t), testenv.Font(t))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.comp.Close(); err != nil {
			t.Error(err)
		}
	})
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		r.on(func() { l.Stop() })
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
		_ = disp.Close()
		_ = l.Close()
	})
	if r.client, err = wlclient.Connect(filepath.Join(dir, disp.Name())); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.client.Close() })
	globals := registry{}
	reg, err := r.client.WlDisplay().GetRegistry(globals)
	if err != nil {
		t.Fatal(err)
	}
	r.roundtrip()
	if r.shm, err = cw.NewWlShm(r.client, 2, shmEvents{}); err != nil {
		t.Fatal(err)
	}
	if r.wlComp, err = cw.NewWlCompositor(r.client, 6); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(reg.Bind(globals["wl_shm"], r.shm), reg.Bind(globals["wl_compositor"], r.wlComp)); err != nil {
		t.Fatal(err)
	}
	r.roundtrip()
	return r
}

func (r *rig) on(f func()) {
	ran := make(chan struct{})
	if err := r.loop.Post(func() error {
		f()
		close(ran)
		return nil
	}); err != nil {
		r.t.Fatal(err)
	}
	<-ran
}

func (r *rig) roundtrip() {
	r.t.Helper()
	if err := r.client.Roundtrip(); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) pool(size int) ([]byte, *cw.WlShmPool) {
	r.t.Helper()
	fd, err := unix.MemfdCreate("pool", unix.MFD_CLOEXEC)
	if err != nil {
		r.t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		r.t.Fatal(err)
	}
	m, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = unix.Munmap(m) })
	p, err := r.shm.CreatePool(fd, int32(size))
	if err != nil {
		r.t.Fatal(err)
	}
	return m, p
}

func must(t *testing.T, errs ...error) {
	t.Helper()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) texel(tex *spyTexture, x, y int) [4]byte {
	r.t.Helper()
	size := tex.Size()
	b := soft.NewBuffer(size.X, size.Y)
	p, err := r.renderer.Begin(b, render.Region{image.Rectangle{Max: size}})
	if err != nil {
		r.t.Error(err)
		return [4]byte{}
	}
	p.DrawTexture(tex.Texture, render.FRect{W: float64(size.X), H: float64(size.Y)}, image.Rectangle{Max: size}, render.Normal, 1)
	if err := p.End(); err != nil {
		r.t.Error(err)
	}
	i := y*b.Stride + x*4
	return [4]byte(b.Pix[i : i+4])
}

func TestCommitUploadsDamageOnceAndReleases(t *testing.T) {
	r := setup(t)
	mem, pool := r.pool(4 * 4 * 4 * 2)
	rel := &releases{}
	small, err := pool.CreateBuffer(rel, 0, 4, 4, 16, cw.WlShmFormatXrgb8888)
	must(t, err)
	big, err := pool.CreateBuffer(rel, 0, 4, 8, 16, cw.WlShmFormatXrgb8888)
	must(t, err)
	s, err := r.wlComp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	must(t, err)
	mem[0] = 0x11
	must(t, s.Attach(small, 0, 0), s.DamageBuffer(0, 0, 4, 4), s.Commit())
	r.roundtrip()
	if rel.n != 1 {
		t.Fatalf("%d releases after the first commit, want 1", rel.n)
	}
	r.on(func() {
		if len(r.renderer.textures) != 1 || r.renderer.textures[0].Size() != image.Pt(4, 4) {
			t.Errorf("textures %v, want one 4x4 upload", len(r.renderer.textures))
		}
	})
	mem[(1*4+1)*4] = 0x22
	mem[(3*4+3)*4] = 0x33
	must(t, s.Attach(small, 0, 0), s.DamageBuffer(1, 1, 1, 1), s.Commit())
	r.roundtrip()
	r.on(func() {
		tex := r.renderer.textures[0]
		if len(tex.updates) != 1 || len(tex.updates[0]) != 1 || tex.updates[0][0] != image.Rect(1, 1, 2, 2) {
			t.Errorf("updates %v, want exactly the damaged pixel", tex.updates)
		}
		if got := r.texel(tex, 1, 1)[0]; got != 0x22 {
			t.Errorf("damaged texel = %#x, want 0x22", got)
		}
		if got := r.texel(tex, 3, 3)[0]; got != 0 {
			t.Errorf("undamaged texel = %#x, want the old 0: only damage is uploaded", got)
		}
	})
	must(t, s.Damage(0, 0, 1, 1), s.Commit())
	r.roundtrip()
	if rel.n != 2 {
		t.Fatalf("%d releases, want 2: a commit without attach has no buffer to release", rel.n)
	}
	must(t, s.Attach(big, 0, 0), s.Commit())
	r.roundtrip()
	r.on(func() {
		if len(r.renderer.textures) != 2 || !r.renderer.textures[0].destroyed || r.renderer.textures[1].Size() != image.Pt(4, 8) {
			t.Error("a new buffer size did not replace the texture")
		}
	})
	must(t, s.Attach(nil, 0, 0), s.Commit())
	r.roundtrip()
	r.on(func() {
		if !r.renderer.textures[1].destroyed || len(r.comp.textures) != 0 {
			t.Error("a null attach kept the texture")
		}
	})
	must(t, s.Attach(small, 0, 0), s.Commit(), s.Destroy())
	r.roundtrip()
	r.on(func() {
		if len(r.comp.textures) != 0 || !r.renderer.textures[2].destroyed {
			t.Error("destroying the surface kept its texture")
		}
	})
}

type unsyncable struct{ *soft.Renderer }

func (unsyncable) NativeFences() bool          { return true }
func (unsyncable) SyncFile() (int, error)      { return -1, errors.New("no fence") }
func (unsyncable) RenderNode() (string, error) { return "", errors.New("no render node") }

type rendererBackend struct{ r render.Renderer }

func (b rendererBackend) Start(backend.Handler) error { return nil }
func (b rendererBackend) Renderer() render.Renderer   { return b.r }
func (b rendererBackend) Close() error                { return nil }

func TestAFailedStartReturnsItsError(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	disp, err := server.New(l, server.Options{RuntimeDir: testenv.SocketDir(t), Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	defer disp.Close()
	c, err := New(l, disp, rendererBackend{unsyncable{soft.New()}}, DefaultConfig(testenv.Keymap(t), testenv.Font(t)))
	if c != nil || err == nil || !strings.Contains(err.Error(), "no render node") {
		t.Fatalf("New = %v, %v; want the render node error", c, err)
	}
}
