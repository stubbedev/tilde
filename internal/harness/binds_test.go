package harness

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/binds"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wm"
)

const (
	keySuper  = 125
	keyShift  = 42
	keyQ      = 16
	keyR      = 19
	keyL      = 38
	btnMiddle = 274
)

type bindSpec struct {
	submap, chord, action string
	flags                 binds.Flags
}

func withBinds(t *testing.T, specs ...bindSpec) func(*compositor.Config) {
	return func(cfg *compositor.Config) {
		for _, s := range specs {
			chord, err := binds.ParseChord(s.chord, cfg.Seat.Keymap)
			if err != nil {
				t.Fatal(err)
			}
			a, err := wm.ParseAction(s.action)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Binds = append(cfg.Binds, binds.Bind{Submap: s.submap, Chord: chord, Action: a, Flags: s.flags})
		}
	}
}

func (h *Harness) key(code uint32, pressed bool) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error { return b.Keyboard().Key(code, pressed) })
}

func lines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

func eventuallyLines(t *testing.T, path string, cond func(int) bool) {
	t.Helper()
	deadline := time.Now().Add(clientTimeout)
	for !cond(lines(t, path)) {
		if time.Now().After(deadline) {
			t.Fatalf("%s has %d lines", path, lines(t, path))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestKeybinds(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	h := StartWith(t, withBinds(t,
		bindSpec{chord: "SUPER + Return", action: "exec echo spawned >> " + log},
		bindSpec{chord: "SUPER + SHIFT + Q", action: "kill"},
		bindSpec{chord: "SUPER + R", action: "submap resize"},
		bindSpec{submap: "resize", chord: "Return", action: "submap reset"},
		bindSpec{submap: "resize", chord: "L", action: "exec echo grow >> " + log, flags: binds.Flags{Repeating: true}},
		bindSpec{chord: "SUPER + mouse_down", action: "workspace e+1"},
		bindSpec{chord: "SUPER + mouse:274", action: "workspace 2"},
	), small)
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("win", quadrants)
	in.Name(w.Surface, "win")
	c.DispatchUntil("the window to map", w.Mapped)
	c.DispatchUntil("keyboard focus", func() bool { return slices.Contains(in.Log, "kb_enter win") })
	start := len(in.Log)

	h.key(keySuper, true)
	h.key(keyEnter, true)
	h.key(keyEnter, false)
	eventuallyLines(t, log, func(n int) bool { return n == 1 })

	h.key(keyShift, true)
	h.key(keyQ, true)
	h.key(keyQ, false)
	h.key(keyShift, false)
	c.DispatchUntil("the window asked to close", func() bool { return w.Closed })

	h.key(keyR, true)
	h.key(keyR, false)
	h.key(keySuper, false)
	h.key(keyL, true)
	eventuallyLines(t, log, func(n int) bool { return n >= 4 })
	h.key(keyL, false)
	settled := lines(t, log)
	time.Sleep(100 * time.Millisecond)
	if lines(t, log) != settled {
		t.Fatal("a repeating bind kept firing after its key was released")
	}
	h.key(keyEnter, true)
	h.key(keyEnter, false)
	h.key(keyL, true)
	h.key(keyL, false)
	c.DispatchUntil("the keys after leaving the submap", func() bool { return slices.Contains(in.Log, "key 38 0") })
	for _, e := range in.Log[start:] {
		if e == "key 28 1" || e == "key 16 1" || e == "key 19 1" {
			t.Fatalf("a bound key reached the client: %v", in.Log[start:])
		}
	}
	if !slices.Contains(in.Log[start:], "key 125 1") {
		t.Fatalf("the modifier never reached the client: %v", in.Log[start:])
	}

	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(btnMiddle, true), b.Pointer().Frame(), b.Pointer().Button(btnMiddle, false), b.Pointer().Frame())
	})
	h.Eventually("workspace 2 from the mouse bind", showingWorkspace(2))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Axis(backend.Vertical, backend.Wheel, 15, 120), b.Pointer().Frame())
	})
	h.Eventually("workspace 1 from the scroll bind", showingWorkspace(1))
	h.key(keySuper, false)
}

func showingWorkspace(id wm.WorkspaceID) func(*headless.Backend, *compositor.Compositor) bool {
	return func(_ *headless.Backend, c *compositor.Compositor) bool {
		s := c.WM()
		for _, o := range s.Outputs() {
			if o.Name == s.FocusedOutput() {
				return o.Active == id
			}
		}
		return false
	}
}

func TestDragsNeedAMouseBind(t *testing.T) {
	var r reports
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Report = r.report
		withBinds(t, bindSpec{chord: "SUPER + Q", action: "movewindow"}, bindSpec{chord: "SUPER + R", action: "resizewindow"})(cfg)
	}, small)
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("win", quadrants)
	in.Name(w.Surface, "win")
	c.DispatchUntil("the window to map", w.Mapped)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.MoveTo(b.Outputs()[0], 0.5, 0.5), p.Frame())
	})
	c.DispatchUntil("pointer focus", func() bool { return slices.Contains(in.Log, "enter win 80,50") })
	h.chord(keySuper, keyQ)
	h.chord(keySuper, keyR)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if c.Seat().PointerFocus() == nil {
			return errors.New("a key bind drag took pointer focus away")
		}
		return nil
	})
	errs := r.take()
	if len(errs) != 2 || !errors.Is(errs[0], compositor.ErrNotFromMouseBind) || !errors.Is(errs[1], compositor.ErrNotFromMouseBind) {
		t.Fatalf("reports %v", errs)
	}
	if slices.Contains(in.Log, "leave win") {
		t.Fatalf("the window lost the pointer: %v", in.Log)
	}
}
