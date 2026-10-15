package xwayland

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/data"
	"github.com/stubbedev/tilde/internal/loop"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cxs "github.com/stubbedev/tilde/internal/protocol/client/xwaylandshell"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wlclient"
)

func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Skipf("no /bin/sh to make a dead pid: %v", err)
	}
	return cmd.Process.Pid
}

func TestDisplaysSkipLiveLocksAndReclaimStaleOnes(t *testing.T) {
	dir := t.TempDir()
	for n, pid := range []int{os.Getpid(), deadPid(t)} {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf(".X%d-lock", n)), fmt.Appendf(nil, "%10d\n", pid), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	s, err := bindDisplay(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.n != 1 {
		t.Fatalf("took display :%d, want the stale :1", s.n)
	}
	lock, err := os.ReadFile(s.lockPath)
	if err != nil || string(lock) != fmt.Sprintf("%10d\n", os.Getpid()) {
		t.Fatalf("lock %q, %v", lock, err)
	}
	if info, err := os.Stat(filepath.Join(dir, ".X11-unix")); err != nil || info.Mode()&os.ModeSticky == 0 || info.Mode().Perm() != 0o777 {
		t.Fatalf("socket directory %v, %v", info.Mode(), err)
	}
	for _, name := range []string{s.path, "@" + s.path} {
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		err = unix.Connect(fd, &unix.SockaddrUnix{Name: name})
		_ = unix.Close(fd)
		if err != nil {
			t.Fatalf("connect %s: %v", name, err)
		}
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.path, s.lockPath} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s outlived the display: %v", p, err)
		}
	}
}

type noSurfaces struct{}

func (noSurfaces) Committed(*surface.Surface) error { return nil }
func (noSurfaces) Destroyed(*surface.Surface) error { return nil }

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (r registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type shellRig struct {
	t     *testing.T
	loop  *loop.Loop
	shell *Shell
	log   []string
	d     *wlclient.Display
	comp  *cw.WlCompositor
	xs    *cxs.XwaylandShellV1
}

func (r *shellRig) on(f func()) {
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

func startShell(t *testing.T) *shellRig {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	disp, err := server.New(l, server.Options{RuntimeDir: t.TempDir(), Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := surface.New(disp, noSurfaces{}); err != nil {
		t.Fatal(err)
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	xwayland, err := disp.AddClient(fds[0])
	if err != nil {
		t.Fatal(err)
	}
	r := &shellRig{t: t, loop: l}
	associated := func(serial uint64, _ *surface.Surface) error {
		r.log = append(r.log, fmt.Sprintf("associated %#x", serial))
		return nil
	}
	dissociated := func(serial uint64) error {
		r.log = append(r.log, fmt.Sprintf("dissociated %#x", serial))
		return nil
	}
	if r.shell, err = newShell(disp, func(c *server.Client) bool { return c == xwayland }, associated, dissociated); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		r.on(l.Stop)
		if err := errors.Join(<-done, disp.Close(), l.Close()); err != nil {
			t.Error(err)
		}
	})
	if r.d, err = wlclient.FromFD(fds[1]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.d.Close() })
	globals := registry{}
	reg, err := r.d.WlDisplay().GetRegistry(globals)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if r.comp, err = cw.NewWlCompositor(r.d, 6); err != nil {
		t.Fatal(err)
	}
	if r.xs, err = cxs.NewXwaylandShellV1(r.d, 1); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(reg.Bind(globals["wl_compositor"], r.comp), reg.Bind(globals["xwayland_shell_v1"], r.xs)); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *shellRig) surface() (*cw.WlSurface, *cxs.XwaylandSurfaceV1) {
	r.t.Helper()
	s, err := r.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	if err != nil {
		r.t.Fatal(err)
	}
	x, err := r.xs.GetXwaylandSurface(s)
	if err != nil {
		r.t.Fatal(err)
	}
	return s, x
}

func (r *shellRig) lookup(serial uint64) bool {
	var ok bool
	r.on(func() { _, ok = r.shell.Surface(serial) })
	return ok
}

func protocolError(t *testing.T, err error, code uint32, what string) {
	t.Helper()
	var pe *wire.ProtocolError
	if !errors.As(err, &pe) || pe.Code != code {
		t.Fatalf("%s: %v, want protocol error %d", what, err, code)
	}
}

func TestShellAssociatesSurfacesOnCommit(t *testing.T) {
	r := startShell(t)
	s, x := r.surface()
	const serial = uint64(7)<<32 | 5
	if err := errors.Join(x.SetSerial(5, 7), r.d.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	if r.lookup(serial) {
		t.Fatal("the serial applied before the commit")
	}
	if err := errors.Join(s.Commit(), x.Destroy(), r.d.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	if !r.lookup(serial) {
		t.Fatal("the committed serial names no surface")
	}
	if err := errors.Join(s.Destroy(), r.d.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	if r.lookup(serial) {
		t.Fatal("a destroyed surface stayed associated")
	}
	var log []string
	r.on(func() { log = slices.Clone(r.log) })
	if !slices.Equal(log, []string{"associated 0x700000005", "dissociated 0x700000005"}) {
		t.Fatalf("hooks %v", log)
	}
}

func TestShellErrors(t *testing.T) {
	for _, c := range []struct {
		what string
		code uint32
		do   func(r *shellRig) error
	}{
		{"serial 0", uint32(cxs.XwaylandSurfaceV1ErrorInvalidSerial), func(r *shellRig) error {
			_, x := r.surface()
			return x.SetSerial(0, 0)
		}},
		{"a second serial", uint32(cxs.XwaylandSurfaceV1ErrorAlreadyAssociated), func(r *shellRig) error {
			_, x := r.surface()
			return errors.Join(x.SetSerial(1, 0), x.SetSerial(2, 0))
		}},
		{"a second role object", uint32(cxs.XwaylandShellV1ErrorRole), func(r *shellRig) error {
			s, _ := r.surface()
			_, err := r.xs.GetXwaylandSurface(s)
			return err
		}},
	} {
		t.Run(strings.ReplaceAll(c.what, " ", "_"), func(t *testing.T) {
			r := startShell(t)
			if err := c.do(r); err != nil {
				t.Fatal(err)
			}
			protocolError(t, r.d.Roundtrip(), c.code, c.what)
		})
	}
}

type quietListener struct{}

func (quietListener) XWindowMapped(*Window) error               { return nil }
func (quietListener) XWindowUnmapped(*Window) error             { return nil }
func (quietListener) XWindowChanged(*Window) error              { return nil }
func (quietListener) XWindowRequest(*Window, Request) error     { return nil }
func (quietListener) XWindowResponsive(*Window, bool) error     { return nil }
func (quietListener) CurrentSelection(data.Kind) data.Source    { return nil }
func (quietListener) SetSelection(data.Kind, data.Source) error { return nil }

func TestAnXwaylandThatCannotStartIsTriedAgainNextTime(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "Xwayland")
	if err := os.WriteFile(binary, []byte("not a program"), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	d, err := server.New(l, server.Options{RuntimeDir: t.TempDir(), Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var reports []error
	s, err := Listen(l, d, Options{Binary: binary, Dir: dir, Log: io.Discard, Report: func(err error) { reports = append(reports, err) }, Listener: quietListener{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sock := filepath.Join(dir, ".X11-unix", "X"+strings.TrimPrefix(s.Display(), ":"))
	for attempt := 1; attempt <= 2; attempt++ {
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.Connect(fd, &unix.SockaddrUnix{Name: sock}); err != nil {
			t.Fatal(err)
		}
		stop, err := l.AddTimer(func() error {
			l.Stop()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(stop.Arm(50 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if err := l.Run(); err != nil {
			t.Fatalf("attempt %d: the loop ended with %v", attempt, err)
		}
		_ = stop.Remove()
		_ = unix.Close(fd)
		if len(reports) != attempt {
			t.Fatalf("attempt %d: reports %v", attempt, reports)
		}
	}
}
