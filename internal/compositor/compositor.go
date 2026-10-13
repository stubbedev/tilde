// Package compositor connects backend events to the server and draws each
// output's frames.
package compositor

import (
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/render"
)

var Background = render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}

type Compositor struct {
	outputs []backend.Output
}

func New(b backend.Backend) (*Compositor, error) {
	c := &Compositor{}
	if err := b.Start(c); err != nil {
		return nil, fmt.Errorf("compositor: start backend: %w", err)
	}
	return c, nil
}

func (c *Compositor) Outputs() []backend.Output {
	return slices.Clone(c.outputs)
}

func (c *Compositor) OutputAdded(o backend.Output) error {
	c.outputs = append(c.outputs, o)
	if err := o.ScheduleFrame(); err != nil {
		return fmt.Errorf("compositor: first frame on %s: %w", o.Name(), err)
	}
	return nil
}

func (c *Compositor) OutputRemoved(o backend.Output) error {
	if i := slices.Index(c.outputs, o); i >= 0 {
		c.outputs = slices.Delete(c.outputs, i, i+1)
	}
	return nil
}

func (c *Compositor) OutputChanged(o backend.Output) error {
	if err := o.ScheduleFrame(); err != nil {
		return fmt.Errorf("compositor: frame after %s changed: %w", o.Name(), err)
	}
	return nil
}

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
	whole := image.Rectangle{Max: target.Size()}
	damage := render.Region{whole}
	pass, err := r.Begin(target, damage)
	if err != nil {
		return err
	}
	pass.FillRect(whole, Background)
	if err := pass.End(); err != nil {
		return err
	}
	return o.Present(damage)
}

func (c *Compositor) Presented(backend.Output, backend.Presentation) error { return nil }

func (c *Compositor) DeviceAdded(backend.Device) error { return nil }

func (c *Compositor) DeviceRemoved(backend.Device) error { return nil }

func (c *Compositor) Input(backend.Device, backend.Event) error { return nil }
