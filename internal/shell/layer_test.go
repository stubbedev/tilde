package shell

import (
	"image"
	"slices"
	"testing"

	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cwlr "github.com/stubbedev/tilde/internal/protocol/client/wlrlayershell"
	swlr "github.com/stubbedev/tilde/internal/protocol/server/wlrlayershell"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
)

type layerEvents struct {
	serials []uint32
	sizes   []image.Point
	closed  bool
}

func (l *layerEvents) Configure(_ *cwlr.ZwlrLayerSurfaceV1, serial, w, h uint32) error {
	l.serials = append(l.serials, serial)
	l.sizes = append(l.sizes, image.Pt(int(w), int(h)))
	return nil
}

func (l *layerEvents) Closed(*cwlr.ZwlrLayerSurfaceV1) error {
	l.closed = true
	return nil
}

type layer struct {
	surf *cw.WlSurface
	ls   *cwlr.ZwlrLayerSurfaceV1
	ev   *layerEvents
}

const bar = cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorLeft | cwlr.ZwlrLayerSurfaceV1AnchorRight

func (c *client) layer(anchor cwlr.ZwlrLayerSurfaceV1Anchor, size image.Point) *layer {
	c.t.Helper()
	l := &layer{ev: &layerEvents{}}
	var err error
	l.surf, err = c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	must(c.t, err)
	l.ls, err = c.lsh.GetLayerSurface(l.ev, l.surf, nil, cwlr.ZwlrLayerShellV1LayerTop, "bar")
	must(c.t, err)
	must(c.t, l.ls.SetAnchor(anchor), l.ls.SetSize(uint32(size.X), uint32(size.Y)))
	return l
}

func (l *layer) last() uint32 {
	return l.ev.serials[len(l.ev.serials)-1]
}

func layerSetup(t *testing.T, configure ...func(*recorder)) (*env, *client) {
	e := setup(t, append([]func(*recorder){func(r *recorder) {
		r.layerSize = func(l *LayerSurface) image.Point {
			size := l.Current().Size
			if size.X == 0 {
				size.X = 200
			}
			if size.Y == 0 {
				size.Y = 100
			}
			return size
		}
	}}, configure...)...)
	return e, e.connect(6)
}

func TestLayerSurfaceConfigureMapAndUnmap(t *testing.T) {
	e, c := layerSetup(t)
	l := c.layer(bar, image.Pt(0, 30))
	must(t, l.ls.SetExclusiveZone(30), l.ls.SetMargin(1, 2, 3, 4), l.ls.SetKeyboardInteractivity(cwlr.ZwlrLayerSurfaceV1KeyboardInteractivityOnDemand), l.surf.Commit())
	c.roundtrip()
	if !slices.Equal(l.ev.sizes, []image.Point{image.Pt(200, 30)}) {
		t.Fatalf("configures %v, want the compositor's width with the requested height", l.ev.sizes)
	}
	must(t, l.ls.AckConfigure(l.last()), l.surf.Attach(c.buffer(200, 30), 0, 0), l.surf.Commit())
	c.roundtrip()
	must(t, l.surf.Commit())
	c.roundtrip()
	if !slices.Equal(l.ev.sizes, []image.Point{image.Pt(200, 30)}) {
		t.Fatalf("configures %v: an unchanged size is not configured again", l.ev.sizes)
	}
	e.on(func() {
		got := e.rec.layers[0].Current()
		want := LayerState{
			Layer: swlr.ZwlrLayerShellV1LayerTop, Size: image.Pt(0, 30), Anchor: Edges(bar), ExclusiveZone: 30,
			Margin: Margin{Top: 1, Right: 2, Bottom: 3, Left: 4}, Interactivity: swlr.ZwlrLayerSurfaceV1KeyboardInteractivityOnDemand,
		}
		if got != want {
			t.Errorf("state %+v, want %+v", got, want)
		}
		if e.rec.layers[0].Namespace() != "bar" {
			t.Errorf("namespace %q", e.rec.layers[0].Namespace())
		}
	})
	must(t, l.surf.Attach(nil, 0, 0), l.surf.Commit(), l.surf.Commit())
	c.roundtrip()
	want := []string{"layer committed false", "layer committed true", "layer committed true", "layer unmapped", "layer committed false"}
	if got := e.log(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if len(l.ev.sizes) != 2 {
		t.Fatalf("configures %v: a surface committed again after unmapping is configured afresh", l.ev.sizes)
	}
	must(t, l.ls.Destroy())
	c.roundtrip()
	if got := e.log(); got[len(got)-1] != "layer destroyed" {
		t.Fatalf("events %v, want the destroy reported", got)
	}
}

func TestLayerSurfaceCloseAndPopups(t *testing.T) {
	e, c := layerSetup(t, func(r *recorder) { r.constraint = image.Rect(0, 0, 200, 100) })
	l := c.layer(bar, image.Pt(0, 30))
	must(t, l.surf.Commit())
	c.roundtrip()
	must(t, l.ls.AckConfigure(l.last()), l.surf.Attach(c.buffer(200, 30), 0, 0), l.surf.Commit())
	c.roundtrip()
	p := c.popup(nil, c.positioner(image.Pt(50, 40), image.Rect(10, 20, 30, 30)))
	must(t, l.ls.GetPopup(p.popup))
	c.mapped(p.surf, image.Pt(50, 40))
	if !slices.Equal(p.ev.boxes, []image.Rectangle{image.Rect(10, 30, 60, 70)}) {
		t.Fatalf("popup of a layer surface configured at %v", p.ev.boxes)
	}
	e.on(func() {
		if len(e.rec.popups) != 1 || e.rec.popups[0].Root() != PopupRoot(e.rec.layers[0]) {
			t.Errorf("the popup's root is not the layer surface")
		}
	})
	var errs []error
	e.on(func() { errs = append(errs, e.rec.layers[0].Close()) })
	must(t, errs...)
	c.roundtrip()
	if !l.ev.closed || p.ev.log[len(p.ev.log)-1] != "done" {
		t.Fatalf("closed %t, popup events %v: closing must dismiss the popups too", l.ev.closed, p.ev.log)
	}
	must(t, l.ls.SetSize(1, 1), l.surf.Commit())
	c.roundtrip()
	if len(l.ev.sizes) != 1 {
		t.Fatalf("a closed surface was configured again: %v", l.ev.sizes)
	}
}

func TestLayerShellProtocolErrors(t *testing.T) {
	cases := []struct {
		name string
		do   func(c *client)
		code uint32
	}{
		{"invalid layer", func(c *client) {
			s, err := c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
			must(c.t, err)
			_, err = c.lsh.GetLayerSurface(&layerEvents{}, s, nil, 4, "x")
			must(c.t, err)
		}, uint32(swlr.ZwlrLayerShellV1ErrorInvalidLayer)},
		{"surface with a buffer", func(c *client) {
			s, err := c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
			must(c.t, err, s.Attach(c.buffer(4, 4), 0, 0), s.Commit())
			_, err = c.lsh.GetLayerSurface(&layerEvents{}, s, nil, 0, "x")
			must(c.t, err)
		}, uint32(swlr.ZwlrLayerShellV1ErrorAlreadyConstructed)},
		{"surface with another role", func(c *client) {
			w := c.window()
			_, err := c.lsh.GetLayerSurface(&layerEvents{}, w.surf, nil, 0, "x")
			must(c.t, err)
		}, uint32(swlr.ZwlrLayerShellV1ErrorRole)},
		{"zero width without both side anchors", func(c *client) {
			l := c.layer(cwlr.ZwlrLayerSurfaceV1AnchorTop, image.Pt(0, 30))
			must(c.t, l.surf.Commit())
		}, uint32(swlr.ZwlrLayerSurfaceV1ErrorInvalidSize)},
		{"buffer before the configure", func(c *client) {
			l := c.layer(bar, image.Pt(0, 30))
			must(c.t, l.surf.Commit(), l.surf.Attach(c.buffer(4, 4), 0, 0), l.surf.Commit())
		}, uint32(swlr.ZwlrLayerSurfaceV1ErrorInvalidSurfaceState)},
		{"unknown serial", func(c *client) {
			must(c.t, c.layer(bar, image.Pt(0, 30)).ls.AckConfigure(77))
		}, uint32(swlr.ZwlrLayerSurfaceV1ErrorInvalidSurfaceState)},
		{"bad anchor", func(c *client) {
			must(c.t, c.layer(bar, image.Pt(0, 30)).ls.SetAnchor(16))
		}, uint32(swlr.ZwlrLayerSurfaceV1ErrorInvalidAnchor)},
		{"bad keyboard interactivity", func(c *client) {
			must(c.t, c.layer(bar, image.Pt(0, 30)).ls.SetKeyboardInteractivity(3))
		}, uint32(swlr.ZwlrLayerSurfaceV1ErrorInvalidKeyboardInteractivity)},
		{"exclusive edge of two edges", func(c *client) {
			must(c.t, c.layer(bar, image.Pt(0, 30)).ls.SetExclusiveEdge(cwlr.ZwlrLayerSurfaceV1AnchorTop|cwlr.ZwlrLayerSurfaceV1AnchorLeft))
		}, uint32(swlr.ZwlrLayerSurfaceV1ErrorInvalidExclusiveEdge)},
		{"exclusive edge off the anchors", func(c *client) {
			l := c.layer(bar, image.Pt(0, 30))
			must(c.t, l.ls.SetExclusiveEdge(cwlr.ZwlrLayerSurfaceV1AnchorBottom), l.surf.Commit())
		}, uint32(swlr.ZwlrLayerSurfaceV1ErrorInvalidExclusiveEdge)},
		{"bad layer later", func(c *client) {
			must(c.t, c.layer(bar, image.Pt(0, 30)).ls.SetLayer(9))
		}, uint32(swlr.ZwlrLayerShellV1ErrorInvalidLayer)},
		{"popup that already has a parent", func(c *client) {
			l := c.layer(bar, image.Pt(0, 30))
			w := c.window()
			p := c.popup(w.xs, c.positioner(image.Pt(5, 5), image.Rect(0, 0, 1, 1)))
			must(c.t, l.ls.GetPopup(p.popup))
		}, uint32(sxdg.XdgWmBaseErrorInvalidPopupParent)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, c := layerSetup(t)
			tc.do(c)
			c.protocolError(tc.code)
		})
	}
}
