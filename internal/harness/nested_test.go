package harness

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/backend/nested"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/data"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/testenv"
)

type nestedTilde struct {
	t       *testing.T
	host    *Harness
	loop    *loop.Loop
	comp    *compositor.Compositor
	dir     string
	display string
}

func startNested(t *testing.T, host *Harness) *nestedTilde {
	t.Helper()
	n := &nestedTilde{t: t, host: host, dir: t.TempDir()}
	var err error
	if n.loop, err = loop.New(); err != nil {
		t.Fatal(err)
	}
	b, err := nested.New(n.loop, host.Socket())
	if err != nil {
		t.Fatal(err)
	}
	display, err := server.New(n.loop, server.Options{RuntimeDir: n.dir})
	if err != nil {
		t.Fatal(err)
	}
	n.display = display.Name()
	if n.comp, err = compositor.New(n.loop, display, b, compositor.DefaultConfig(testenv.Keymap(t))); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- n.loop.Run() }()
	t.Cleanup(func() {
		if err := n.loop.Post(func() error { n.loop.Stop(); return nil }); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil && !errors.Is(err, nested.ErrClosed) {
			t.Error(err)
		}
		n.comp.Close()
		if err := errors.Join(display.Close(), b.Close(), n.loop.Close()); err != nil {
			t.Error(err)
		}
	})
	return n
}

func (n *nestedTilde) do(f func(c *compositor.Compositor) error) {
	n.t.Helper()
	result := make(chan error, 1)
	if err := n.loop.Post(func() error {
		result <- f(n.comp)
		return nil
	}); err != nil {
		n.t.Fatal(err)
	}
	if err := <-result; err != nil {
		n.t.Fatal(err)
	}
}

func (n *nestedTilde) eventually(what string, cond func(c *compositor.Compositor) bool) {
	n.t.Helper()
	deadline := time.Now().Add(clientTimeout)
	for {
		var ok bool
		n.do(func(c *compositor.Compositor) error {
			ok = cond(c)
			return nil
		})
		if ok {
			return
		}
		if time.Now().After(deadline) {
			n.t.Fatalf("nested tilde: timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (n *nestedTilde) run(binary string, args ...string) *Client {
	n.t.Helper()
	return n.host.RunOn(n.dir, n.display, binary, args...)
}

func nestedToplevels(want int) func(c *compositor.Compositor) bool {
	return func(c *compositor.Compositor) bool {
		return mappedToplevels(want)(nil, c)
	}
}

func TestNestedTildeHidesTheHostCursor(t *testing.T) {
	host := Start(t, small)
	startNested(t, host)
	host.Eventually("the nested window under the pointer with no host cursor", func(_ *headless.Backend, c *compositor.Compositor) bool {
		cur := c.Seat().Cursor()
		return c.Seat().PointerFocus() != nil && cur.Set() && cur.Surface() == nil && cur.Shape() == ""
	})
}

func TestNestedSessionWithRealClients(t *testing.T) {
	t.Setenv("GOELM_DEBUG", "demo")
	host := Start(t, desktop)
	n := startNested(t, host)
	host.Eventually("the nested tilde window", mappedToplevels(1))
	n.do(func(c *compositor.Compositor) error {
		return c.SetOutputScale(c.Outputs()[0].Backend(), 180)
	})

	typed := filepath.Join(t.TempDir(), "typed")
	terminal := n.run("alacritty-swrast", "-e", "sh", "-c", `read -r line; printf %s "$line" > "$0"`, typed)
	n.eventually("alacritty to map", nestedToplevels(1))
	n.eventually("alacritty to take the keyboard", func(c *compositor.Compositor) bool {
		return c.Seat().Focus() == c.Toplevels()[0].Surface()
	})
	host.tap(keyE, keyC, keyH, keyO, keyEnter)
	terminal.Wait(t, clientTimeout)
	got, err := os.ReadFile(typed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "echo" {
		t.Fatalf("alacritty read %q from the host keyboard, want echo", got)
	}
	n.eventually("alacritty gone", func(c *compositor.Compositor) bool { return len(c.Toplevels()) == 0 })

	hello := n.run("gelm-hello")
	hello.Expect(t, "demo: mapped", clientTimeout)
	n.eventually("gelm-hello to map", nestedToplevels(1))
	host.click(0, 900, 500, btnRight)
	hello.Expect(t, "demo: menu open", clientTimeout)
	n.eventually("the menu with the keyboard", func(c *compositor.Compositor) bool {
		popups := c.Toplevels()[0].Popups()
		return len(popups) == 1 && popups[0].Mapped() && c.Seat().Focus() == popups[0].Surface()
	})

	host.tap(keyEsc)
	n.eventually("the menu closed by escape", func(c *compositor.Compositor) bool {
		return len(c.Toplevels()[0].Popups()) == 0 && c.Seat().Focus() == c.Toplevels()[0].Surface()
	})

	n.run("wl-copy", "--foreground", "nested clipboard")
	n.eventually("the copied selection", func(c *compositor.Compositor) bool { return c.Selection(data.Clipboard) })
	if out := n.run("wl-paste", "--no-newline").Wait(t, clientTimeout); out != "nested clipboard" {
		t.Fatalf("wl-paste printed %q", out)
	}
}
