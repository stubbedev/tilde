package harness

import (
	"errors"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wire"
)

func unresponsive(id int, want bool) func(*headless.Backend, *compositor.Compositor) bool {
	return func(_ *headless.Backend, c *compositor.Compositor) bool {
		for _, w := range c.Snapshot().Windows {
			if int(w.ID) == id {
				return w.Unresponsive == want
			}
		}
		return false
	}
}

func TestHungClientsAreMarkedUnresponsive(t *testing.T) {
	h := Start(t, small)
	lines := h.watch()
	hung := testclient.Connect(t, h.Socket())
	w := hung.Window("hung", quadrants)
	hung.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window focused", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := c.Snapshot().Windows
		return len(ws) == 1 && ws[0].Focused
	})
	time.Sleep(shell.PingTimeout)
	h.Eventually("the hung window marked unresponsive", unresponsive(1, true))
	expectEvent(t, lines, `{"event":"unresponsive","window":1,"unresponsive":true}`)

	alive := testclient.Connect(t, h.Socket())
	other := alive.Window("alive", quadrants)
	alive.DispatchUntil("another client's window to map", other.Mapped)
	h.Eventually("the other window responsive", unresponsive(2, false))

	if err := hung.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the recovered window marked responsive", unresponsive(1, false))
	expectEvent(t, lines, `{"event":"unresponsive","window":1,"unresponsive":false}`)
}

func survives(t *testing.T, h *Harness) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	w := c.Window("survivor", quadrants)
	c.DispatchUntil("a window mapped beside the hostile client", w.Mapped)
}

func TestClientsLeavingABacklogDoNotStopTheCompositor(t *testing.T) {
	h := Start(t, small)
	gone := testclient.Connect(t, h.Socket())
	w := gone.Window("leaving", quadrants)
	gone.DispatchUntil("the window to map", w.Mapped)
	var done bool
	for range 20000 {
		if _, err := w.Surface.Frame(frameDone{&done}); err != nil || errors.Is(gone.Display.Flush(), wire.ErrWouldBlock) {
			break
		}
	}
	if err := errors.Join(w.Surface.Attach(gone.Buffer(w.Logical, 1, quadrants), 0, 0), w.Surface.Commit(), gone.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	if err := gone.Display.Close(); err != nil {
		t.Fatal(err)
	}
	survives(t, h)
	h.Eventually("the leaving client's window gone", mappedToplevels(1))
}
