package harness

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (h *Harness) dispatch(text string) {
	h.t.Helper()
	a, err := wm.ParseAction(text)
	if err != nil {
		h.t.Fatal(err)
	}
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error { return c.Dispatch(a) })
}

func TestDispatchedActions(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	red := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", red.Mapped)
	blue := c.Window("blue", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("blue to map", blue.Mapped)
	h.Eventually("blue on the right", showing(150, 10, [3]byte{0, 0, 0xff}))

	h.dispatch("move left")
	h.Eventually("blue moved to the left", showing(10, 10, [3]byte{0, 0, 0xff}))
	h.dispatch("movetoworkspace 2")
	c.DispatchUntil("red alone on workspace 1", func() bool { return red.Logical == small.Size })
	h.Eventually("red over the whole output", showing(10, 10, [3]byte{0xff, 0, 0}))
	h.dispatch("workspace 2")
	h.Eventually("blue alone on workspace 2", showing(10, 10, [3]byte{0, 0, 0xff}))
	h.dispatch("kill")
	c.DispatchUntil("blue asked to close", func() bool { return blue.Closed })
	if red.Closed {
		t.Fatal("kill closed a window that was not focused")
	}

	out := filepath.Join(t.TempDir(), "display")
	h.dispatch(`exec printf %s "$WAYLAND_DISPLAY" > ` + out)
	deadline := time.Now().Add(clientTimeout)
	for {
		got, err := os.ReadFile(out)
		if err == nil && string(got) == filepath.Join(h.RuntimeDir, h.display.Name()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("exec wrote %q, %v; want the compositor's socket path", got, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
