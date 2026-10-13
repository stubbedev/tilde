// Package harness boots the compositor on the headless backend for
// integration tests and drives real clients against it.
package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render/soft"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/testenv"
)

type Harness struct {
	t          *testing.T
	RuntimeDir string
	loop       *loop.Loop
	display    *server.Display
	backend    *headless.Backend
	compositor *compositor.Compositor
	trace      *syncBuffer
	done       chan error
	clients    []*Client
}

func Start(t *testing.T, modes ...backend.Mode) *Harness {
	t.Helper()
	dir, err := os.MkdirTemp("", "tilde-")
	if err != nil {
		t.Fatalf("harness: runtime dir: %v", err)
	}
	h := &Harness{t: t, RuntimeDir: dir, trace: &syncBuffer{}, done: make(chan error, 1)}
	t.Cleanup(h.stop)
	if h.loop, err = loop.New(); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if h.display, err = server.New(h.loop, server.Options{RuntimeDir: dir, Trace: h.trace}); err != nil {
		t.Fatalf("harness: %v", err)
	}
	h.backend = headless.New(h.loop)
	for _, m := range modes {
		if _, err := h.backend.AddOutput(m); err != nil {
			t.Fatalf("harness: %v", err)
		}
	}
	if h.compositor, err = compositor.New(h.loop, h.display, h.backend, compositor.DefaultConfig(testenv.Keymap(t))); err != nil {
		t.Fatalf("harness: %v", err)
	}
	go func() { h.done <- h.loop.Run() }()
	return h
}

func (h *Harness) stop() {
	for _, c := range h.clients {
		c.kill()
	}
	if h.compositor != nil {
		defer h.compositor.Close()
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
	if h.backend != nil {
		errs = append(errs, h.backend.Close())
	}
	if h.display != nil {
		errs = append(errs, h.display.Close())
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
	path := testenv.Require(h.t, binary)
	cmd := exec.CommandContext(context.Background(), path, args...)
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+h.RuntimeDir, "WAYLAND_DISPLAY="+h.display.Name())
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
