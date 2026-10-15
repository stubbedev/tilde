package seat

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/testenv"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wlclient"
	"github.com/stubbedev/tilde/internal/xkb"
)

type surfaces struct {
	seat *Seat
	list []*surface.Surface
}

func (s *surfaces) Committed(sf *surface.Surface) error {
	if !slices.Contains(s.list, sf) {
		s.list = append(s.list, sf)
	}
	return nil
}

func (s *surfaces) Destroyed(sf *surface.Surface) error {
	return s.seat.SurfaceDestroyed(sf)
}

type env struct {
	t      *testing.T
	loop   *loop.Loop
	seat   *Seat
	keymap *xkb.Keymap
	dir    string
	disp   *server.Display
	surf   *surfaces
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
	e := &env{t: t, loop: l, dir: dir, keymap: testenv.Keymap(t), surf: &surfaces{}}
	if e.disp, err = server.New(l, server.Options{RuntimeDir: dir, Report: func(err error) { t.Error(err) }}); err != nil {
		t.Fatal(err)
	}
	if _, err := surface.New(e.disp, e.surf); err != nil {
		t.Fatal(err)
	}
	if e.seat, err = New(e.disp, Config{Keymap: e.keymap, RepeatRate: DefaultRepeatRate, RepeatDelay: DefaultRepeatDelay}, quietListener{}); err != nil {
		t.Fatal(err)
	}
	e.surf.seat = e.seat
	if err := e.seat.AddDevice(sw.WlSeatCapabilityKeyboard); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		e.on(func() { l.Stop() })
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
		e.seat.Close()
		_ = e.disp.Close()
		_ = l.Close()
		_ = os.RemoveAll(dir)
	})
	return e
}

func (e *env) on(f func()) {
	ran := make(chan struct{})
	if err := e.loop.Post(func() error {
		f()
		close(ran)
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
	<-ran
}

type registry map[string]uint32

func (r registry) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	r[iface] = name
	return nil
}

func (r registry) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

type seatEvents struct {
	caps []cw.WlSeatCapability
	name string
}

func (s *seatEvents) Capabilities(_ *cw.WlSeat, c cw.WlSeatCapability) error {
	s.caps = append(s.caps, c)
	return nil
}

func (s *seatEvents) Name(_ *cw.WlSeat, n string) error {
	s.name = n
	return nil
}

type keyboardEvents struct {
	log     []string
	keymap  string
	format  cw.WlKeyboardKeymapFormat
	repeat  [2]int32
	entered []uint32
	mods    []uint32
}

func (k *keyboardEvents) Keymap(_ *cw.WlKeyboard, f cw.WlKeyboardKeymapFormat, fd int, size uint32) error {
	defer unix.Close(fd)
	m, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		return err
	}
	k.keymap, k.format = string(m), f
	return unix.Munmap(m)
}

func (k *keyboardEvents) Enter(_ *cw.WlKeyboard, _ uint32, s *cw.WlSurface, keys []byte) error {
	k.entered = nil
	for i := 0; i+4 <= len(keys); i += 4 {
		k.entered = append(k.entered, binary.NativeEndian.Uint32(keys[i:]))
	}
	k.log = append(k.log, "enter")
	return nil
}

func (k *keyboardEvents) Leave(*cw.WlKeyboard, uint32, *cw.WlSurface) error {
	k.log = append(k.log, "leave")
	return nil
}

func (k *keyboardEvents) Key(_ *cw.WlKeyboard, _, _ uint32, key uint32, state cw.WlKeyboardKeyState) error {
	if state == cw.WlKeyboardKeyStatePressed {
		k.log = append(k.log, "press")
	} else {
		k.log = append(k.log, "release")
	}
	return nil
}

func (k *keyboardEvents) Modifiers(_ *cw.WlKeyboard, _, depressed, _, _, group uint32) error {
	k.mods = append(k.mods, depressed)
	k.log = append(k.log, "modifiers")
	return nil
}

func (k *keyboardEvents) RepeatInfo(_ *cw.WlKeyboard, rate, delay int32) error {
	k.repeat = [2]int32{rate, delay}
	return nil
}

type client struct {
	t     *testing.T
	d     *wlclient.Display
	seat  *cw.WlSeat
	comp  *cw.WlCompositor
	seatE *seatEvents
}

func (e *env) connect() *client {
	e.t.Helper()
	d, err := wlclient.Connect(filepath.Join(e.dir, e.disp.Name()))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = d.Close() })
	c := &client{t: e.t, d: d, seatE: &seatEvents{}}
	globals := registry{}
	reg, err := d.WlDisplay().GetRegistry(globals)
	if err != nil {
		e.t.Fatal(err)
	}
	c.roundtrip()
	if c.seat, err = cw.NewWlSeat(d, 9, c.seatE); err != nil {
		e.t.Fatal(err)
	}
	if c.comp, err = cw.NewWlCompositor(d, 6); err != nil {
		e.t.Fatal(err)
	}
	if err := errors.Join(reg.Bind(globals["wl_seat"], c.seat), reg.Bind(globals["wl_compositor"], c.comp)); err != nil {
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

func (c *client) keyboard() *keyboardEvents {
	c.t.Helper()
	k := &keyboardEvents{}
	if _, err := c.seat.GetKeyboard(k); err != nil {
		c.t.Fatal(err)
	}
	c.roundtrip()
	return k
}

func (c *client) surface() *cw.WlSurface {
	c.t.Helper()
	s, err := c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	if err != nil {
		c.t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		c.t.Fatal(err)
	}
	c.roundtrip()
	return s
}

func (e *env) focus(i int) {
	e.t.Helper()
	e.on(func() {
		if err := e.seat.SetKeyboardFocus(e.surf.list[i]); err != nil {
			e.t.Error(err)
		}
	})
}

func (e *env) key(code uint32, pressed bool) {
	e.t.Helper()
	e.on(func() {
		if err := e.seat.Key(code, pressed, time.Second); err != nil {
			e.t.Error(err)
		}
	})
}

func TestSeatAdvertisesKeyboardAndKeymap(t *testing.T) {
	e := setup(t)
	c := e.connect()
	if !slices.Equal(c.seatE.caps, []cw.WlSeatCapability{cw.WlSeatCapabilityKeyboard}) || c.seatE.name != "seat0" {
		t.Fatalf("seat events: caps %v name %q", c.seatE.caps, c.seatE.name)
	}
	k := c.keyboard()
	if k.format != cw.WlKeyboardKeymapFormatXkbV1 || k.keymap != e.keymap.Text()+"\x00" {
		t.Fatalf("keymap format %d, %d bytes; want the served xkb_v1 text", k.format, len(k.keymap))
	}
	if k.repeat != [2]int32{DefaultRepeatRate, DefaultRepeatDelay} {
		t.Fatalf("repeat info %v", k.repeat)
	}
	if _, err := c.seat.GetPointer(struct{ cw.WlPointerHandler }{}); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := c.d.Roundtrip(); !errors.As(err, &pe) || pe.Code != uint32(sw.WlSeatErrorMissingCapability) {
		t.Fatalf("get_pointer without a pointer = %v, want missing_capability", err)
	}
}

func TestFocusKeysAndModifiers(t *testing.T) {
	e := setup(t)
	a, b := e.connect(), e.connect()
	ka, kb := a.keyboard(), b.keyboard()
	a.surface()
	sb := b.surface()
	e.key(30, true)
	e.focus(0)
	a.roundtrip()
	if !slices.Equal(ka.log, []string{"enter", "modifiers"}) || !slices.Equal(ka.entered, []uint32{30}) {
		t.Fatalf("on focus: %v with keys %v; want enter carrying the held key, then modifiers", ka.log, ka.entered)
	}
	e.key(30, false)
	e.key(42, true)
	e.key(42, false)
	a.roundtrip()
	if want := []string{"enter", "modifiers", "release", "press", "modifiers", "release", "modifiers"}; !slices.Equal(ka.log, want) {
		t.Fatalf("log %v, want %v", ka.log, want)
	}
	if ka.mods[1] == 0 || ka.mods[2] != 0 {
		t.Fatalf("modifier masks %v: shift should set then clear depressed", ka.mods)
	}
	e.focus(1)
	a.roundtrip()
	b.roundtrip()
	if ka.log[len(ka.log)-1] != "leave" || !slices.Equal(kb.log, []string{"enter", "modifiers"}) {
		t.Fatalf("focus change: a %v, b %v", ka.log, kb.log)
	}
	e.key(30, true)
	a.roundtrip()
	b.roundtrip()
	if ka.log[len(ka.log)-1] != "leave" || kb.log[len(kb.log)-1] != "press" {
		t.Fatal("a key went to the client without focus")
	}
	late := b.keyboard()
	if !slices.Equal(late.log, []string{"enter", "modifiers"}) || !slices.Equal(late.entered, []uint32{30}) {
		t.Fatalf("a keyboard created while focused got %v keys %v", late.log, late.entered)
	}
	if err := sb.Destroy(); err != nil {
		t.Fatal(err)
	}
	b.roundtrip()
	e.on(func() {
		if e.seat.Focus() != nil {
			t.Error("focus survived the surface's destruction")
		}
	})
}

func TestCapabilitiesFollowDevices(t *testing.T) {
	e := setup(t)
	c := e.connect()
	e.on(func() {
		if err := e.seat.RemoveDevice(sw.WlSeatCapabilityKeyboard | sw.WlSeatCapabilityPointer); err == nil {
			t.Error("removing a keyboard and a pointer that was never added succeeded")
		}
		if err := e.seat.RemoveDevice(sw.WlSeatCapabilityKeyboard); err != nil {
			t.Errorf("the keyboard was taken by a refused removal: %v", err)
		}
		if err := e.seat.RemoveDevice(sw.WlSeatCapabilityKeyboard); err == nil {
			t.Error("removing a keyboard that was never added succeeded")
		}
	})
	c.roundtrip()
	if !slices.Equal(c.seatE.caps, []cw.WlSeatCapability{cw.WlSeatCapabilityKeyboard, 0}) {
		t.Fatalf("capabilities %v, want keyboard then none", c.seatE.caps)
	}
	c.keyboard()
	e.key(30, false)
}

type quietListener struct{}

func (quietListener) CursorChanged() error { return nil }

func (quietListener) KeyboardFocusChanged(*surface.Surface) error { return nil }

func (quietListener) PointerFocusChanged(*surface.Surface) error { return nil }
