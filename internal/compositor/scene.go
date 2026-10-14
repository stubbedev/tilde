package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/output"
	spt "github.com/stubbedev/tilde/internal/protocol/server/presentationtime"
	swlr "github.com/stubbedev/tilde/internal/protocol/server/wlrlayershell"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wm"
)

func (c *Compositor) Frame(b backend.Output) error {
	o := c.output(b)
	if o == nil {
		return fmt.Errorf("compositor: frame on unknown output %s", b.Name())
	}
	if err := c.render(o); err != nil {
		return fmt.Errorf("compositor: frame on %s: %w", b.Name(), err)
	}
	return c.updateEntered(o)
}

func (c *Compositor) render(o *output.Output) error {
	r, target, err := o.Backend().BeginFrame()
	if err != nil {
		return err
	}
	bounds := image.Rectangle{Max: target.Size()}
	damage := render.Region{bounds}
	pass, err := r.Begin(target, damage)
	if err != nil {
		return err
	}
	pass.FillRect(bounds, Background)
	shown := c.shown[o][:0]
	for _, e := range c.layersAt(o, belowWindows...) {
		shown = c.drawTree(pass, o, e.surface.Surface(), e.bounds().Min, shown)
	}
	for _, layer := range []wm.PlacementLayer{wm.LayerTiled, wm.LayerFloating} {
		for _, w := range c.stack {
			if w.layer == layer {
				shown = c.drawWindow(pass, o, w, shown)
			}
		}
		if layer == wm.LayerTiled {
			if err := c.drawTabBars(pass, o); err != nil {
				return err
			}
		}
	}
	for _, w := range c.stack {
		if w.layer != wm.LayerFullscreen {
			shown = c.drawPopups(pass, o, c.popups(w), shown)
		}
	}
	for _, e := range c.layersAt(o, swlr.ZwlrLayerShellV1LayerTop) {
		shown = c.drawTree(pass, o, e.surface.Surface(), e.bounds().Min, shown)
	}
	for _, w := range c.stack {
		if w.layer == wm.LayerFullscreen {
			shown = c.drawWindow(pass, o, w, shown)
			shown = c.drawPopups(pass, o, c.popups(w), shown)
		}
	}
	if err := c.evictLabels(o); err != nil {
		return err
	}
	for _, e := range c.layersAt(o, swlr.ZwlrLayerShellV1LayerOverlay) {
		shown = c.drawTree(pass, o, e.surface.Surface(), e.bounds().Min, shown)
	}
	for _, e := range c.layers {
		shown = c.drawPopups(pass, o, c.layerPopups(e), shown)
	}
	shown = c.drawDragIcon(pass, o, shown)
	shown, err = c.drawCursor(pass, o, shown)
	if err != nil {
		return err
	}
	c.shown[o] = shown
	if err := pass.End(); err != nil {
		return err
	}
	return o.Backend().Present(damage)
}

func (c *Compositor) drawWindow(pass render.Pass, o *output.Output, w *window, shown []*surface.Surface) []*surface.Surface {
	if !w.visible || !w.tile.Overlaps(o.Logical()) {
		return shown
	}
	shown = c.drawTree(pass, o, w.toplevel.Surface(), w.rect.Min.Sub(w.toplevel.Geometry().Min), shown)
	c.drawBorder(pass, o, w)
	return shown
}

func (c *Compositor) drawPopups(pass render.Pass, o *output.Output, popups []placedPopup, shown []*surface.Surface) []*surface.Surface {
	for _, p := range popups {
		if p.bounds().Overlaps(o.Logical()) {
			shown = c.drawTree(pass, o, p.popup.Surface(), p.origin, shown)
		}
	}
	return shown
}

func (c *Compositor) drawTree(pass render.Pass, o *output.Output, s *surface.Surface, at image.Point, shown []*surface.Surface) []*surface.Surface {
	for _, member := range s.Stack() {
		if member != s {
			if sub := member.Subsurface(); sub != nil {
				shown = c.drawTree(pass, o, member, at.Add(sub.Position()), shown)
			}
			continue
		}
		tex := c.Texture(s)
		if tex == nil {
			continue
		}
		logical := image.Rectangle{Min: at, Max: at.Add(s.Size())}.Sub(o.Position())
		pass.DrawTexture(tex, s.BufferSource(), o.Scale().Rect(logical), s.Transform().Invert(), 1)
		shown = append(shown, s)
	}
	return shown
}

func (c *Compositor) enteredOf(s *surface.Surface) *entered {
	for _, e := range c.entered {
		if e.s == s {
			return e
		}
	}
	e := &entered{s: s}
	c.entered = append(c.entered, e)
	return e
}

func (c *Compositor) updateEntered(o *output.Output) error {
	var errs []error
	shown := c.shown[o]
	for _, s := range shown {
		e := c.enteredOf(s)
		if slices.Contains(e.outputs, o) {
			continue
		}
		e.outputs = append(e.outputs, o)
		errs = append(errs, s.Client().Sent(s.Enter(o.Resources(s.Client()))), s.Client().Sent(s.SetPreferredScale(uint32(o.Scale()))))
	}
	for _, e := range c.entered {
		if slices.Contains(e.outputs, o) && !slices.Contains(shown, e.s) {
			e.outputs = slices.DeleteFunc(e.outputs, func(x *output.Output) bool { return x == o })
			errs = append(errs, e.s.Client().Sent(e.s.Leave(o.Resources(e.s.Client()))))
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) Presented(b backend.Output, p backend.Presentation) error {
	o := c.output(b)
	if o == nil {
		return nil
	}
	var errs []error
	for _, s := range c.shown[o] {
		errs = append(errs,
			s.Client().Sent(s.FrameDone(wire.Millis(p.Time))),
			s.Client().Sent(s.Presented(o.Resources(s.Client()), p.Time, p.Refresh, p.Seq, spt.WpPresentationFeedbackKindVsync)))
	}
	return errors.Join(errs...)
}
