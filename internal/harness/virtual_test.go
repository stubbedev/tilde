package harness

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend/headless"
	cvk "github.com/stubbedev/tilde/internal/protocol/client/virtualkeyboard"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cvp "github.com/stubbedev/tilde/internal/protocol/client/wlrvirtualpointer"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wire"
)

const typedKeymap = "xkb_keymap { xkb_keycodes \"typed\" {}; };\x00"

func keymapFD(t *testing.T, padding int) (int, uint32) {
	t.Helper()
	fd, err := unix.MemfdCreate("keymap", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	keymap := typedKeymap + strings.Repeat("\n", padding)
	if _, err := unix.Write(fd, []byte(keymap)); err != nil {
		t.Fatal(err)
	}
	return fd, uint32(len(keymap))
}

func TestVirtualKeyboardsTypeWithTheirOwnKeymap(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	real := r.in.KeymapSize
	typist := testclient.Connect(t, r.h.Socket())
	seat, _ := typist.Seat()
	mgr, err := cvk.NewZwpVirtualKeyboardManagerV1(typist.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	typist.Bind("zwp_virtual_keyboard_manager_v1", mgr)
	kb, err := mgr.CreateVirtualKeyboard(seat)
	if err != nil {
		t.Fatal(err)
	}
	fd, size := keymapFD(t, 0)
	if err := errors.Join(kb.Keymap(uint32(cw.WlKeyboardKeymapFormatXkbV1), fd, size), unix.Close(fd),
		kb.Key(1, 30, uint32(cw.WlKeyboardKeyStatePressed)), kb.Key(2, 30, uint32(cw.WlKeyboardKeyStateReleased)), typist.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect("key 30 1", "key 30 0")
	if r.in.KeymapSize != size {
		t.Fatalf("typed with a %d-byte keymap, want the virtual keyboard's %d", r.in.KeymapSize, size)
	}
	fd, size = keymapFD(t, 7)
	if err := errors.Join(kb.Keymap(uint32(cw.WlKeyboardKeymapFormatXkbV1), fd, size), unix.Close(fd),
		kb.Key(3, 30, uint32(cw.WlKeyboardKeyStatePressed)), kb.Key(4, 30, uint32(cw.WlKeyboardKeyStateReleased)), typist.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect("key 30 1", "key 30 0")
	if r.in.KeymapSize != size {
		t.Fatalf("after a second keymap, typed with a %d-byte keymap, want the new %d", r.in.KeymapSize, size)
	}
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Keyboard().Key(31, true), b.Keyboard().Key(31, false))
	})
	r.expect("key 31 1", "key 31 0")
	if r.in.KeymapSize != real {
		t.Fatalf("a physical key came with a %d-byte keymap, want the real %d", r.in.KeymapSize, real)
	}
	nokeys, err := mgr.CreateVirtualKeyboard(seat)
	if err != nil {
		t.Fatal(err)
	}
	if err := nokeys.Key(3, 30, 1); err != nil {
		t.Fatal(err)
	}
	typist.ExpectProtocolError(uint32(cvk.ZwpVirtualKeyboardV1ErrorNoKeymap))
}

func TestVirtualPointersMoveClickAndScroll(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	driver := testclient.Connect(t, r.h.Socket())
	seat, _ := driver.Seat()
	mgr, err := cvp.NewZwlrVirtualPointerManagerV1(driver.Display, 2)
	if err != nil {
		t.Fatal(err)
	}
	driver.Bind("zwlr_virtual_pointer_manager_v1", mgr)
	vp, err := mgr.CreateVirtualPointerWithOutput(seat, driver.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(vp.MotionAbsolute(1, 40, 25, 160, 100), vp.Frame(), driver.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect("motion 40,25", "frame")
	if err := errors.Join(vp.Motion(2, wire.FixedFromFloat(10), wire.FixedFromFloat(5)), vp.Frame(),
		vp.Button(3, 0x110, cw.WlPointerButtonStatePressed), vp.Frame(), vp.Button(4, 0x110, cw.WlPointerButtonStateReleased), vp.Frame(),
		vp.AxisSource(cw.WlPointerAxisSourceWheel), vp.AxisDiscrete(5, cw.WlPointerAxisVerticalScroll, wire.FixedFromFloat(15), 1), vp.Frame(),
		driver.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.expect("motion 50,30", "frame", "button 272 1", "frame", "button 272 0", "frame")
	r.c.DispatchUntil("the scroll", func() bool {
		return slices.ContainsFunc(r.in.Log, func(s string) bool { return s == "axis 0 15" })
	})
	if err := errors.Join(vp.AxisSource(cw.WlPointerAxisSourceFinger), vp.AxisStop(6, cw.WlPointerAxisVerticalScroll), vp.Frame(), driver.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	r.c.DispatchUntil("the scroll stop", func() bool { return slices.Contains(r.in.Log, "axis_stop 0") })
}
