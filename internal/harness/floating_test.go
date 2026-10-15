package harness

import (
	"errors"
	"image"
	"regexp"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cwlr "github.com/stubbedev/tilde/internal/protocol/client/wlrlayershell"
	cxdg "github.com/stubbedev/tilde/internal/protocol/client/xdgshell"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wm"
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

func TestWindowRulesFloatAndSize(t *testing.T) {
	steam := regexp.MustCompile(`^tilde\.testclient$`)
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Rules = []wm.Rule{
			{AppID: steam, Mode: wm.RuleFloat},
			{AppID: steam, Title: regexp.MustCompile(`^Steam$`), Mode: wm.RuleTile},
			{AppID: steam, Title: regexp.MustCompile(`^Friends List$`), Size: image.Pt(40, 90)},
		}
	}, small)
	c := testclient.Connect(t, h.Socket())
	main := c.Window("Steam", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the main window to map", main.Mapped)
	if main.Logical != small.Size || !slices.Contains(main.States, cxdg.XdgToplevelStateTiledLeft) {
		t.Fatalf("the main window is %v %v; the tile rule wins", main.Logical, main.States)
	}
	friends := c.Window("Friends List", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the friends list to map", friends.Mapped)
	if friends.Logical != image.Pt(40, 90) || slices.Contains(friends.States, cxdg.XdgToplevelStateTiledLeft) {
		t.Fatalf("the friends list is %v %v, want floating at 40x90", friends.Logical, friends.States)
	}
	h.Eventually("the friends list centred", showing(70, 20, blueColor))
}

func TestAFloatingWindowOverAnotherOutputTakesTheClicksItCovers(t *testing.T) {
	h := StartWith(t, withBinds(t, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"}), small, small)
	c := testclient.Connect(t, h.Socket())
	float := c.Window("float", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the floating window to map", float.Mapped)
	h.dispatch("togglefloating")
	c.DispatchUntil("a floating window", func() bool { return float.Logical == image.Pt(80, 50) })
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		return errors.Join(p.MoveTo(o, 0.5, 0.5), p.Frame(), p.Button(btnLeft, true), p.Frame(),
			p.MoveTo(o, 150.0/160, 0.5), p.Frame(), p.Button(btnLeft, false), p.Frame())
	})
	h.key(keySuper, false)
	h.Eventually("the floating window over the second output", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		for _, w := range comp.Snapshot().Windows {
			if w.Title == "float" {
				return w.Rect.X+w.Rect.Width > 170
			}
		}
		return false
	})
	h.dispatch("workspace 2")
	tiled := c.Window("tiled", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the tiled window to map", tiled.Mapped)
	h.Eventually("the tiled window on the second output", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		for _, w := range comp.Snapshot().Windows {
			if w.Title == "tiled" {
				return w.Rect.X >= 160
			}
		}
		return false
	})
	h.Eventually("the floating window drawn over the tiled one", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return pixel(b.Outputs()[1].Snapshot(), 5, 50) == redColor
	})
	h.click(1, 5, 50, btnLeft)
	h.Eventually("the click went to the window drawn there", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		for _, w := range comp.Snapshot().Windows {
			if w.Focused {
				return w.Title == "float"
			}
		}
		return false
	})
}

func TestAWindowAcrossOutputsPrefersTheHighestScale(t *testing.T) {
	h := StartWith(t, withBinds(t, bindSpec{chord: "SUPER + mouse:272", action: "movewindow"}), small, small)
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		return c.SetOutputScale(b.Outputs()[1], 240)
	})
	c := testclient.Connect(t, h.Socket())
	w := c.ScaledWindow("across", testclient.FractionalScale, testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("togglefloating")
	c.DispatchUntil("a floating window", func() bool { return w.Logical == image.Pt(80, 50) })
	h.key(keySuper, true)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		o, p := b.Outputs()[0], b.Pointer()
		return errors.Join(p.MoveTo(o, 0.5, 0.5), p.Frame(), p.Button(btnLeft, true), p.Frame(),
			p.MoveTo(o, 150.0/160, 0.5), p.Frame(), p.Button(btnLeft, false), p.Frame())
	})
	h.key(keySuper, false)
	c.DispatchUntil("the window on both outputs at the higher scale", func() bool { return w.Entered == 2 && w.Preferred120 == 240 })
	for range 5 {
		h.dispatch("resizeactive 1 0")
		if err := c.Display.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		if w.Preferred120 != 240 {
			t.Fatalf("the preferred scale went back to %d after a configure", w.Preferred120)
		}
	}
}

func TestAFloatingWindowIsNeverSmallerThanItAsks(t *testing.T) {
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Rules = []wm.Rule{{AppID: regexp.MustCompile(`^tilde\.testclient$`), Mode: wm.RuleFloat, Size: image.Pt(40, 30)}}
	}, desktop)
	c := testclient.Connect(t, h.Socket())
	w := c.WindowWith("roomy", testclient.Fill(0, 0, 0xff), func(w *testclient.Window) error { return w.Toplevel.SetMinSize(400, 300) })
	c.DispatchUntil("the window to map", w.Mapped)
	if w.Logical != image.Pt(400, 300) {
		t.Fatalf("a window asking for at least 400x300 floats at %v", w.Logical)
	}
}
