package compositor

import (
	"errors"
	"image"
	"math"
	"slices"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/binds"
	"github.com/stubbedev/tilde/internal/output"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/seat"
	"github.com/stubbedev/tilde/internal/surface"
)

type point struct {
	X, Y float64
}

func (p point) sub(o image.Point) point {
	return point{p.X - float64(o.X), p.Y - float64(o.Y)}
}

func wlCaps(c backend.Capability) sw.WlSeatCapability {
	var caps sw.WlSeatCapability
	if c.Has(backend.Keyboard) {
		caps |= sw.WlSeatCapabilityKeyboard
	}
	if c.Has(backend.Pointer) {
		caps |= sw.WlSeatCapabilityPointer
	}
	if c.Has(backend.Touch) {
		caps |= sw.WlSeatCapabilityTouch
	}
	return caps
}

func (c *Compositor) DeviceAdded(d backend.Device) error {
	return c.seat.AddDevice(wlCaps(d.Capabilities()))
}

func (c *Compositor) DeviceRemoved(d backend.Device) error {
	return c.seat.RemoveDevice(wlCaps(d.Capabilities()))
}

func (c *Compositor) Cursor() (float64, float64) {
	return c.cursor.X, c.cursor.Y
}

func (c *Compositor) layoutBounds() image.Rectangle {
	var b image.Rectangle
	for _, o := range c.outputs {
		b = b.Union(o.Logical())
	}
	return b
}

func (c *Compositor) onOutput(o backend.Output, x, y float64) (point, *output.Output, bool) {
	out := c.output(o)
	if out == nil {
		return point{}, nil, false
	}
	r := out.Logical()
	return point{float64(r.Min.X) + x*float64(r.Dx()), float64(r.Min.Y) + y*float64(r.Dy())}, out, true
}

func (c *Compositor) Input(_ backend.Device, e backend.Event) error {
	switch e := e.(type) {
	case backend.Key:
		if bound, err := c.bindKey(e.Code, e.Pressed); bound || err != nil {
			return err
		}
		return c.seat.Key(e.Code, e.Pressed, e.Time)
	case backend.PointerMotion:
		b := c.layoutBounds()
		c.cursor = point{
			X: math.Min(math.Max(c.cursor.X+e.DX, float64(b.Min.X)), float64(b.Max.X)-1),
			Y: math.Min(math.Max(c.cursor.Y+e.DY, float64(b.Min.Y)), float64(b.Max.Y)-1),
		}
		return c.pointerMoved(e.Time)
	case backend.PointerMotionAbsolute:
		p, _, ok := c.onOutput(e.Output, e.X, e.Y)
		if !ok {
			return nil
		}
		c.cursor = p
		return c.pointerMoved(e.Time)
	case backend.PointerButton:
		if c.drag != nil {
			if e.Pressed {
				return nil
			}
			return c.drag.Drop()
		}
		if i := slices.Index(c.consumed, e.Button); !e.Pressed && i >= 0 {
			c.consumed = slices.Delete(c.consumed, i, i+1)
			return nil
		}
		if e.Pressed && !c.seat.PointerGrabbed() && c.outsideGrab(c.seat.PointerFocus()) {
			c.consumed = append(c.consumed, e.Button)
			return c.shell.DismissGrab()
		}
		if b, ok := c.match(binds.Button{Code: e.Button}); e.Pressed && ok {
			c.consumed = append(c.consumed, e.Button)
			c.run(b.Action)
			return nil
		}
		if e.Pressed {
			if err := c.clickFocus(c.seat.PointerFocus()); err != nil {
				return err
			}
		}
		return c.seat.PointerButton(e.Time, e.Button, e.Pressed)
	case backend.PointerAxis:
		if e.Orientation == backend.Vertical && e.Value != 0 {
			dir := binds.ScrollDown
			if e.Value < 0 {
				dir = binds.ScrollUp
			}
			if b, ok := c.match(binds.Scroll{Dir: dir}); ok {
				c.run(b.Action)
				return nil
			}
		}
		a := seat.Axis{Orientation: sw.WlPointerAxisVerticalScroll, Source: sw.WlPointerAxisSource(e.Source), Value: e.Value, Value120: e.Value120}
		if e.Orientation == backend.Horizontal {
			a.Orientation = sw.WlPointerAxisHorizontalScroll
		}
		return c.seat.PointerAxis(e.Time, a)
	case backend.PointerFrame:
		return c.seat.PointerFrame()
	case backend.TouchDown:
		p, out, ok := c.onOutput(e.Output, e.X, e.Y)
		if !ok {
			return nil
		}
		s, local, _ := c.surfaceAt(p)
		if c.outsideGrab(s) {
			return c.shell.DismissGrab()
		}
		if s == nil {
			return nil
		}
		c.touches[e.ID] = out
		return c.seat.TouchDown(e.Time, e.ID, s, local.X, local.Y)
	case backend.TouchMotion:
		out, ok := c.touches[e.ID]
		s := c.seat.TouchSurface(e.ID)
		if !ok || s == nil {
			return nil
		}
		origin, found := c.originOf(s)
		if !found {
			return nil
		}
		r := out.Logical()
		local := point{float64(r.Min.X) + e.X*float64(r.Dx()), float64(r.Min.Y) + e.Y*float64(r.Dy())}.sub(origin)
		return c.seat.TouchMotion(e.Time, e.ID, local.X, local.Y)
	case backend.TouchUp:
		delete(c.touches, e.ID)
		return c.seat.TouchUp(e.Time, e.ID)
	case backend.TouchCancel:
		clear(c.touches)
		return c.seat.TouchCancel()
	case backend.TouchFrame:
		return c.seat.TouchFrame()
	}
	return nil
}

func (c *Compositor) pointerMoved(at time.Duration) error {
	if c.drag != nil {
		return errors.Join(c.dragMotion(at), c.scheduleAll())
	}
	if f := c.seat.PointerFocus(); f != nil && c.seat.PointerGrabbed() {
		if origin, ok := c.originOf(f); ok {
			local := c.cursor.sub(origin)
			return c.seat.PointerMotion(at, local.X, local.Y)
		}
	}
	s, local, w := c.pointerTarget()
	var err error
	if s != c.seat.PointerFocus() {
		err = c.seat.SetPointerFocus(s, local.X, local.Y)
	} else if s != nil {
		err = c.seat.PointerMotion(at, local.X, local.Y)
	}
	if w != nil && c.grabClient() == nil && (c.active != w || c.seat.Focus() != w.toplevel.Surface()) {
		err = errors.Join(err, c.focusWindow(w))
	}
	return errors.Join(err, c.scheduleAll())
}

func (c *Compositor) pointerTarget() (*surface.Surface, point, *window) {
	s, local, w := c.surfaceAt(c.cursor)
	if s != nil && c.outsideGrab(s) {
		return nil, point{}, nil
	}
	return s, local, w
}

func (c *Compositor) refreshPointer() error {
	if c.drag != nil {
		return c.dragMotion(backend.Now())
	}
	if c.seat.PointerGrabbed() {
		return nil
	}
	s, local, _ := c.pointerTarget()
	if s == c.seat.PointerFocus() {
		return nil
	}
	return errors.Join(c.seat.SetPointerFocus(s, local.X, local.Y), c.seat.PointerFrame())
}

func hitPopups(popups []placedPopup, p point) (*surface.Surface, point) {
	for _, pp := range slices.Backward(popups) {
		if s, local := hitTree(pp.popup.Surface(), pp.origin, p); s != nil {
			return s, local
		}
	}
	return nil, point{}
}

func hitLayers(layers []*layer, p point) (*surface.Surface, point) {
	for _, e := range slices.Backward(layers) {
		if s, local := hitTree(e.surface.Surface(), e.bounds().Min, p); s != nil {
			return s, local
		}
	}
	return nil, point{}
}

func (c *Compositor) outputAt(p point) *output.Output {
	for _, o := range c.outputs {
		if image.Pt(int(math.Floor(p.X)), int(math.Floor(p.Y))).In(o.Logical()) {
			return o
		}
	}
	return nil
}

func (c *Compositor) surfaceAt(p point) (*surface.Surface, point, *window) {
	for _, e := range slices.Backward(c.layers) {
		if s, local := hitPopups(c.layerPopups(e), p); s != nil {
			return s, local, nil
		}
	}
	o := c.outputAt(p)
	if s, local := hitLayers(c.layersAt(o, aboveWindows...), p); s != nil {
		return s, local, nil
	}
	for _, w := range slices.Backward(c.windows) {
		if s, local := hitPopups(c.popups(w), p); s != nil {
			return s, local, w
		}
	}
	for _, w := range slices.Backward(c.windows) {
		if !w.visible {
			continue
		}
		origin := c.placement(w).Min.Sub(w.toplevel.Geometry().Min)
		if s, local := hitTree(w.toplevel.Surface(), origin, p); s != nil {
			return s, local, w
		}
	}
	if s, local := hitLayers(c.layersAt(o, belowWindows...), p); s != nil {
		return s, local, nil
	}
	return nil, point{}, nil
}

func hitTree(s *surface.Surface, at image.Point, p point) (*surface.Surface, point) {
	for _, member := range slices.Backward(s.Stack()) {
		if member != s {
			if sub := member.Subsurface(); sub != nil {
				if hit, local := hitTree(member, at.Add(sub.Position()), p); hit != nil {
					return hit, local
				}
			}
			continue
		}
		local := p.sub(at)
		if s.Buffer() != nil && s.Input().Contains(image.Pt(int(math.Floor(local.X)), int(math.Floor(local.Y)))) {
			return s, local
		}
	}
	return nil, point{}
}

func (c *Compositor) originOf(target *surface.Surface) (image.Point, bool) {
	for _, w := range c.windows {
		if !w.visible {
			continue
		}
		if origin, ok := treeOrigin(w.toplevel.Surface(), c.placement(w).Min.Sub(w.toplevel.Geometry().Min), target); ok {
			return origin, true
		}
		for _, pp := range c.popups(w) {
			if origin, ok := treeOrigin(pp.popup.Surface(), pp.origin, target); ok {
				return origin, true
			}
		}
	}
	for _, e := range c.layers {
		if !e.shown() {
			continue
		}
		if origin, ok := treeOrigin(e.surface.Surface(), e.bounds().Min, target); ok {
			return origin, true
		}
		for _, pp := range c.layerPopups(e) {
			if origin, ok := treeOrigin(pp.popup.Surface(), pp.origin, target); ok {
				return origin, true
			}
		}
	}
	return image.Point{}, false
}

func treeOrigin(s *surface.Surface, at image.Point, target *surface.Surface) (image.Point, bool) {
	if s == target {
		return at, true
	}
	for _, member := range s.Stack() {
		if member != s && member.Subsurface() != nil {
			if origin, ok := treeOrigin(member, at.Add(member.Subsurface().Position()), target); ok {
				return origin, true
			}
		}
	}
	return image.Point{}, false
}
