package harness

import (
	"errors"
	"math"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cam "github.com/stubbedev/tilde/internal/protocol/client/alphamodifier"
	"github.com/stubbedev/tilde/internal/testclient"
)

var (
	redPixel        = [3]byte{0xff, 0, 0}
	backgroundPixel = [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
)

func TestAnAlphaModifierFadesASurface(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("fading", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window opaque", showing(30, 30, redPixel))
	m, err := cam.NewWpAlphaModifierV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	fade, err := m.GetSurface(w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	set := func(factor uint32) {
		t.Helper()
		if err := errors.Join(fade.SetMultiplier(factor), w.Surface.Commit(), c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
	}

	set(0)
	h.Eventually("the window fully faded", showing(30, 30, backgroundPixel))
	set(math.MaxUint32 / 2)
	h.Eventually("the window half faded", func(b *headless.Backend, _ *compositor.Compositor) bool {
		p := pixel(b.Outputs()[0].Snapshot(), 30, 30)
		return p[0] > backgroundPixel[0]+0x40 && p[0] < 0xff-0x40
	})
	if err := errors.Join(fade.Destroy(), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the window opaque once the modifier is gone", showing(30, 30, redPixel))

	if _, err := m.GetSurface(w.Surface); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetSurface(w.Surface); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(cam.WpAlphaModifierV1ErrorAlreadyConstructed))
}

func TestAnAlphaMultiplierAfterTheSurfaceIsGoneIsAnError(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m, err := cam.NewWpAlphaModifierV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	s := c.Surface()
	fade, err := m.GetSurface(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(s.Destroy(), fade.SetMultiplier(0)); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(cam.WpAlphaModifierSurfaceV1ErrorNoSurface))
}
