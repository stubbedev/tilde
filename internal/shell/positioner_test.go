package shell

import (
	"image"
	"testing"

	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
)

func TestPlacement(t *testing.T) {
	const (
		slide  = sxdg.XdgPositionerConstraintAdjustmentSlideX | sxdg.XdgPositionerConstraintAdjustmentSlideY
		flip   = sxdg.XdgPositionerConstraintAdjustmentFlipX | sxdg.XdgPositionerConstraintAdjustmentFlipY
		resize = sxdg.XdgPositionerConstraintAdjustmentResizeX | sxdg.XdgPositionerConstraintAdjustmentResizeY
	)
	bounds := image.Rect(0, 0, 200, 100)
	menu := Positioner{
		Size:       image.Pt(50, 40),
		AnchorRect: image.Rect(10, 10, 30, 20),
		Anchor:     sxdg.XdgPositionerAnchorBottomLeft,
		Gravity:    sxdg.XdgPositionerGravityBottomRight,
	}
	at := func(r image.Rectangle) Positioner {
		p := menu
		p.AnchorRect = r
		return p
	}
	with := func(p Positioner, f func(*Positioner)) Positioner {
		f(&p)
		return p
	}
	cases := []struct {
		name string
		p    Positioner
		want image.Rectangle
	}{
		{"below the anchor", menu, image.Rect(10, 20, 60, 60)},
		{"centred on a point", with(menu, func(p *Positioner) {
			p.Anchor, p.Gravity, p.AnchorRect = sxdg.XdgPositionerAnchorNone, sxdg.XdgPositionerGravityNone, image.Rect(100, 50, 100, 50)
		}), image.Rect(75, 30, 125, 70)},
		{"with an offset", with(menu, func(p *Positioner) { p.Offset = image.Pt(-3, 4) }), image.Rect(7, 24, 57, 64)},
		{"constrained without adjustment", at(image.Rect(170, 80, 190, 90)), image.Rect(170, 90, 220, 130)},
		{"flipped up and left", with(at(image.Rect(170, 70, 190, 80)), func(p *Positioner) {
			p.Adjustment, p.Anchor = flip, sxdg.XdgPositionerAnchorBottomRight
		}), image.Rect(120, 30, 170, 70)},
		{"a flip that shows less is refused", with(at(image.Rect(170, 10, 190, 20)), func(p *Positioner) {
			p.Adjustment, p.Size = flip, image.Pt(50, 90)
		}), image.Rect(140, 20, 190, 110)},
		{"slid back inside", with(at(image.Rect(170, 80, 190, 90)), func(p *Positioner) { p.Adjustment = slide }), image.Rect(150, 60, 200, 100)},
		{"slid to the top left when too large", with(menu, func(p *Positioner) {
			p.Adjustment, p.Size = slide, image.Pt(300, 150)
		}), image.Rect(0, 0, 300, 150)},
		{"resized to fit", with(at(image.Rect(170, 80, 190, 90)), func(p *Positioner) { p.Adjustment = resize }), image.Rect(170, 90, 200, 100)},
		{"resize never empties", with(at(image.Rect(200, 100, 210, 110)), func(p *Positioner) { p.Adjustment = resize }), image.Rect(200, 110, 250, 150)},
		{"no bounds means no adjustment", with(at(image.Rect(170, 80, 190, 90)), func(p *Positioner) { p.Adjustment = slide | flip | resize }), image.Rect(170, 90, 220, 130)},
	}
	for _, c := range cases {
		b := bounds
		if c.name == "no bounds means no adjustment" {
			b = image.Rectangle{}
		}
		if got := c.p.Place(b); got != c.want {
			t.Errorf("%s: placed at %v, want %v", c.name, got, c.want)
		}
	}
}
