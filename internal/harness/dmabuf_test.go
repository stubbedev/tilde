package harness

import (
	"image"
	"testing"

	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/testclient"
)

func TestDMABUFClientsOnTheGLESRenderer(t *testing.T) {
	h := StartGLES(t, func(*compositor.Config) {}, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("dmabuf", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the shm window through GLES", showing(120, 80, [3]byte{0xff, 0, 0}))
	child := c.Surface()
	sub, err := c.Subcompositor.GetSubsurface(child, w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	blue := c.DMABUF(image.Pt(16, 16), testclient.Fill(0xff, 0, 0))
	for _, err := range []error{sub.SetPosition(10, 10), child.Attach(blue.WL, 0, 0), child.DamageBuffer(0, 0, 16, 16), child.Commit(), w.Surface.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	c.Sync()
	h.Eventually("the dmabuf subsurface", showing(18, 18, [3]byte{0, 0, 0xff}))
	green := c.DMABUF(image.Pt(16, 16), testclient.Fill(0, 0xff, 0))
	for _, err := range []error{child.Attach(green.WL, 0, 0), child.DamageBuffer(0, 0, 16, 16), child.Commit(), w.Surface.Commit()} {
		if err != nil {
			t.Fatal(err)
		}
	}
	c.Sync()
	h.Eventually("the second dmabuf", showing(18, 18, [3]byte{0, 0xff, 0}))
	c.DispatchUntil("the first dmabuf released", func() bool { return blue.Released })
	if green.Released {
		t.Fatal("the dmabuf on screen was released")
	}
	if err := green.WL.Destroy(); err != nil {
		t.Fatal(err)
	}
	c.Sync()
	h.Eventually("the window under the destroyed dmabuf", showing(18, 18, [3]byte{0xff, 0, 0}))
}
