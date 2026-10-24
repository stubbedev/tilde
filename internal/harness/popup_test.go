package harness

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cxdg "github.com/stubbedev/lys-wm/internal/protocol/client/xdgshell"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

var gray = [3]byte{0x80, 0x80, 0x80}

func menuAt(anchor image.Point, size image.Point) testclient.Placement {
	return testclient.Placement{
		Size:       size,
		Anchor:     image.Rectangle{Min: anchor, Max: anchor.Add(image.Pt(1, 1))},
		Edge:       cxdg.XdgPositionerAnchorBottomRight,
		Gravity:    cxdg.XdgPositionerGravityBottomRight,
		Adjustment: cxdg.XdgPositionerConstraintAdjustmentFlipX | cxdg.XdgPositionerConstraintAdjustmentFlipY | cxdg.XdgPositionerConstraintAdjustmentSlideX | cxdg.XdgPositionerConstraintAdjustmentSlideY,
	}
}

func (r *inputRig) click() {
	r.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		p := b.Pointer()
		return errors.Join(p.Button(272, true), p.Frame(), p.Button(272, false), p.Frame())
	})
}

func TestPopupMenusGrabAndDismiss(t *testing.T) {
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	seat, in := c.Seat()
	w := c.Window("win", quadrants)
	in.Name(w.Surface, "win")
	c.DispatchUntil("the window to map", w.Mapped)
	r := &inputRig{t: t, h: h, c: c, in: in, w: w}
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.moveOn(0, 70, 40)
	r.expect("motion 70,40", "frame")
	r.click()
	r.expect("button 272 1", "frame", "button 272 0", "frame")

	menu := c.Popup(w, menuAt(image.Pt(70, 40), image.Pt(40, 30)), &testclient.Grab{Seat: seat, Serial: in.Press}, testclient.Fill(0x80, 0x80, 0x80))
	in.Name(menu.Surface, "menu")
	c.DispatchUntil("the menu to map", menu.Mapped)
	if menu.Placed != image.Rect(71, 41, 111, 71) {
		t.Fatalf("menu placed at %v, want below and right of the anchor", menu.Placed)
	}
	r.expect("kb_leave win", "kb_enter menu")
	h.Eventually("the menu on screen", showing(100, 60, gray))

	sub := c.Popup(menu, menuAt(image.Pt(39, 0), image.Pt(60, 30)), &testclient.Grab{Seat: seat, Serial: in.Press}, testclient.Fill(0x40, 0x40, 0x40))
	in.Name(sub.Surface, "sub")
	c.DispatchUntil("the submenu to map", sub.Mapped)
	if sub.Placed != image.Rect(-21, 1, 39, 31) {
		t.Fatalf("submenu placed at %v, want flipped to the left of its anchor", sub.Placed)
	}
	r.expect("kb_leave menu", "kb_enter sub")
	h.Eventually("the submenu above its menu", showing(60, 50, [3]byte{0x40, 0x40, 0x40}))

	r.moveOn(0, 60, 50)
	r.expect("leave win", "enter sub 10,8", "frame")
	r.moveOn(0, 10, 10)
	r.expect("leave sub", "enter win 10,10", "frame")
	r.moveOn(1, 10, 10)
	r.expect("leave win", "frame")
	r.click()
	c.DispatchUntil("both popups dismissed", func() bool { return menu.Dismissed && sub.Dismissed })
	r.expect("kb_leave sub", "kb_enter win")
	h.Eventually("the popups gone", showing(100, 60, [3]byte{0xff, 0xff, 0}))
	if err := errors.Join(sub.Destroy(), menu.Destroy()); err != nil {
		t.Fatal(err)
	}
	r.moveOn(0, 20, 20)
	r.click()
	r.expect("enter win 20,20", "frame", "button 272 1", "frame", "button 272 0", "frame")
}

func TestGrabsFromClientsWithoutInputAreRefused(t *testing.T) {
	h := Start(t, small)
	background := testclient.Connect(t, h.Socket())
	seat, _ := background.Seat()
	hidden := background.Window("hidden", quadrants)
	background.DispatchUntil("the background window to map", hidden.Mapped)
	front := testclient.Connect(t, h.Socket())
	frontSeat, _ := front.Seat()
	w := front.Window("front", quadrants)
	front.DispatchUntil("the front window to map", w.Mapped)
	refused := background.Popup(hidden, menuAt(image.Pt(10, 10), image.Pt(20, 20)), &testclient.Grab{Seat: seat, Serial: 12345}, testclient.Fill(0x80, 0x80, 0x80))
	background.DispatchUntil("the background popup dismissed", func() bool { return refused.Dismissed })
	focused := front.Popup(w, menuAt(image.Pt(10, 10), image.Pt(20, 20)), &testclient.Grab{Seat: frontSeat, Serial: 0}, testclient.Fill(0x80, 0x80, 0x80))
	front.DispatchUntil("the focused client's popup to map", focused.Mapped)
	if focused.Dismissed {
		t.Fatal("the client with keyboard and pointer focus was refused a grab")
	}
}

func TestPopupsAreClickableWithoutAGrab(t *testing.T) {
	r := startInput(t)
	r.expect("kb_enter win", "enter win 80,50", "frame")
	tip := r.c.Popup(r.w, menuAt(image.Pt(150, 90), image.Pt(30, 20)), nil, testclient.Fill(0x80, 0x80, 0x80))
	r.in.Name(tip.Surface, "tip")
	r.c.DispatchUntil("the tooltip to map", tip.Mapped)
	if tip.Placed != image.Rect(120, 70, 150, 90) {
		t.Fatalf("tooltip placed at %v, want flipped above and left of its anchor", tip.Placed)
	}
	r.h.Eventually("the tooltip on screen", showing(130, 80, gray))
	r.moveTo(130, 80)
	r.expect("leave win", "enter tip 10,10", "frame")
	r.inject(func(b *headless.Backend, _ *headless.Output) error {
		return errors.Join(b.Pointer().Button(272, true), b.Pointer().Frame())
	})
	r.expect("button 272 1", "frame")
}

func TestAnAckForADismissedPopupIsNotAnError(t *testing.T) {
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	seat, in := c.Seat()
	w := c.Window("win", quadrants)
	in.Name(w.Surface, "win")
	c.DispatchUntil("the window to map", w.Mapped)
	r := &inputRig{t: t, h: h, c: c, in: in, w: w}
	r.expect("kb_enter win", "enter win 80,50", "frame")
	r.moveOn(0, 70, 40)
	r.expect("motion 70,40", "frame")
	r.click()
	r.expect("button 272 1", "frame", "button 272 0", "frame")
	menu := c.Popup(w, menuAt(image.Pt(70, 40), image.Pt(40, 30)), &testclient.Grab{Seat: seat, Serial: in.Press}, testclient.Fill(0x80, 0x80, 0x80))
	c.DispatchUntil("the menu to map", menu.Mapped)
	pos := c.Positioner(menuAt(image.Pt(10, 10), image.Pt(40, 30)))
	if err := errors.Join(menu.Popup.Reposition(pos, 1), pos.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
	h.click(1, 10, 10, btnLeft)
	c.DispatchUntil("the menu dismissed", func() bool { return menu.Dismissed })
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatalf("acking a configure sent before the dismissal: %v", err)
	}
}
