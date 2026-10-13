package harness

import (
	"image"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/golden"
)

var small = backend.Mode{Size: image.Pt(160, 100), RefreshMHz: 60000, Preferred: true}

func presented(i int) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		outputs := b.Outputs()
		return i < len(outputs) && outputs[i].Snapshot().Pix[3] == 0xff
	}
}

func TestDesktopIsDrawnOnEveryOutput(t *testing.T) {
	h := Start(t, small)
	h.Eventually("the first frame", presented(0))
	golden.Assert(t, "desktop", h.Snapshot(0))
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddOutput(backend.Mode{Size: image.Pt(40, 30), RefreshMHz: 60000})
		return err
	})
	h.Eventually("a frame on the output added at runtime", presented(1))
	if snap := h.Snapshot(1); snap.W != 40 || snap.H != 30 {
		t.Fatalf("second output is %dx%d", snap.W, snap.H)
	}
}

func TestWaylandInfoConnects(t *testing.T) {
	h := Start(t, small)
	out := h.Run("wayland-info").Wait(t, 10*time.Second)
	if trace := h.Trace(); !strings.Contains(trace, "wl_display@1.get_registry(new id wl_registry@2)") {
		t.Fatalf("wayland-info never reached the compositor:\n%s", trace)
	}
	for _, global := range []string{"'wl_compositor'", "'wl_subcompositor'", "'wl_shm'", "'wl_seat'"} {
		if !strings.Contains(out, global) {
			t.Errorf("wayland-info does not list %s:\n%s", global, out)
		}
	}
}
