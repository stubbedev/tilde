package shell

import (
	"image"
	"slices"
	"testing"

	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cxdg "github.com/stubbedev/tilde/internal/protocol/client/xdgshell"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
)

type popupEvents struct {
	log   []string
	boxes []image.Rectangle
}

func (p *popupEvents) Configure(_ *cxdg.XdgPopup, x, y, w, h int32) error {
	p.boxes = append(p.boxes, image.Rect(int(x), int(y), int(x+w), int(y+h)))
	p.log = append(p.log, "configure")
	return nil
}

func (p *popupEvents) PopupDone(*cxdg.XdgPopup) error {
	p.log = append(p.log, "done")
	return nil
}

func (p *popupEvents) Repositioned(_ *cxdg.XdgPopup, token uint32) error {
	p.log = append(p.log, "repositioned")
	return nil
}

type popup struct {
	surf  *window
	popup *cxdg.XdgPopup
	ev    *popupEvents
}

func (c *client) positioner(size image.Point, anchor image.Rectangle) *cxdg.XdgPositioner {
	c.t.Helper()
	pos, err := c.wm.CreatePositioner()
	must(c.t, err)
	must(c.t, pos.SetSize(int32(size.X), int32(size.Y)), pos.SetAnchorRect(int32(anchor.Min.X), int32(anchor.Min.Y), int32(anchor.Dx()), int32(anchor.Dy())),
		pos.SetAnchor(cxdg.XdgPositionerAnchorBottomLeft), pos.SetGravity(cxdg.XdgPositionerGravityBottomRight))
	return pos
}

func (c *client) popup(parent *cxdg.XdgSurface, pos *cxdg.XdgPositioner) *popup {
	c.t.Helper()
	p := &popup{surf: &window{xsE: &xdgSurfaceEvents{}}, ev: &popupEvents{}}
	var err error
	p.surf.surf, err = c.comp.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	must(c.t, err)
	p.surf.xs, err = c.wm.GetXdgSurface(p.surf.xsE, p.surf.surf)
	must(c.t, err)
	p.popup, err = p.surf.xs.GetPopup(p.ev, parent, pos)
	must(c.t, err)
	return p
}

func (c *client) mapped(w *window, size image.Point) {
	c.t.Helper()
	must(c.t, w.surf.Commit())
	c.roundtrip()
	must(c.t, w.xs.AckConfigure(w.lastSerial()), w.surf.Attach(c.buffer(size.X, size.Y), 0, 0), w.surf.Commit())
	c.roundtrip()
}

func (e *env) shellGrab() int {
	var n int
	e.on(func() { n = len(e.shell.Grab()) })
	return n
}

func TestPopupIsPlacedAndMapped(t *testing.T) {
	e := setup(t)
	e.rec.constraint = image.Rect(0, 0, 100, 100)
	c := e.connect(6)
	w := c.window()
	c.mapped(w, image.Pt(100, 100))
	pos := c.positioner(image.Pt(40, 30), image.Rect(70, 80, 80, 90))
	must(t, pos.SetConstraintAdjustment(cxdg.XdgPositionerConstraintAdjustmentSlideX|cxdg.XdgPositionerConstraintAdjustmentFlipY))
	p := c.popup(w.xs, pos)
	must(t, pos.Destroy())
	c.mapped(p.surf, image.Pt(40, 30))
	if want := []image.Rectangle{image.Rect(60, 50, 100, 80)}; !slices.Equal(p.ev.boxes, want) {
		t.Fatalf("popup configured at %v, want %v: slid left and flipped above the anchor", p.ev.boxes, want)
	}
	e.on(func() {
		if len(e.rec.popups) != 1 || e.rec.popups[0].Box() != image.Rect(60, 50, 100, 80) || e.rec.popups[0].Root() != PopupRoot(e.rec.toplevels[0]) {
			t.Errorf("mapped popups %v", e.rec.popups)
		}
	})
	moved := c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1))
	must(t, p.popup.Reposition(moved, 7))
	c.roundtrip()
	if want := []string{"configure", "repositioned", "configure"}; !slices.Equal(p.ev.log, want) {
		t.Fatalf("popup events %v, want %v", p.ev.log, want)
	}
	must(t, p.surf.xs.AckConfigure(p.surf.lastSerial()), p.surf.surf.Commit())
	c.roundtrip()
	e.on(func() {
		if got := e.rec.popups[0].Box(); got != image.Rect(0, 1, 10, 11) {
			t.Errorf("after reposition the popup is at %v", got)
		}
	})
	must(t, w.top.Destroy())
	c.roundtrip()
	if p.ev.log[len(p.ev.log)-1] != "done" {
		t.Fatalf("popup events %v: destroying the parent must dismiss the popup", p.ev.log)
	}
}

func TestNestedPopupsAreOffsetByTheirParents(t *testing.T) {
	e := setup(t)
	e.rec.constraint = image.Rect(-1000, -1000, 1000, 1000)
	c := e.connect(6)
	w := c.window()
	c.mapped(w, image.Pt(100, 100))
	outer := c.popup(w.xs, c.positioner(image.Pt(40, 40), image.Rect(10, 10, 10, 10)))
	c.mapped(outer.surf, image.Pt(40, 40))
	inner := c.popup(outer.surf.xs, c.positioner(image.Pt(20, 20), image.Rect(5, 5, 5, 5)))
	c.mapped(inner.surf, image.Pt(20, 20))
	e.on(func() {
		if len(e.rec.popups) != 2 || e.rec.popups[1].Box() != image.Rect(5, 5, 25, 25) || e.rec.popups[1].parentOffset() != image.Pt(10, 10) {
			t.Errorf("inner popup at %v offset %v", e.rec.popups[1].Box(), e.rec.popups[1].parentOffset())
		}
	})
}

func TestPopupGrabs(t *testing.T) {
	e := setup(t)
	e.rec.constraint = image.Rect(0, 0, 100, 100)
	e.rec.press = 42
	c := e.connect(6)
	w := c.window()
	c.mapped(w, image.Pt(100, 100))
	refused := c.popup(w.xs, c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1)))
	must(t, refused.popup.Grab(c.seat, 41))
	c.roundtrip()
	if !slices.Equal(refused.ev.log, []string{"done"}) || e.shellGrab() != 0 {
		t.Fatalf("a grab with a stale serial: %v, %d grabs; want it dismissed at once", refused.ev.log, e.shellGrab())
	}
	must(t, refused.popup.Destroy(), refused.surf.xs.Destroy())
	menu := c.popup(w.xs, c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1)))
	must(t, menu.popup.Grab(c.seat, 42))
	c.mapped(menu.surf, image.Pt(10, 10))
	sub := c.popup(menu.surf.xs, c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1)))
	must(t, sub.popup.Grab(c.seat, 42))
	c.mapped(sub.surf, image.Pt(10, 10))
	if e.shellGrab() != 2 {
		t.Fatalf("%d grabbing popups, want the menu and its submenu", e.shellGrab())
	}
	var errs []error
	e.on(func() { errs = append(errs, e.shell.DismissGrab()) })
	must(t, errs...)
	c.roundtrip()
	if menu.ev.log[len(menu.ev.log)-1] != "done" || sub.ev.log[len(sub.ev.log)-1] != "done" || e.shellGrab() != 0 {
		t.Fatalf("after dismissal: menu %v, submenu %v, %d grabs", menu.ev.log, sub.ev.log, e.shellGrab())
	}
	log := e.log()
	if i, j := slices.Index(log, "popup unmapped"), slices.Index(log[slices.Index(log, "popup unmapped")+1:], "popup unmapped"); i < 0 || j < 0 {
		t.Fatalf("log %v: both popups must unmap", log)
	}
}

func TestPopupProtocolErrors(t *testing.T) {
	cases := []struct {
		name string
		do   func(c *client, w *window)
		code uint32
	}{
		{"incomplete positioner", func(c *client, w *window) {
			pos, err := c.wm.CreatePositioner()
			must(c.t, err, pos.SetSize(10, 10))
			c.popup(w.xs, pos)
		}, uint32(sxdg.XdgWmBaseErrorInvalidPositioner)},
		{"zero size", func(c *client, _ *window) {
			pos, err := c.wm.CreatePositioner()
			must(c.t, err, pos.SetSize(0, 10))
		}, uint32(sxdg.XdgPositionerErrorInvalidInput)},
		{"negative anchor rectangle", func(c *client, _ *window) {
			pos, err := c.wm.CreatePositioner()
			must(c.t, err, pos.SetAnchorRect(0, 0, -1, 1))
		}, uint32(sxdg.XdgPositionerErrorInvalidInput)},
		{"bad anchor", func(c *client, _ *window) {
			pos, err := c.wm.CreatePositioner()
			must(c.t, err, pos.SetAnchor(9))
		}, uint32(sxdg.XdgPositionerErrorInvalidInput)},
		{"no parent", func(c *client, _ *window) {
			p := c.popup(nil, c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1)))
			must(c.t, p.surf.surf.Commit())
		}, uint32(sxdg.XdgWmBaseErrorInvalidPopupParent)},
		{"grab after the initial commit", func(c *client, w *window) {
			p := c.popup(w.xs, c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1)))
			must(c.t, p.surf.surf.Commit(), p.popup.Grab(c.seat, 42))
		}, uint32(sxdg.XdgPopupErrorInvalidGrab)},
		{"grab on a popup without a grab", func(c *client, w *window) {
			p := c.popup(w.xs, c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1)))
			c.mapped(p.surf, image.Pt(10, 10))
			q := c.popup(p.surf.xs, c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1)))
			must(c.t, q.popup.Grab(c.seat, 42))
		}, uint32(sxdg.XdgPopupErrorInvalidGrab)},
		{"destroying a grabbing popup below the top", func(c *client, w *window) {
			p := c.popup(w.xs, c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1)))
			must(c.t, p.popup.Grab(c.seat, 42))
			c.mapped(p.surf, image.Pt(10, 10))
			q := c.popup(p.surf.xs, c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1)))
			must(c.t, q.popup.Grab(c.seat, 42))
			c.mapped(q.surf, image.Pt(10, 10))
			must(c.t, p.popup.Destroy())
		}, uint32(sxdg.XdgWmBaseErrorNotTheTopmostPopup)},
		{"xdg_surface destroyed before the popup", func(c *client, w *window) {
			p := c.popup(w.xs, c.positioner(image.Pt(10, 10), image.Rect(0, 0, 1, 1)))
			must(c.t, p.surf.xs.Destroy())
		}, uint32(sxdg.XdgSurfaceErrorDefunctRoleObject)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			e.rec.constraint = image.Rect(0, 0, 100, 100)
			e.rec.press = 42
			c := e.connect(6)
			w := c.window()
			c.mapped(w, image.Pt(100, 100))
			tc.do(c, w)
			c.protocolError(tc.code)
		})
	}
}

func TestReactivePopupsFollowTheirConstraint(t *testing.T) {
	e := setup(t)
	e.rec.constraint = image.Rect(0, 0, 100, 100)
	c := e.connect(6)
	w := c.window()
	c.mapped(w, image.Pt(100, 100))
	pos := c.positioner(image.Pt(20, 20), image.Rect(70, 70, 80, 80))
	must(t, pos.SetConstraintAdjustment(cxdg.XdgPositionerConstraintAdjustmentSlideX|cxdg.XdgPositionerConstraintAdjustmentSlideY))
	fixed := c.popup(w.xs, pos)
	c.mapped(fixed.surf, image.Pt(20, 20))
	must(t, pos.SetReactive())
	reactive := c.popup(w.xs, pos)
	c.mapped(reactive.surf, image.Pt(20, 20))
	e.rec.constraint = image.Rect(0, 0, 90, 90)
	var errs []error
	e.on(func() {
		for _, p := range e.rec.popups {
			errs = append(errs, p.Reconstrain())
		}
	})
	must(t, errs...)
	c.roundtrip()
	if len(fixed.ev.boxes) != 1 {
		t.Fatalf("a non-reactive popup was configured again: %v", fixed.ev.boxes)
	}
	if want := []image.Rectangle{image.Rect(70, 80, 90, 100), image.Rect(70, 70, 90, 90)}; !slices.Equal(reactive.ev.boxes, want) {
		t.Fatalf("reactive popup configured at %v, want %v", reactive.ev.boxes, want)
	}
}
