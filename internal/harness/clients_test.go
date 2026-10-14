package harness

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/render/soft"
)

const clientTimeout = 20 * time.Second

var desktop = backend.Mode{Size: image.Pt(1280, 800), RefreshMHz: 60000, Preferred: true}

func mappedToplevels(n int) func(*headless.Backend, *compositor.Compositor) bool {
	return func(_ *headless.Backend, c *compositor.Compositor) bool {
		mapped := 0
		for _, t := range c.Toplevels() {
			if t.Mapped() {
				mapped++
			}
		}
		return mapped >= n
	}
}

func mappedPopups(n int) func(*headless.Backend, *compositor.Compositor) bool {
	return func(_ *headless.Backend, c *compositor.Compositor) bool {
		mapped := 0
		for _, t := range c.Toplevels() {
			for _, p := range t.Popups() {
				if p.Mapped() {
					mapped++
				}
			}
		}
		return mapped == n
	}
}

func notBackground(b *soft.Buffer, r image.Rectangle) int {
	bg := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if pixel(b, x, y) != bg {
				n++
			}
		}
	}
	return n
}

func (h *Harness) click(output int, x, y float64, button uint32) {
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o := b.Outputs()[output]
		size := o.State().Mode.Size
		p := b.Pointer()
		return errors.Join(p.MoveTo(o, x/float64(size.X), y/float64(size.Y)), p.Frame(), p.Button(button, true), p.Frame(), p.Button(button, false), p.Frame())
	})
}

func (h *Harness) tap(keys ...uint32) {
	for _, k := range keys {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return errors.Join(b.Keyboard().Key(k, true), b.Keyboard().Key(k, false))
		})
	}
}

const (
	btnLeft  = 272
	btnRight = 273
	keyH     = 35
	keyI     = 23
	keyE     = 18
	keyC     = 46
	keyO     = 24
	keyEnter = 28
	keyEsc   = 1
	keyRight = 106
	keyDown  = 108
)

func TestWestonFlowerMaps(t *testing.T) {
	h := Start(t, desktop)
	flower := h.Run("weston-flower")
	h.Eventually("weston-flower to map", mappedToplevels(1))
	h.expectActive(flower)
	h.Eventually("the flower on the output", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return notBackground(b.Outputs()[0].Snapshot(), image.Rect(0, 0, 1280, 800)) > 1000
	})
}

func TestFootMapsAndTakesInput(t *testing.T) {
	h := Start(t, desktop)
	typed := filepath.Join(t.TempDir(), "typed")
	foot := h.Run("foot", "--override=main.font=monospace:size=12", "sh", "-c", `printf ready; read -r line; printf %s "$line" > "$0"`, typed)
	h.Eventually("foot to map", mappedToplevels(1))
	h.Eventually("foot to draw", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return notBackground(b.Outputs()[0].Snapshot(), image.Rect(0, 0, 1280, 800)) > 1000
	})
	h.Eventually("foot to take the keyboard", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return c.Seat().Focus() != nil
	})
	h.expectActive(foot)
	h.tap(keyE, keyC, keyH, keyO, keyEnter)
	foot.Wait(t, clientTimeout)
	got, err := os.ReadFile(typed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "echo" {
		t.Fatalf("foot read %q from the keyboard, want echo", got)
	}
}

func TestGelmHelloMenu(t *testing.T) {
	t.Setenv("GOELM_DEBUG", "demo")
	h := Start(t, desktop, desktop)
	hello := h.Run("gelm-hello")
	hello.Expect(t, "demo: mapped", clientTimeout)
	h.Eventually("gelm-hello to map", mappedToplevels(1))
	h.click(0, 1024, 560, btnRight)
	hello.Expect(t, "demo: menu open at (1024,560)", clientTimeout)
	h.Eventually("the menu mapped with the keyboard", func(b *headless.Backend, c *compositor.Compositor) bool {
		if !mappedPopups(1)(b, c) {
			return false
		}
		top := c.Toplevels()[0]
		p := top.Popups()[0]
		return c.Seat().Focus() == p.Surface() && image.Pt(1024, 560).In(p.Box().Add(top.Geometry().Min))
	})
	h.click(1, 640, 400, btnLeft)
	h.Eventually("the menu dismissed", mappedPopups(0))
	h.Eventually("the keyboard back on the window", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return c.Seat().Focus() == c.Toplevels()[0].Surface()
	})
	h.expectActive(hello)
}

func TestGelmPopoverAndNestedMenu(t *testing.T) {
	t.Setenv("GOELM_DEBUG", "demo")
	h := Start(t, desktop)
	popover := h.Run("gelm-popover")
	center := func(name string) (float64, float64) {
		var x, y float64
		if _, err := fmt.Sscanf(popover.Expect(t, "control "+name+" center", clientTimeout), "control "+name+" center (%g,%g)", &x, &y); err != nil {
			t.Fatal(err)
		}
		return x, y
	}
	mx, my := center("menu")
	h.click(0, mx, my, btnLeft)
	popover.Expect(t, "demo: menu depth 1", clientTimeout)
	h.tap(keyDown, keyRight)
	popover.Expect(t, "demo: menu depth 2", clientTimeout)
	h.tap(keyDown, keyEnter)
	popover.Expect(t, "demo: menu leaf a", clientTimeout)
	popover.Expect(t, "demo: menu depth 0", clientTimeout)
	ox, oy := center("open")
	h.click(0, ox, oy, btnLeft)
	popover.Expect(t, "demo: popover open", clientTimeout)
	h.tap(keyH, keyI)
	popover.Expect(t, "demo: popover text hi", clientTimeout)
	popover.Expect(t, "demo: popover grew", clientTimeout)
	h.click(0, 1200, 700, btnLeft)
	popover.Expect(t, "demo: popover closed", clientTimeout)
}
