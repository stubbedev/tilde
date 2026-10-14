package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	csb "github.com/stubbedev/tilde/internal/protocol/client/systembell"
	"github.com/stubbedev/tilde/internal/testclient"
)

func TestTheSystemBellMarksAnUnfocusedWindowUrgent(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	first := c.Window("first", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the first window to map", first.Mapped)
	second := c.Window("second", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the second window to map", second.Mapped)
	bell, err := csb.NewXdgSystemBellV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("xdg_system_bell_v1", bell)
	if err := errors.Join(bell.Ring(nil), bell.Ring(second.Surface), bell.Ring(first.Surface), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("only the unfocused window urgent", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		ws := windowsOf(comp)
		return len(ws) == 2 && ws[0].Title == "first" && ws[0].Urgent && !ws[0].Focused && !ws[1].Urgent && ws[1].Focused
	})
}
