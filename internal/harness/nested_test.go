package harness

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/backend/nested"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/data"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/testenv"
	"github.com/stubbedev/tilde/internal/wm"
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
	return startNestedWith(t, host, func(*compositor.Config) {})
}

func startNestedWith(t *testing.T, host *Harness, configure func(*compositor.Config)) *nestedTilde {
	t.Helper()
	n := &nestedTilde{t: t, host: host, dir: t.TempDir()}
	var err error
	if n.loop, err = loop.New(); err != nil {
		t.Fatal(err)
	}
	b, err := nested.New(n.loop, host.Socket(), nested.Options{Renderer: nested.Soft})
	if err != nil {
		t.Fatal(err)
	}
	display, err := server.New(n.loop, server.Options{RuntimeDir: n.dir, Report: func(err error) { t.Error(err) }})
	if err != nil {
		t.Fatal(err)
	}
	n.display = display.Name()
	cfg := compositor.DefaultConfig(testenv.Keymap(t), testenv.Font(t))
	cfg.Report = func(err error) { t.Errorf("nested tilde reported: %v", err) }
	configure(&cfg)
	if n.comp, err = compositor.New(n.loop, display, b, cfg); err != nil {
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
		if err := errors.Join(n.comp.Close(), display.Close(), b.Close(), n.loop.Close()); err != nil {
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

func (n *nestedTilde) state() wm.State {
	var s wm.State
	n.do(func(c *compositor.Compositor) error {
		s = c.WM()
		return nil
	})
	return s
}

func placementsOf(s wm.State) map[wm.WindowID]wm.Placement {
	out := map[wm.WindowID]wm.Placement{}
	for _, p := range s.Arrange(0).Windows {
		out[p.ID] = p
	}
	return out
}

func (h *Harness) chord(keys ...uint32) {
	for _, k := range keys {
		h.key(k, true)
	}
	for _, k := range slices.Backward(keys) {
		h.key(k, false)
	}
}

const (
	keyLeft = 105
	keyT    = 20
	keyW    = 17
	keyOne  = 2
	keyTwo  = 3
	keyCtrl = 29
)

func TestNestedTilingSession(t *testing.T) {
	host := Start(t, desktop)
	n := startNestedWith(t, host, func(cfg *compositor.Config) {
		specs := []bindSpec{
			{chord: "SUPER + Return", action: "exec alacritty-swrast"},
			{chord: "SUPER + CTRL + Return", action: "exec alacritty-swrast --title float-me"},
			{chord: "SUPER + Left", action: "focus left"},
			{chord: "SUPER + Right", action: "focus right"},
			{chord: "SUPER + SHIFT + Right", action: "move right"},
			{chord: "SUPER + W", action: "makegroup tab"},
			{chord: "SUPER + T", action: "togglefloating"},
			{chord: "SUPER + 1", action: "workspace 1"},
			{chord: "SUPER + 2", action: "workspace 2"},
		}
		withBinds(t, specs...)(cfg)
		cfg.Rules = []wm.Rule{{Title: regexp.MustCompile(`^float-me$`), Mode: wm.RuleFloat, Size: image.Pt(300, 200)}}
	})
	host.Eventually("the nested tilde window", mappedToplevels(1))
	mapped := func(want int) {
		t.Helper()
		n.eventually(fmt.Sprintf("%d windows", want), func(c *compositor.Compositor) bool {
			count := 0
			for _, tl := range c.Toplevels() {
				if tl.Mapped() {
					count++
				}
			}
			return count == want
		})
	}
	ids := func() []wm.WindowID {
		var out []wm.WindowID
		for _, ws := range n.state().Workspaces() {
			out = append(out, ws.Tree.Windows()...)
		}
		return out
	}

	for i := range 3 {
		host.chord(keySuper, keyEnter)
		mapped(i + 1)
	}
	p := placementsOf(n.state())
	w := ids()
	if p[w[0]].Rect.Dx() < 400 || p[w[0]].Rect.Max.X != p[w[1]].Rect.Min.X || p[w[1]].Rect.Max.X != p[w[2]].Rect.Min.X {
		t.Fatalf("three terminals do not split the output side by side: %v", p)
	}

	host.chord(keySuper, keyEnter)
	mapped(4)
	p = placementsOf(n.state())
	w = ids()
	if p[w[3]].Rect.Min.X != p[w[2]].Rect.Min.X || p[w[3]].Rect.Min.Y == p[w[2]].Rect.Min.Y {
		t.Fatalf("autotile did not stack the fourth terminal under the third: %v", p)
	}

	host.chord(keySuper, keyLeft)
	n.eventually("focus left from the stacked column", func(c *compositor.Compositor) bool {
		f, _ := c.WM().Focused()
		return f == w[1]
	})
	host.chord(keySuper, keyShift, keyRight)
	n.eventually("the second terminal moved into the stacked column", func(c *compositor.Compositor) bool {
		p := placementsOf(c.WM())
		return p[w[1]].Rect.Min.X == p[w[3]].Rect.Min.X
	})

	host.chord(keySuper, keyW)
	n.eventually("a tab group", func(c *compositor.Compositor) bool { return len(c.WM().Arrange(20).TabBars) == 1 })

	host.chord(keySuper, keyT)
	n.eventually("the focused terminal floating", func(c *compositor.Compositor) bool {
		f, _ := c.WM().Focused()
		return placementsOf(c.WM())[f].Layer == wm.LayerFloating
	})

	host.chord(keySuper, keyTwo)
	n.eventually("workspace 2", func(c *compositor.Compositor) bool { return c.WM().Outputs()[0].Active == 2 })
	host.chord(keySuper, keyCtrl, keyEnter)
	mapped(5)
	n.eventually("the rule-floated terminal", func(c *compositor.Compositor) bool {
		for _, pl := range c.WM().Arrange(0).Windows {
			if pl.Layer == wm.LayerFloating && pl.Visible && pl.Rect.Size() == image.Pt(302, 202) {
				return true
			}
		}
		return false
	})
	host.chord(keySuper, keyOne)
	n.eventually("workspace 1", func(c *compositor.Compositor) bool { return c.WM().Outputs()[0].Active == 1 })
}
