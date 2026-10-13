// Package compositor connects backend events to the server and draws each
// output's frames.
package compositor

import (
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/seat"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shm"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/xkb"
)

var Background = render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}

type Config struct {
	Seat seat.Config
}

func DefaultConfig(keymap *xkb.Keymap) Config {
	return Config{Seat: seat.Config{Keymap: keymap, RepeatRate: seat.DefaultRepeatRate, RepeatDelay: seat.DefaultRepeatDelay}}
}

type Compositor struct {
	outputs  []backend.Output
	renderer render.Renderer
	surfaces *surface.Manager
	shm      *shm.Manager
	seat     *seat.Seat
	textures map[*surface.Surface]*texture
}

type texture struct {
	tex    render.Texture
	format render.Format
}

func New(display *server.Display, b backend.Backend, cfg Config) (*Compositor, error) {
	c := &Compositor{renderer: b.Renderer(), textures: map[*surface.Surface]*texture{}}
	var err error
	if c.surfaces, err = surface.New(display, c); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.shm, err = shm.New(display, c.renderer.Formats()); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.seat, err = seat.New(display, cfg.Seat); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
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

func (c *Compositor) Close() {
	c.seat.Close()
}

func (c *Compositor) Seat() *seat.Seat {
	return c.seat
}

func (c *Compositor) DeviceAdded(d backend.Device) error {
	if d.Capabilities().Has(backend.Keyboard) {
		return c.seat.AddKeyboard()
	}
	return nil
}

func (c *Compositor) DeviceRemoved(d backend.Device) error {
	if d.Capabilities().Has(backend.Keyboard) {
		return c.seat.RemoveKeyboard()
	}
	return nil
}

func (c *Compositor) Input(_ backend.Device, e backend.Event) error {
	if k, ok := e.(backend.Key); ok {
		return c.seat.Key(k.Code, k.Pressed, k.Time)
	}
	return nil
}

func (c *Compositor) Committed(s *surface.Surface) error {
	if !s.Attached() {
		return nil
	}
	switch b := s.Buffer().(type) {
	case nil:
		c.dropTexture(s)
		return nil
	case *shm.Buffer:
		if err := c.upload(s, b); err != nil {
			return err
		}
		return b.Release()
	default:
		return fmt.Errorf("compositor: no upload path for buffers of type %T", b)
	}
}

func (c *Compositor) upload(s *surface.Surface, b *shm.Buffer) error {
	t := c.textures[s]
	return b.Read(func(data []byte, stride int) error {
		if t != nil && t.format == b.Format() && t.tex.Size() == b.Size() {
			return t.tex.Update(stride, data, s.BufferDamage().Rects())
		}
		tex, err := c.renderer.NewTexture(b.Format(), b.Size(), stride, data)
		if err != nil {
			return fmt.Errorf("compositor: upload wl_surface@%d: %w", s.Resource().Base().ID(), err)
		}
		c.dropTexture(s)
		c.textures[s] = &texture{tex: tex, format: b.Format()}
		return nil
	})
}

func (c *Compositor) dropTexture(s *surface.Surface) {
	if t, ok := c.textures[s]; ok {
		t.tex.Destroy()
		delete(c.textures, s)
	}
}

func (c *Compositor) Texture(s *surface.Surface) render.Texture {
	if t, ok := c.textures[s]; ok {
		return t.tex
	}
	return nil
}

func (c *Compositor) Destroyed(s *surface.Surface) {
	c.seat.SurfaceDestroyed(s)
	c.dropTexture(s)
}
