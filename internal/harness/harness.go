// Package harness boots the compositor on the headless backend for
// integration tests and drives real clients against it.
package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/ipc"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render/soft"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/testenv"
	"github.com/stubbedev/tilde/internal/xwayland"
)

type Harness struct {
	t          *testing.T
	RuntimeDir string
	loop       *loop.Loop
	display    *server.Display
	backend    *headless.Backend
	compositor *compositor.Compositor
	ipc        *ipc.Server
	ipcPath    string
	trace      *syncBuffer
	xwayland   *xwayland.Server
	xlog       *syncBuffer
	done       chan error
	clients    []*Client
}

type setup struct {
	open      func(*loop.Loop) (*headless.Backend, error)
	configure func(*compositor.Config)
	traced    bool
	xwayland  func(error)
}

func Start(t *testing.T, modes ...backend.Mode) *Harness {
	t.Helper()
	return StartWith(t, func(*compositor.Config) {}, modes...)
}

func softBackend(l *loop.Loop) (*headless.Backend, error) {
	return headless.New(l), nil
}

func StartWith(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	return start(t, setup{open: softBackend, configure: configure, traced: true}, modes...)
}

func StartWithCursorPlanes(t *testing.T, size image.Point, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	return start(t, setup{open: func(l *loop.Loop) (*headless.Backend, error) {
		b := headless.New(l)
		return b, b.SetCursorPlanes(size)
	}, configure: configure, traced: true}, modes...)
}

func StartUntraced(t *testing.T, modes ...backend.Mode) *Harness {
	t.Helper()
	return start(t, setup{open: softBackend, configure: func(*compositor.Config) {}}, modes...)
}

func StartGLES(t *testing.T, configure func(*compositor.Config), modes ...backend.Mode) *Harness {
	t.Helper()
	testenv.GPU(t)
	return start(t, setup{open: func(l *loop.Loop) (*headless.Backend, error) {
		return headless.NewGLES(l, gpu.PathsFromEnv(os.Getenv))
	}, configure: configure, traced: true}, modes...)
}

const XTransferTimeout = 2 * time.Second

func StartXwayland(t *testing.T, report func(error), modes ...backend.Mode) *Harness {
	t.Helper()
	testenv.Require(t, xwayland.Path(os.Getenv))
	return start(t, setup{open: softBackend, configure: func(*compositor.Config) {}, traced: true, xwayland: report}, modes...)
}

func start(t *testing.T, s setup, modes ...backend.Mode) *Harness {
	t.Helper()
	dir, err := os.MkdirTemp("", "tilde-")
	if err != nil {
		t.Fatalf("harness: runtime dir: %v", err)
	}
	h := &Harness{t: t, RuntimeDir: dir, trace: &syncBuffer{}, xlog: &syncBuffer{}, done: make(chan error, 1)}
	t.Cleanup(h.stop)
	if h.loop, err = loop.New(); err != nil {
		t.Fatalf("harness: %v", err)
	}
	opts := server.Options{RuntimeDir: dir, Report: func(err error) { t.Errorf("server reported: %v", err) }}
	if s.traced {
		opts.Trace = h.trace
	}
	if h.display, err = server.New(h.loop, opts); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if h.backend, err = s.open(h.loop); err != nil {
		t.Fatalf("harness: %v", err)
	}
	for _, m := range modes {
		if _, err := h.backend.AddOutput(m); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	cfg := compositor.DefaultConfig(testenv.Keymap(t), testenv.Font(t))
	cfg.Report = func(err error) { t.Errorf("compositor reported: %v", err) }
	cfg.Decor.BorderWidth = 0
	s.configure(&cfg)
	if h.compositor, err = compositor.New(h.loop, h.display, h.backend, cfg); err != nil {
		t.Fatalf("harness: %v", err)
	}
	socket, err := ipc.SocketPath(dir, h.display.Name())
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	h.ipcPath = socket
	if h.ipc, err = ipc.Listen(h.loop, socket, h.compositor, cfg.Report); err != nil {
		t.Fatalf("harness: %v", err)
	}
	h.compositor.OnDispatch(h.ipc.Dispatched)
	if s.xwayland != nil {
		opts := xwayland.Options{Binary: xwayland.Path(os.Getenv), Dir: dir, Env: os.Environ(), Log: h.xlog, Report: s.xwayland, Listener: h.compositor, TransferTimeout: XTransferTimeout}
		if h.xwayland, err = xwayland.Listen(h.loop, h.display, opts); err != nil {
			t.Fatalf("harness: %v", err)
		}
		h.compositor.WatchSelection(h.xwayland.SelectionChanged)
		if err := h.compositor.ScaleXwayland(h.xwayland.IsClient); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	go func() { h.done <- h.loop.Run() }()
	return h
}

func (h *Harness) stop() {
	for _, c := range h.clients {
		c.kill()
	}
	if h.loop != nil && h.compositor != nil {
		if err := h.loop.Post(func() error {
			h.loop.Stop()
			return nil
		}); err == nil {
			if err := <-h.done; err != nil {
				h.t.Errorf("harness: compositor loop: %v", err)
			}
		}
	}
	var errs []error
	if h.xwayland != nil {
		errs = append(errs, h.xwayland.Close())
	}
	if h.ipc != nil {
		errs = append(errs, h.ipc.Close())
	}
	if h.compositor != nil {
		errs = append(errs, h.compositor.Close())
	}
	if h.display != nil {
		errs = append(errs, h.display.Close())
	}
	if h.backend != nil {
		errs = append(errs, h.backend.Close())
	}
	if h.loop != nil {
		errs = append(errs, h.loop.Close())
	}
	errs = append(errs, os.RemoveAll(h.RuntimeDir))
	if err := errors.Join(errs...); err != nil {
		h.t.Errorf("harness: teardown: %v", err)
	}
	if h.t.Failed() {
		h.t.Logf("compositor wire trace:\n%s", h.trace.String())
		if log := h.xlog.String(); log != "" {
			h.t.Logf("Xwayland output:\n%s", log)
		}
	}
}

func (h *Harness) Do(f func(b *headless.Backend, c *compositor.Compositor) error) {
	h.t.Helper()
	result := make(chan error, 1)
	if err := h.loop.Post(func() error {
		result <- f(h.backend, h.compositor)
		return nil
	}); err != nil {
		h.t.Fatalf("harness: post to the compositor loop: %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			h.t.Fatal(err)
		}
	case err := <-h.done:
		h.done <- err
		h.t.Fatalf("harness: compositor loop ended: %v", err)
	}
}

func (h *Harness) Eventually(what string, cond func(b *headless.Backend, c *compositor.Compositor) bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var ok bool
		h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
			ok = cond(b, c)
			return nil
		})
		if ok {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("harness: timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *Harness) Rendered(i int) *soft.Buffer {
	h.t.Helper()
	var o *headless.Output
	var before uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		outputs := b.Outputs()
		if i >= len(outputs) {
			return fmt.Errorf("harness: no output %d of %d", i, len(outputs))
		}
		o, before = outputs[i], outputs[i].Frames()
		return o.ScheduleFrame()
	})
	h.Eventually(fmt.Sprintf("a new frame on output %d", i), func(*headless.Backend, *compositor.Compositor) bool {
		return o.Frames() > before
	})
	return h.Snapshot(i)
}

func (h *Harness) Snapshot(i int) *soft.Buffer {
	h.t.Helper()
	var snap *soft.Buffer
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		outputs := b.Outputs()
		if i >= len(outputs) {
			return fmt.Errorf("harness: no output %d of %d", i, len(outputs))
		}
		snap = outputs[i].Snapshot()
		return nil
	})
	return snap
}

func (h *Harness) Socket() string {
	return filepath.Join(h.RuntimeDir, h.display.Name())
}

func (h *Harness) Wakeups() int {
	h.t.Helper()
	var n int
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		n = h.loop.Wakeups()
		return nil
	})
	return n
}

func (h *Harness) IPCSocket() string {
	return h.ipcPath
}

func (h *Harness) Trace() string {
	return h.trace.String()
}

type Client struct {
	cmd    *exec.Cmd
	output syncBuffer
	exited chan error
}

func (h *Harness) Run(binary string, args ...string) *Client {
	h.t.Helper()
	return h.RunOn(h.RuntimeDir, h.display.Name(), binary, args...)
}

func (h *Harness) RunOn(runtimeDir, display, binary string, args ...string) *Client {
	h.t.Helper()
	path := testenv.Require(h.t, binary)
	cmd := exec.CommandContext(context.Background(), path, args...)
	socket, err := ipc.SocketPath(runtimeDir, display)
	if err != nil {
		h.t.Fatalf("harness: %v", err)
	}
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeDir, "WAYLAND_DISPLAY="+display, ipc.SocketEnv+"="+socket)
	c := &Client{cmd: cmd, exited: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = &c.output, &c.output
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("harness: start %s: %v", binary, err)
	}
	go func() { c.exited <- cmd.Wait() }()
	h.clients = append(h.clients, c)
	return c
}

func (c *Client) Wait(t *testing.T, timeout time.Duration) string {
	t.Helper()
	select {
	case err := <-c.exited:
		c.exited <- err
		if err != nil {
			t.Fatalf("harness: %s failed: %v\n%s", filepath.Base(c.cmd.Path), err, c.output.String())
		}
	case <-time.After(timeout):
		t.Fatalf("harness: %s still running after %v\n%s", filepath.Base(c.cmd.Path), timeout, c.output.String())
	}
	return c.output.String()
}

func (c *Client) Expect(t *testing.T, text string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		out := c.output.String()
		if i := strings.Index(out, text); i >= 0 {
			return out[i:]
		}
		select {
		case err := <-c.exited:
			c.exited <- err
			t.Fatalf("harness: %s exited (%v) before printing %q:\n%s", filepath.Base(c.cmd.Path), err, text, out)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("harness: %s did not print %q within %v:\n%s", filepath.Base(c.cmd.Path), text, timeout, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (c *Client) kill() {
	select {
	case err := <-c.exited:
		c.exited <- err
	default:
		_ = c.cmd.Process.Kill()
		<-c.exited
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
