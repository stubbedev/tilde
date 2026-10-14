package harness

import (
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cwlr "github.com/stubbedev/tilde/internal/protocol/client/wlrlayershell"
	cxdg "github.com/stubbedev/tilde/internal/protocol/client/xdgshell"
	"github.com/stubbedev/tilde/internal/testclient"
)

var (
	redColor  = [3]byte{0xff, 0, 0}
	blueColor = [3]byte{0, 0, 0xff}
)

func TestFloatingFullscreenAndTransients(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	red := c.Window("red", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("red to map", red.Mapped)
	blue := c.Window("blue", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("blue to map", blue.Mapped)

	h.dispatch("togglefloating")
	c.DispatchUntil("blue floats at half size", func() bool { return blue.Logical == image.Pt(80, 50) })
	if slices.Contains(blue.States, cxdg.XdgToplevelStateTiledLeft) {
		t.Fatalf("a floating window is still configured tiled: %v", blue.States)
	}
	c.DispatchUntil("red takes the whole output", func() bool { return red.Logical == small.Size })
	h.Eventually("blue centred above red", showing(80, 40, blueColor))
	h.Eventually("red around it", showing(10, 10, redColor))

	h.dispatch("fullscreen")
	c.DispatchUntil("blue fullscreen", func() bool {
		return blue.Logical == small.Size && slices.Contains(blue.States, cxdg.XdgToplevelStateFullscreen)
	})
	h.Eventually("blue covering the output", showing(10, 10, blueColor))
	if err := errors.Join(blue.Toplevel.UnsetFullscreen(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("blue floating again", func() bool { return blue.Logical == image.Pt(80, 50) })
	if err := errors.Join(red.Toplevel.SetFullscreen(nil), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("red fullscreen on request", func() bool { return slices.Contains(red.States, cxdg.XdgToplevelStateFullscreen) })
	if err := errors.Join(red.Toplevel.UnsetFullscreen(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}

	dialog := c.Dialog(red, "dialog", testclient.Fill(0, 0xff, 0))
	c.DispatchUntil("the dialog to map", dialog.Mapped)
	if dialog.Logical != image.Pt(64, 64) {
		t.Fatalf("the dialog was sized %v; transients pick their own size", dialog.Logical)
	}
	h.Eventually("the dialog centred over its parent", showing(90, 60, [3]byte{0, 0xff, 0}))
}

func TestFullscreenCoversTheTopLayerButNotOverlay(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	bar := c.Layer(topBar(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone), testclient.Fill(barColor[2], barColor[1], barColor[0]))
	c.DispatchUntil("the bar to map", bar.Mapped)
	note := c.Layer(testclient.LayerSpec{
		Layer:  cwlr.ZwlrLayerShellV1LayerOverlay,
		Anchor: cwlr.ZwlrLayerSurfaceV1AnchorBottom | cwlr.ZwlrLayerSurfaceV1AnchorRight,
		Size:   image.Pt(30, 20),
	}, testclient.Fill(noteColor[2], noteColor[1], noteColor[0]))
	c.DispatchUntil("the note to map", note.Mapped)
	w := c.Window("win", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("fullscreen")
	c.DispatchUntil("the window fullscreen", func() bool { return w.Logical == small.Size })
	h.Eventually("the window over the bar", showing(5, 5, blueColor))
	h.Eventually("the note above the window", showing(150, 95, noteColor))
}

func TestMouseDragsMoveAndResize(t *testing.T) {
	h := StartWith(t, withBinds(t,
		bindSpec{chord: "SUPER + mouse:272", action: "movewindow"},
		bindSpec{chord: "SUPER + mouse:273", action: "resizewindow"},
	), small)
	c := testclient.Connect(t, h.Socket())
	left := c.Window("left", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("left to map", left.Mapped)
	right := c.Window("right", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("right to map", right.Mapped)

	drag := func(button uint32, from, to image.Point) {
		t.Helper()
		h.key(keySuper, true)
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			o := b.Outputs()[0]
			p := b.Pointer()
			return errors.Join(
				p.MoveTo(o, float64(from.X)/160, float64(from.Y)/100), p.Frame(),
				p.Button(button, true), p.Frame(),
				p.MoveTo(o, float64(to.X)/160, float64(to.Y)/100), p.Frame(),
				p.Button(button, false), p.Frame(),
			)
		})
		h.key(keySuper, false)
	}

	drag(btnRight, image.Pt(40, 50), image.Pt(70, 50))
	c.DispatchUntil("the left tile grown by the drag", func() bool { return left.Logical == image.Pt(110, 100) })
	c.DispatchUntil("the right tile shrunk", func() bool { return right.Logical == image.Pt(50, 100) })

	drag(btnLeft, image.Pt(140, 50), image.Pt(10, 50))
	h.Eventually("right dropped onto the left half", showing(5, 50, blueColor))

	h.dispatch("togglefloating")
	c.DispatchUntil("a floating window", func() bool { return right.Logical == image.Pt(80, 50) })
	drag(btnLeft, image.Pt(80, 50), image.Pt(100, 60))
	h.Eventually("the floating window moved by the drag", showing(135, 80, blueColor))
}
