package harness

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/binds"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/seat"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/testenv"
	"github.com/stubbedev/tilde/internal/xkb"
)

func TestReloadSwapsBindsKeymapAndRepeat(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	dk, err := testenv.XKBLibrary(t).Compile(xkb.Names{Layout: "dk"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dk.Close() })
	var next compositor.Config
	var comp *compositor.Compositor
	h := StartWith(t, func(cfg *compositor.Config) {
		withBinds(t,
			bindSpec{chord: "SUPER + Return", action: "exec echo old >> " + log},
			bindSpec{chord: "SUPER + Q", action: "reload"},
		)(cfg)
		next = compositor.DefaultConfig(dk, cfg.Font)
		next.Report = cfg.Report
		next.Seat.RepeatRate, next.Seat.RepeatDelay = 10, 700
		next.Decor.BorderWidth = 3
		withBinds(t, bindSpec{chord: "SUPER + R", action: "exec echo new >> " + log})(&next)
		cfg.Reload = func() error { return comp.Reconfigure(next) }
	}, small)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		comp = c
		return nil
	})
	c := testclient.Connect(t, h.Socket())
	_, in := c.Seat()
	w := c.Window("win", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	c.DispatchUntil("the first keymap", func() bool { return in.KeymapSize != 0 })
	first := in.KeymapSize

	h.key(keySuper, true)
	h.key(keyEnter, true)
	h.key(keyEnter, false)
	eventuallyLines(t, log, func(n int) bool { return n == 1 })
	h.key(keyQ, true)
	h.key(keyQ, false)
	c.DispatchUntil("the new keymap and repeat info", func() bool {
		return in.KeymapSize == dk.Size() && in.Repeat == [2]int32{10, 700}
	})
	if first == dk.Size() {
		t.Fatal("the test keymaps have the same size, so the swap is not observable")
	}
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if c.Seat().DepressedModifiers() == 0 {
			return errors.New("super is held, but the new keymap state has no modifier down")
		}
		return nil
	})
	h.key(keyEnter, true)
	h.key(keyEnter, false)
	h.key(keyR, true)
	h.key(keyR, false)
	h.key(keySuper, false)
	eventuallyLines(t, log, func(n int) bool { return n == 2 })
	if text, err := os.ReadFile(log); err != nil || string(text) != "old\nnew\n" {
		t.Fatalf("binds after the reload ran: %q, %v", text, err)
	}
	c.DispatchUntil("the window inside the new border", func() bool { return w.Size == image.Pt(160-6, 100-6) })

	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		bad := next
		bad.Font = nil
		if err := c.Reconfigure(bad); err == nil {
			return errors.New("a config without a font was accepted")
		}
		return nil
	})
	h.key(keySuper, true)
	h.key(keyR, true)
	h.key(keyR, false)
	h.key(keySuper, false)
	eventuallyLines(t, log, func(n int) bool { return n == 3 })
}

var (
	big    = backend.Mode{Size: image.Pt(320, 200), RefreshMHz: 60000, Preferred: true}
	medium = backend.Mode{Size: image.Pt(240, 150), RefreshMHz: 59940}
	medHz  = backend.Mode{Size: image.Pt(240, 150), RefreshMHz: 75000}
)

type reports struct {
	mu   sync.Mutex
	errs []error
}

func (r *reports) report(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *reports) take() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.errs
	r.errs = nil
	return out
}

type placed struct {
	name     string
	at       image.Point
	scale    output.Scale
	mode     image.Point
	enabled  bool
	inLayout bool
}

func layout(b *headless.Backend, c *compositor.Compositor) []placed {
	var out []placed
	for _, bo := range b.Outputs() {
		p := placed{name: bo.Name(), mode: bo.State().Mode.Size, enabled: bo.State().Enabled}
		for _, o := range c.Outputs() {
			if o.Backend() == backend.Output(bo) {
				p.at, p.scale, p.inLayout = o.Position(), o.Scale(), true
			}
		}
		out = append(out, p)
	}
	return out
}

func TestOutputRules(t *testing.T) {
	at := image.Pt(1000, 50)
	wanted := compositor.ExactMode{Size: image.Pt(240, 150)}
	missing := compositor.ExactMode{Size: image.Pt(800, 600), RefreshMHz: 60000}
	var r reports
	var cfg compositor.Config
	h := StartWith(t, func(c *compositor.Config) {
		c.Report = r.report
		c.Outputs = []compositor.OutputRule{
			{Name: "HEADLESS-1", Mode: wanted, Scale: 2 * output.Unit, Position: &at},
			{Description: "Headless output HEADLESS-2", Disabled: true},
			{Name: "HEADLESS-4", Mode: missing},
		}
		cfg = *c
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		for range 4 {
			if _, err := b.AddOutput(big, medium, medHz); err != nil {
				return err
			}
		}
		return nil
	})
	want := []placed{
		{"HEADLESS-1", at, 2 * output.Unit, image.Pt(240, 150), true, true},
		{"HEADLESS-2", image.Point{}, 0, image.Pt(320, 200), false, false},
		{"HEADLESS-3", image.Pt(1120, 0), output.Unit, image.Pt(320, 200), true, true},
		{"HEADLESS-4", image.Pt(1440, 0), output.Unit, image.Pt(320, 200), true, true},
	}
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		if got := layout(b, c); !slices.Equal(got, want) {
			t.Errorf("outputs\n got %+v\nwant %+v", got, want)
		}
		if got := b.Outputs()[0].State().Mode.RefreshMHz; got != medHz.RefreshMHz {
			t.Errorf("a mode without a rate took %d mHz, not the fastest", got)
		}
		if n := len(c.WM().Outputs()); n != 3 {
			t.Errorf("the window manager has %d outputs, want 3", n)
		}
		return nil
	})
	if errs := r.take(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "HEADLESS-4: no mode 800x600") {
		t.Fatalf("reports %v", errs)
	}

	cfg.Outputs = []compositor.OutputRule{{Name: "HEADLESS-3", Disabled: true}}
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		if err := c.Reconfigure(cfg); err != nil {
			return err
		}
		want := []placed{
			{"HEADLESS-1", image.Pt(0, 0), output.Unit, image.Pt(320, 200), true, true},
			{"HEADLESS-2", image.Pt(640, 0), output.Unit, image.Pt(320, 200), true, true},
			{"HEADLESS-3", image.Point{}, 0, image.Pt(320, 200), false, false},
			{"HEADLESS-4", image.Pt(320, 0), output.Unit, image.Pt(320, 200), true, true},
		}
		if got := layout(b, c); !slices.Equal(got, want) {
			t.Errorf("outputs after the reload\n got %+v\nwant %+v", got, want)
		}
		names := []string{}
		for _, o := range c.WM().Outputs() {
			names = append(names, o.Name)
		}
		if !slices.Equal(names, []string{"HEADLESS-1", "HEADLESS-4", "HEADLESS-2"}) {
			t.Errorf("window manager outputs %v", names)
		}
		return nil
	})
	if errs := r.take(); len(errs) != 0 {
		t.Fatalf("reports %v", errs)
	}
}

func TestRepeatingBindsWithRepeatOff(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Seat.RepeatRate = 0
		withBinds(t, bindSpec{chord: "SUPER + L", action: "exec echo grow >> " + log, flags: binds.Flags{Repeating: true}})(cfg)
	}, small)
	h.key(keySuper, true)
	h.key(keyL, true)
	eventuallyLines(t, log, func(n int) bool { return n == 1 })
	time.Sleep(time.Duration(seat.DefaultRepeatDelay+100) * time.Millisecond)
	h.key(keyL, false)
	h.key(keySuper, false)
	if n := lines(t, log); n != 1 {
		t.Fatalf("a repeating bind fired %d times with key repeat off", n)
	}
}

func TestClosingTheLidDisablesTheInternalPanel(t *testing.T) {
	h := StartWith(t, func(c *compositor.Config) {
		c.Outputs = []compositor.OutputRule{{Name: "eDP-1", Scale: 2 * output.Unit}}
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddNamedOutput("eDP-1", small)
		if err != nil {
			return err
		}
		_, err = b.AddNamedOutput("DP-1", small)
		return err
	})
	c := testclient.Connect(t, h.Socket())
	w := c.Window("on the panel", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	names := func(comp *compositor.Compositor) []string {
		var out []string
		for _, o := range comp.Outputs() {
			out = append(out, o.Backend().Name())
		}
		return out
	}
	lid := func(closed bool) {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return b.Switches().Toggle(backend.SwitchLid, closed)
		})
	}
	lid(true)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if got := names(comp); !slices.Equal(got, []string{"DP-1"}) {
			t.Errorf("outputs with the lid closed: %v", got)
		}
		if ws, ok := comp.WM().WorkspaceOf(1); !ok || ws == 0 {
			t.Errorf("the window left the session: %v %v", ws, ok)
		}
		return nil
	})
	lid(false)
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		if got := names(comp); !slices.Equal(got, []string{"DP-1", "eDP-1"}) {
			t.Errorf("outputs with the lid open: %v", got)
		}
		for _, o := range comp.Outputs() {
			if o.Backend().Name() == "eDP-1" && o.Scale() != 2*output.Unit {
				t.Errorf("the panel came back at scale %d, not its rule's", o.Scale())
			}
		}
		return nil
	})
}

func TestARepeatedReloadThatTurnsRepeatOffStopsIt(t *testing.T) {
	var r reports
	var comp *compositor.Compositor
	reloads := 0
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Report = r.report
		cfg.Seat.RepeatRate, cfg.Seat.RepeatDelay = 50, 50
		withBinds(t, bindSpec{chord: "SUPER + Q", action: "reload", flags: binds.Flags{Repeating: true}})(cfg)
		next := *cfg
		next.Seat.RepeatRate = 0
		cfg.Reload = func() error {
			reloads++
			if reloads == 2 {
				return comp.Reconfigure(next)
			}
			return nil
		}
	}, small)
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		comp = c
		return nil
	})
	h.key(keySuper, true)
	h.key(keyQ, true)
	h.Eventually("the repeat to reload", func(*headless.Backend, *compositor.Compositor) bool { return reloads >= 2 })
	time.Sleep(200 * time.Millisecond)
	h.key(keyQ, false)
	h.key(keySuper, false)
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		if reloads != 2 {
			return fmt.Errorf("the bind fired %d times, want 2: repeat went on after it was turned off", reloads)
		}
		return nil
	})
	if errs := r.take(); len(errs) != 0 {
		t.Fatalf("reports %v", errs)
	}
}
