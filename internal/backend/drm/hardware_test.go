package drm

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/backend"
	kdrm "github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/edid"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
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

func cursorImage(t *testing.T, o *Output) {
	t.Helper()
	cp, err := o.CursorPlane()
	if err != nil {
		t.Fatal(err)
	}
	f, err := cp.BeginCursor()
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rectangle{Max: cp.CursorSize()}})
	if err != nil {
		t.Fatal(err)
	}
	p.Clear(render.Color{})
	p.FillRect(image.Rect(0, 0, 8, 8), render.Color{R: 0xff, A: 0xff})
	if err := errors.Join(p.End(), cp.SetCursor(image.Pt(1, 1)), cp.MoveCursor(image.Pt(50, 50))); err != nil {
		t.Fatal(err)
	}
	if len(o.cursor.buffers) != cursorBuffers || o.cursor.pending.fb == 0 || o.cursor.refused != nil {
		t.Fatalf("%s cursor: %d buffers, pending %+v, refused %v", o.Name(), len(o.cursor.buffers), o.cursor.pending, o.cursor.refused)
	}
	pix, pitch := o.cursor.pending.dumb.Pix, int(o.cursor.pending.dumb.Pitch)
	if [4]byte(pix[:4]) != [4]byte{0, 0, 0xff, 0xff} || [4]byte(pix[7*pitch+28:7*pitch+32]) != [4]byte{0, 0, 0xff, 0xff} || [4]byte(pix[8*pitch:8*pitch+4]) != [4]byte{} {
		t.Fatalf("%s cursor buffer: %v, %v, %v", o.Name(), pix[:4], pix[7*pitch+28:7*pitch+32], pix[8*pitch:8*pitch+4])
	}
	t.Logf("%s cursor plane %d, %v", o.Name(), o.cursor.plane.id, cp.CursorSize())
}

func gammaRamps(t *testing.T, o *Output) {
	t.Helper()
	size := o.GammaSize()
	if size == 0 {
		t.Logf("%s has no GAMMA_LUT", o.Name())
		return
	}
	ramps := make([]uint16, 3*size)
	for i := range ramps {
		ramps[i] = uint16(i % size * 0xffff / (size - 1))
	}
	if err := o.SetGamma(ramps); err != nil {
		t.Fatal(err)
	}
	if o.gamma.next == 0 || !o.gamma.dirty {
		t.Fatalf("%s gamma: %+v", o.Name(), o.gamma)
	}
	if err := o.SetGamma(nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s gamma LUT of %d entries", o.Name(), size)
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
	b, err := New(l, dev, Options{Paths: gpu.PathsFromEnv(os.Getenv), Vendors: vendors, Report: func(err error) { t.Error(err) }})
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
		if len(o.buffers) != buffersPerOut || o.buffers[0].fb == 0 || !o.State().Enabled() || !o.State().Mode.Preferred {
			t.Errorf("%s: %d buffers, state %+v", o.Name(), len(o.buffers), o.State())
		}
		if o.Description() == o.Name() {
			t.Errorf("%s has no EDID description", o.Name())
		}
		t.Logf("%s (%s) on CRTC %d and plane %d with modifiers %#x, %+v", o.Name(), o.Description(), o.crtc.id, o.primary.id, o.primary.modifiers, o.State().Mode)
		cursorImage(t, o)
		gammaRamps(t, o)
	}
	t.Logf("atomic async page flips: %v", b.async)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}
