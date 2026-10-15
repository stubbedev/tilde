package shell

import (
	"fmt"
	"image"

	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
)

type (
	Anchor     = sxdg.XdgPositionerAnchor
	Gravity    = sxdg.XdgPositionerGravity
	Adjustment = sxdg.XdgPositionerConstraintAdjustment
)

type Positioner struct {
	Size       image.Point
	AnchorRect image.Rectangle
	Anchor     Anchor
	Gravity    Gravity
	Adjustment Adjustment
	Offset     image.Point
	Reactive   bool
}

func direction(edge uint32) (int, int) {
	switch sxdg.XdgPositionerAnchor(edge) {
	case sxdg.XdgPositionerAnchorTop:
		return 0, -1
	case sxdg.XdgPositionerAnchorBottom:
		return 0, 1
	case sxdg.XdgPositionerAnchorLeft:
		return -1, 0
	case sxdg.XdgPositionerAnchorRight:
		return 1, 0
	case sxdg.XdgPositionerAnchorTopLeft:
		return -1, -1
	case sxdg.XdgPositionerAnchorBottomLeft:
		return -1, 1
	case sxdg.XdgPositionerAnchorTopRight:
		return 1, -1
	case sxdg.XdgPositionerAnchorBottomRight:
		return 1, 1
	}
	return 0, 0
}

type axis struct {
	anchorLo, anchorHi  int
	anchor, gravity     int
	size, offset        int
	lo, hi              int
	flip, slide, resize bool
}

func (a axis) start(anchor, gravity int) int {
	at := a.anchorLo + (a.anchorHi-a.anchorLo)/2
	switch anchor {
	case -1:
		at = a.anchorLo
	case 1:
		at = a.anchorHi
	}
	switch gravity {
	case -1:
		return at - a.size
	case 1:
		return at
	}
	return at - a.size/2
}

func (a axis) clip(start int) (int, int) {
	size := a.size
	if start < a.lo {
		size -= a.lo - start
		start = a.lo
	}
	if start+size > a.hi {
		size = a.hi - start
	}
	return start, size
}

func (a axis) place() (int, int) {
	start := a.start(a.anchor, a.gravity)
	if a.flip && (a.gravity < 0 && start+a.offset < a.lo || a.gravity > 0 && start+a.offset+a.size > a.hi) {
		flipped := a.start(-a.anchor, -a.gravity)
		_, visible := a.clip(start)
		if _, flippedVisible := a.clip(flipped); flippedVisible > visible {
			start = flipped
		}
	}
	start += a.offset
	if a.slide {
		if start+a.size > a.hi {
			start = a.hi - a.size
		}
		if start < a.lo {
			start = a.lo
		}
	}
	if a.resize {
		if clipped, size := a.clip(start); size > 0 {
			return clipped, size
		}
	}
	return start, a.size
}

func (p Positioner) Place(bounds image.Rectangle) image.Rectangle {
	ax, ay := direction(uint32(p.Anchor))
	gx, gy := direction(uint32(p.Gravity))
	adjust := p.Adjustment
	if bounds.Empty() {
		adjust = sxdg.XdgPositionerConstraintAdjustmentNone
	}
	x, w := axis{
		anchorLo: p.AnchorRect.Min.X, anchorHi: p.AnchorRect.Max.X, anchor: ax, gravity: gx,
		size: p.Size.X, offset: p.Offset.X, lo: bounds.Min.X, hi: bounds.Max.X,
		flip:   adjust.Has(sxdg.XdgPositionerConstraintAdjustmentFlipX),
		slide:  adjust.Has(sxdg.XdgPositionerConstraintAdjustmentSlideX),
		resize: adjust.Has(sxdg.XdgPositionerConstraintAdjustmentResizeX),
	}.place()
	y, h := axis{
		anchorLo: p.AnchorRect.Min.Y, anchorHi: p.AnchorRect.Max.Y, anchor: ay, gravity: gy,
		size: p.Size.Y, offset: p.Offset.Y, lo: bounds.Min.Y, hi: bounds.Max.Y,
		flip:   adjust.Has(sxdg.XdgPositionerConstraintAdjustmentFlipY),
		slide:  adjust.Has(sxdg.XdgPositionerConstraintAdjustmentSlideY),
		resize: adjust.Has(sxdg.XdgPositionerConstraintAdjustmentResizeY),
	}.place()
	return image.Rect(x, y, x+w, y+h)
}

type positioner struct {
	rules     Positioner
	sizeSet   bool
	anchorSet bool
}

func (b *wmBase) CreatePositioner(*sxdg.XdgWmBase, *sxdg.XdgPositioner) (sxdg.XdgPositionerHandler, error) {
	return &positioner{}, nil
}

func (b *wmBase) rules(res *sxdg.XdgPositioner) (Positioner, error) {
	p, ok := res.Handler().(*positioner)
	if !ok {
		return Positioner{}, fmt.Errorf("shell: xdg_positioner@%d has handler %T", res.Base().ID(), res.Handler())
	}
	if !p.sizeSet || !p.anchorSet {
		return Positioner{}, b.resource.ProtocolError(sxdg.XdgWmBaseErrorInvalidPositioner, "xdg_positioner@%d has no size or anchor rectangle", res.Base().ID())
	}
	return p.rules, nil
}

func (*positioner) Destroy(*sxdg.XdgPositioner) error {
	return nil
}

func invalidInput(obj *sxdg.XdgPositioner, format string, args ...any) error {
	return obj.ProtocolError(sxdg.XdgPositionerErrorInvalidInput, format, args...)
}

func (p *positioner) SetSize(obj *sxdg.XdgPositioner, w, h int32) error {
	if w <= 0 || h <= 0 {
		return invalidInput(obj, "size %dx%d is not positive", w, h)
	}
	p.rules.Size, p.sizeSet = image.Pt(int(w), int(h)), true
	return nil
}

func (p *positioner) SetAnchorRect(obj *sxdg.XdgPositioner, x, y, w, h int32) error {
	if w < 0 || h < 0 {
		return invalidInput(obj, "anchor rectangle %dx%d is negative", w, h)
	}
	p.rules.AnchorRect, p.anchorSet = image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h)), true
	return nil
}

func (p *positioner) SetAnchor(obj *sxdg.XdgPositioner, a sxdg.XdgPositionerAnchor) error {
	if !a.Valid(obj.Base().Version()) {
		return invalidInput(obj, "anchor %d", a)
	}
	p.rules.Anchor = a
	return nil
}

func (p *positioner) SetGravity(obj *sxdg.XdgPositioner, g sxdg.XdgPositionerGravity) error {
	if !g.Valid(obj.Base().Version()) {
		return invalidInput(obj, "gravity %d", g)
	}
	p.rules.Gravity = g
	return nil
}

func (p *positioner) SetConstraintAdjustment(_ *sxdg.XdgPositioner, a sxdg.XdgPositionerConstraintAdjustment) error {
	p.rules.Adjustment = a
	return nil
}

func (p *positioner) SetOffset(_ *sxdg.XdgPositioner, x, y int32) error {
	p.rules.Offset = image.Pt(int(x), int(y))
	return nil
}

func (p *positioner) SetReactive(*sxdg.XdgPositioner) error {
	p.rules.Reactive = true
	return nil
}

func (*positioner) SetParentSize(*sxdg.XdgPositioner, int32, int32) error {
	return nil
}

func (*positioner) SetParentConfigure(*sxdg.XdgPositioner, uint32) error {
	return nil
}
