package compositor

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

func (c *Compositor) Frame(o backend.Output) error {
	if err := c.render(o); err != nil {
		return fmt.Errorf("compositor: frame on %s: %w", o.Name(), err)
	}
	return nil
}

func (c *Compositor) render(o backend.Output) error {
	r, target, err := o.BeginFrame()
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
	for _, w := range c.windows {
		if w.output != o {
			continue
		}
		origin := w.placement().Min.Sub(w.toplevel.Geometry().Min)
		shown = c.drawTree(pass, w.toplevel.Surface(), origin, shown)
	}
	c.shown[o] = shown
	if err := pass.End(); err != nil {
		return err
	}
	return o.Present(damage)
}

func (c *Compositor) drawTree(pass render.Pass, s *surface.Surface, at image.Point, shown []*surface.Surface) []*surface.Surface {
	for _, member := range s.Stack() {
		if member != s {
			if sub := member.Subsurface(); sub != nil {
				shown = c.drawTree(pass, member, at.Add(sub.Position()), shown)
			}
			continue
		}
		tex := c.Texture(s)
		if tex == nil {
			continue
		}
		size := tex.Size()
		dst := image.Rectangle{Min: at, Max: at.Add(s.Size())}
		pass.DrawTexture(tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, dst, s.Transform().Invert(), 1)
		shown = append(shown, s)
	}
	return shown
}

func (c *Compositor) Presented(o backend.Output, p backend.Presentation) error {
	var errs []error
	for _, s := range c.shown[o] {
		errs = append(errs, s.Client().Sent(s.FrameDone(wire.Millis(p.Time))))
	}
	return errors.Join(errs...)
}
