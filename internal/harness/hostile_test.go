package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wire"
)

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
