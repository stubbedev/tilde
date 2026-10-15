package harness

import (
	"testing"

	"github.com/stubbedev/tilde/internal/binds"
	cksi "github.com/stubbedev/tilde/internal/protocol/client/keyboardshortcutsinhibit"
)

func (l *constraintLog) Active(*cksi.ZwpKeyboardShortcutsInhibitorV1) error {
	l.events = append(l.events, "active")
	return nil
}

func (l *constraintLog) Inactive(*cksi.ZwpKeyboardShortcutsInhibitorV1) error {
	l.events = append(l.events, "inactive")
	return nil
}

func TestShortcutInhibitorPassesKeysButKeepsLockedBinds(t *testing.T) {
	h := StartWith(t, withBinds(t,
		bindSpec{chord: "A", action: "workspace 2", flags: binds.Flags{Locked: true}},
		bindSpec{chord: "B", action: "workspace 3"},
	), small)
	r := inputOn(t, h)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	mgr, err := cksi.NewZwpKeyboardShortcutsInhibitManagerV1(r.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.c.Bind(mgr)
	l := &constraintLog{}
	if _, err := mgr.InhibitShortcuts(l, r.w.Surface, r.seat); err != nil {
		t.Fatal(err)
	}
	r.await(l, "active")
	h.tap(48)
	r.expect("key 48 1", "key 48 0")
	h.tap(30)
	h.Eventually("the locked bind ran", showingWorkspace(2))
	r.await(l, "active", "inactive")
}
