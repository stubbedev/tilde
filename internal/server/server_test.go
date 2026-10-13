package server

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wlclient"
)

type harness struct {
	t     *testing.T
	loop  *loop.Loop
	disp  *Display
	dir   string
	trace *bytes.Buffer
}

func start(t *testing.T) *harness {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, loop: l, dir: t.TempDir(), trace: &bytes.Buffer{}}
	if h.disp, err = New(l, Options{RuntimeDir: h.dir, Trace: h.trace}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		h.on(func() { l.Stop() })
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
		if err := h.disp.Close(); err != nil {
			t.Errorf("Display.Close: %v", err)
		}
		if err := l.Close(); err != nil {
			t.Errorf("Loop.Close: %v", err)
		}
	})
	return h
}

func (h *harness) on(f func()) {
	ran := make(chan struct{})
	if err := h.loop.Post(func() error {
		f()
		close(ran)
		return nil
	}); err != nil {
		h.t.Fatal(err)
	}
	<-ran
}

func (h *harness) connect() *wlclient.Display {
	h.t.Helper()
	d, err := wlclient.Connect(filepath.Join(h.dir, h.disp.Name()))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = d.Close() })
	return d
}

func (h *harness) clients() int {
	var n int
	h.on(func() { n = len(h.disp.clients) })
	return n
}

func (h *harness) waitClients(n int) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.clients() != n {
		if time.Now().After(deadline) {
			h.t.Fatalf("server has %d clients, want %d", h.clients(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type registry struct {
	globals map[uint32]string
	removed []uint32
}

func (r *registry) Global(_ *cw.WlRegistry, name uint32, iface string, version uint32) error {
	r.globals[name] = iface + "/" + string(rune('0'+version))
	return nil
}

func (r *registry) GlobalRemove(_ *cw.WlRegistry, name uint32) error {
	r.removed = append(r.removed, name)
	return nil
}

func (h *harness) registry(d *wlclient.Display) (*cw.WlRegistry, *registry) {
	h.t.Helper()
	r := &registry{globals: map[uint32]string{}}
	reg, err := d.WlDisplay().GetRegistry(r)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := d.Roundtrip(); err != nil {
		h.t.Fatal(err)
	}
	return reg, r
}

func compositorGlobal(bound *[]uint32) BindFunc {
	return func(c *Client, id wire.ObjectID, version uint32) error {
		*bound = append(*bound, version)
		_, err := sw.NewWlCompositor(c, id, version, struct{ sw.WlCompositorHandler }{})
		return err
	}
}

func TestSocketLockAndCleanup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wayland-0"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	first, err := New(l, Options{RuntimeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(l, Options{RuntimeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if first.Name() != "wayland-0" || second.Name() != "wayland-1" {
		t.Fatalf("names = %s, %s; want wayland-0 replacing the stale file, then wayland-1", first.Name(), second.Name())
	}
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(dir, "wayland-0"), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		t.Fatalf("wayland-0 is not a socket: %v", err)
	}
	for _, d := range []*Display{first, second} {
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
	left, err := os.ReadDir(dir)
	if err != nil || len(left) != 0 {
		t.Fatalf("Close left %v behind (%v)", left, err)
	}
	if _, err := New(l, Options{}); err == nil || !strings.Contains(err.Error(), "XDG_RUNTIME_DIR") {
		t.Fatalf("New without a runtime dir = %v", err)
	}
	long := filepath.Join(dir, strings.Repeat("x", 100))
	if _, err := New(l, Options{RuntimeDir: long}); err == nil || !strings.Contains(err.Error(), "a Unix socket allows 107") {
		t.Fatalf("New with an overlong path = %v", err)
	}
}

func TestRegistryAnnouncesAddsAndRemovesGlobals(t *testing.T) {
	h := start(t)
	var bound []uint32
	h.on(func() {
		if _, err := h.disp.AddGlobal(sw.WlCompositorInterface, 6, compositorGlobal(&bound)); err != nil {
			t.Error(err)
		}
	})
	d := h.connect()
	reg, r := h.registry(d)
	if r.globals[1] != "wl_compositor/6" {
		t.Fatalf("globals = %v", r.globals)
	}
	comp, err := cw.NewWlCompositor(d, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Bind(1, comp); err != nil {
		t.Fatal(err)
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	h.on(func() {
		if len(bound) != 1 || bound[0] != 5 {
			t.Errorf("bind called with %v, want [5]", bound)
		}
	})
	var g *Global
	h.on(func() {
		var err error
		if g, err = h.disp.AddGlobal(sw.WlShmInterface, 2, func(*Client, wire.ObjectID, uint32) error { return nil }); err != nil {
			t.Error(err)
		}
	})
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if r.globals[2] != "wl_shm/2" {
		t.Fatalf("runtime global not announced: %v", r.globals)
	}
	h.on(func() {
		if err := g.Remove(); err != nil {
			t.Error(err)
		}
	})
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(r.removed) != 1 || r.removed[0] != 2 {
		t.Fatalf("removed = %v, want [2]", r.removed)
	}
	if _, late := h.registry(h.connect()); late.globals[2] != "" {
		t.Fatal("a removed global was announced to a new registry")
	}
}

func TestBindErrors(t *testing.T) {
	cases := []struct {
		name string
		bind func(d *wlclient.Display, reg *cw.WlRegistry) error
		want string
	}{
		{"version too high", func(d *wlclient.Display, reg *cw.WlRegistry) error {
			comp, err := cw.NewWlCompositor(d, 7)
			if err != nil {
				return err
			}
			return reg.Bind(1, comp)
		}, "invalid version"},
		{"wrong interface", func(d *wlclient.Display, reg *cw.WlRegistry) error {
			shm, err := cw.NewWlShm(d, 1, struct{ cw.WlShmHandler }{})
			if err != nil {
				return err
			}
			return reg.Bind(1, shm)
		}, "invalid interface"},
		{"unknown global", func(d *wlclient.Display, reg *cw.WlRegistry) error {
			comp, err := cw.NewWlCompositor(d, 1)
			if err != nil {
				return err
			}
			return reg.Bind(99, comp)
		}, "invalid global"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t)
			var bound []uint32
			h.on(func() {
				if _, err := h.disp.AddGlobal(sw.WlCompositorInterface, 6, compositorGlobal(&bound)); err != nil {
					t.Error(err)
				}
			})
			d := h.connect()
			reg, _ := h.registry(d)
			if err := c.bind(d, reg); err != nil {
				t.Fatal(err)
			}
			var pe *wire.ProtocolError
			err := d.Roundtrip()
			if !errors.As(err, &pe) || pe.Object != reg.Base().ID() || pe.Code != uint32(sw.WlDisplayErrorInvalidObject) || !strings.Contains(pe.Message, c.want) {
				t.Fatalf("Roundtrip = %v, want invalid_object on the registry mentioning %q", err, c.want)
			}
			h.waitClients(0)
		})
	}
}

func TestProtocolErrorDisconnectsOnlyThatClient(t *testing.T) {
	h := start(t)
	bad, good := h.connect(), h.connect()
	reg, _ := h.registry(bad)
	h.registry(good)
	if err := bad.Conn().Begin(reg.Base().ID(), 7).End(); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := bad.Roundtrip(); !errors.As(err, &pe) || pe.Object != reg.Base().ID() || pe.Code != uint32(sw.WlDisplayErrorInvalidMethod) {
		t.Fatalf("Roundtrip = %v, want invalid_method on the registry", err)
	}
	if err := bad.Dispatch(); err == nil {
		t.Fatal("the failed client is still connected")
	}
	if err := good.Roundtrip(); err != nil {
		t.Fatalf("the other client was affected: %v", err)
	}
	h.waitClients(1)
}

func TestRequestToUnknownObject(t *testing.T) {
	h := start(t)
	d := h.connect()
	if err := d.Conn().Begin(99, 0).End(); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := d.Roundtrip(); !errors.As(err, &pe) || pe.Object != 1 || pe.Code != uint32(sw.WlDisplayErrorInvalidObject) {
		t.Fatalf("Roundtrip = %v, want invalid_object on wl_display", err)
	}
}

func TestSyncFreesCallbackIDsAndTraces(t *testing.T) {
	h := start(t)
	d := h.connect()
	h.registry(d)
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var trace string
	h.on(func() { trace = h.trace.String() })
	for _, want := range []string{
		"wl_display@1.get_registry(new id wl_registry@2)",
		"wl_display@1.sync(new id wl_callback@3)",
		" -> wl_callback@3.done(",
		" -> wl_display@1.delete_id(3)",
	} {
		if !strings.Contains(trace, want) {
			t.Errorf("trace lacks %q:\n%s", want, trace)
		}
	}
	if n := strings.Count(trace, "sync(new id wl_callback@3)"); n != 2 {
		t.Errorf("callback id 3 used %d times, want 2: delete_id did not free it", n)
	}
}

func TestPeerCredentials(t *testing.T) {
	h := start(t)
	h.registry(h.connect())
	h.on(func() {
		creds := h.disp.clients[0].Creds()
		if int(creds.Pid) != os.Getpid() || int(creds.Uid) != os.Getuid() {
			t.Errorf("creds = %+v, want pid %d uid %d", creds, os.Getpid(), os.Getuid())
		}
	})
}

func TestClientThatStopsReadingIsDisconnected(t *testing.T) {
	h := start(t)
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Connect(fd, &unix.SockaddrUnix{Name: filepath.Join(h.dir, h.disp.Name())}); err != nil {
		t.Fatal(err)
	}
	const syncs = 40000
	req := make([]byte, 0, 12*syncs)
	for i := range syncs {
		req = binary.NativeEndian.AppendUint32(req, 1)
		req = binary.NativeEndian.AppendUint32(req, 12<<16)
		req = binary.NativeEndian.AppendUint32(req, uint32(2+i))
	}
	go func() {
		for len(req) > 0 {
			n, err := unix.Write(fd, req)
			if err != nil {
				return
			}
			req = req[n:]
		}
	}()
	h.waitClients(0)
	var stream []byte
	buf := make([]byte, 1<<16)
	for {
		n, err := unix.Read(fd, buf)
		if n <= 0 || err != nil {
			break
		}
		stream = append(stream, buf[:n]...)
	}
	if len(stream) < 1<<16 {
		t.Fatalf("only %d bytes were queued before the disconnect, want more than the output bound", len(stream))
	}
	for len(stream) >= 8 {
		object, word := binary.NativeEndian.Uint32(stream), binary.NativeEndian.Uint32(stream[4:])
		if object == 1 && word&0xffff == 0 {
			t.Fatal("the slow client was sent a protocol error instead of being dropped for back-pressure")
		}
		stream = stream[word>>16:]
	}
}

func TestCloseDisconnectsClients(t *testing.T) {
	h := start(t)
	d := h.connect()
	h.registry(d)
	h.on(func() {
		if err := h.disp.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := d.Dispatch(); err == nil {
		t.Fatal("client still connected after Close")
	}
}

func TestDestroyHooksRunOnDestructorAndDisconnect(t *testing.T) {
	h := start(t)
	var ran []string
	h.on(func() {
		if _, err := h.disp.AddGlobal(sw.WlCompositorInterface, 6, func(c *Client, id wire.ObjectID, version uint32) error {
			comp, err := sw.NewWlCompositor(c, id, version, releasable{})
			if err != nil {
				return err
			}
			c.OnDestroy(comp, func() error {
				ran = append(ran, "compositor")
				return nil
			})
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	d := h.connect()
	reg, _ := h.registry(d)
	first, err := cw.NewWlCompositor(d, 6)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cw.NewWlCompositor(d, 6)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*cw.WlCompositor{first, second} {
		if err := reg.Bind(1, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err == nil {
		t.Fatal("release is a version 7 request and should not be sendable at version 6")
	}
	h.on(func() {
		c := h.disp.clients[0]
		if err := c.Remove(first.Base().ID()); err != nil {
			t.Error(err)
		}
		if len(ran) != 1 {
			t.Errorf("hooks after a destructor: %v", ran)
		}
	})
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	h.waitClients(0)
	h.on(func() {
		if len(ran) != 2 {
			t.Errorf("hooks after disconnect: %v, want both compositors", ran)
		}
	})
}

type releasable struct{ sw.WlCompositorHandler }

func TestFailingDestroyHooksAreReported(t *testing.T) {
	h := start(t)
	broken := errors.New("hook failed")
	h.on(func() {
		if _, err := h.disp.AddGlobal(sw.WlCompositorInterface, 6, func(c *Client, id wire.ObjectID, version uint32) error {
			comp, err := sw.NewWlCompositor(c, id, version, releasable{})
			if err != nil {
				return err
			}
			c.OnDestroy(comp, func() error { return broken })
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	d := h.connect()
	reg, _ := h.registry(d)
	first, err := cw.NewWlCompositor(d, 6)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cw.NewWlCompositor(d, 6)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(reg.Bind(1, first), reg.Bind(1, second), d.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	h.on(func() {
		if err := h.disp.clients[0].Remove(first.Base().ID()); !errors.Is(err, broken) {
			t.Errorf("Remove = %v, want the hook's error", err)
		}
		if err := h.disp.Close(); !errors.Is(err, broken) {
			t.Errorf("Close = %v, want the hook's error from tearing the client down", err)
		}
	})
}
