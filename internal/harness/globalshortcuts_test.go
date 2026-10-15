package harness

import (
	"errors"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	chgs "github.com/stubbedev/tilde/internal/protocol/client/hyprlandglobalshortcuts"
	"github.com/stubbedev/tilde/internal/testclient"
)

type shortcutLog struct{ events []string }

func (l *shortcutLog) Pressed(*chgs.HyprlandGlobalShortcutV1, uint32, uint32, uint32) error {
	l.events = append(l.events, "pressed")
	return nil
}

func (l *shortcutLog) Released(*chgs.HyprlandGlobalShortcutV1, uint32, uint32, uint32) error {
	l.events = append(l.events, "released")
	return nil
}

func TestGlobalShortcutsFollowTheirBind(t *testing.T) {
	h := StartWith(t, withBinds(t, bindSpec{chord: "A", action: "global voice:ptt"}), small)
	c := testclient.Connect(t, h.Socket())
	mgr, err := chgs.NewHyprlandGlobalShortcutsManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	l := &shortcutLog{}
	if _, err := mgr.RegisterShortcut(l, "ptt", "voice", "Push to talk", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	h.key(30, true)
	c.DispatchUntil("the press", func() bool { return len(l.events) == 1 })
	h.key(30, false)
	c.DispatchUntil("the release", func() bool { return len(l.events) == 2 })
	h.dispatch("global voice:ptt")
	c.DispatchUntil("a dispatched tap", func() bool { return len(l.events) == 4 })
	if want := []string{"pressed", "released", "pressed", "released"}; !slices.Equal(l.events, want) {
		t.Fatalf("events %q, want %q", l.events, want)
	}

	if _, err := mgr.RegisterShortcut(&shortcutLog{}, "ptt", "voice", "again", ""); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(chgs.HyprlandGlobalShortcutsManagerV1ErrorAlreadyTaken))
}

func TestAGlobalShortcutIsHeldOnlyByAHeldTrigger(t *testing.T) {
	h := StartWith(t, withBinds(t,
		bindSpec{chord: "mouse:274", action: "global voice:ptt"},
		bindSpec{chord: "mouse_down", action: "global voice:ptt"},
	), small)
	c := testclient.Connect(t, h.Socket())
	mgr, err := chgs.NewHyprlandGlobalShortcutsManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(mgr)
	l := &shortcutLog{}
	if _, err := mgr.RegisterShortcut(l, "ptt", "voice", "Push to talk", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	pointer := func(f func(p *headless.Pointer) error) {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return errors.Join(f(b.Pointer()), b.Pointer().Frame())
		})
	}
	pointer(func(p *headless.Pointer) error { return p.Button(btnMiddle, true) })
	c.DispatchUntil("the button press", func() bool { return len(l.events) >= 1 })
	c.Sync()
	if len(l.events) != 1 {
		t.Fatalf("a held button bind sent %q", l.events)
	}
	pointer(func(p *headless.Pointer) error { return p.Button(btnMiddle, false) })
	c.DispatchUntil("the button release", func() bool { return len(l.events) == 2 })
	pointer(func(p *headless.Pointer) error { return p.Axis(backend.Vertical, backend.Wheel, 15, 120) })
	c.DispatchUntil("a scroll tap", func() bool { return len(l.events) == 4 })
	if want := []string{"pressed", "released", "pressed", "released"}; !slices.Equal(l.events, want) {
		t.Fatalf("events %q, want %q", l.events, want)
	}
}
