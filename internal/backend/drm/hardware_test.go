package drm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/backend"
	kdrm "github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/edid"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/loop"
)

type added struct {
	backend.Handler
	outputs []backend.Output
}

func (a *added) OutputAdded(o backend.Output) error {
	a.outputs = append(a.outputs, o)
	return nil
}

func (a *added) OutputRemoved(backend.Output) error { return nil }

func connected(t *testing.T) int {
	t.Helper()
	statuses, err := filepath.Glob("/sys/class/drm/card0-*/status")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range statuses {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(b)) == "connected" {
			n++
		}
	}
	return n
}

func TestStartOnThisCardWithoutMaster(t *testing.T) {
	dev, err := kdrm.Open("/dev/dri/card0")
	if err != nil {
		t.Skipf("no DRM card: %v", err)
	}
	defer dev.Close()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	vendors, err := edid.LoadVendors(edid.PNPPath(os.Getenv))
	if err != nil {
		t.Skipf("no PNP ID database: %v", err)
	}
	b, err := New(l, dev, gpu.PathsFromEnv(os.Getenv), vendors)
	if err != nil {
		t.Skipf("no EGL vendor drives this card's GBM device here (unset __EGL_VENDOR_LIBRARY_DIRS on a GPU machine): %v", err)
	}
	h := &added{}
	if err := b.Start(h); err != nil {
		t.Fatal(err)
	}
	if len(h.outputs) != connected(t) {
		t.Fatalf("%d outputs for %d connected connectors", len(h.outputs), connected(t))
	}
	for _, o := range b.Outputs() {
		if len(o.buffers) != buffersPerOut || o.buffers[0].fb == 0 || !o.State().Enabled || !o.State().Mode.Preferred {
			t.Errorf("%s: %d buffers, state %+v", o.Name(), len(o.buffers), o.State())
		}
		if o.Description() == o.Name() {
			t.Errorf("%s has no EDID description", o.Name())
		}
		t.Logf("%s (%s) on CRTC %d and plane %d with modifiers %#x, %+v", o.Name(), o.Description(), o.crtc.id, o.primary.id, o.primary.modifiers, o.State().Mode)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}
