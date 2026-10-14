package harness

import (
	"errors"
	"slices"
	"testing"

	chgs "github.com/stubbedev/tilde/internal/protocol/client/hyprlandglobalshortcuts"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wire"
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
	c.Bind("hyprland_global_shortcuts_manager_v1", mgr)
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

	_, err = mgr.RegisterShortcut(&shortcutLog{}, "ptt", "voice", "again", "")
	var pe *wire.ProtocolError
	if err = errors.Join(err, c.Display.Roundtrip()); !errors.As(err, &pe) || pe.Code != uint32(chgs.HyprlandGlobalShortcutsManagerV1ErrorAlreadyTaken) {
		t.Fatalf("registering voice:ptt twice: %v", err)
	}
}
