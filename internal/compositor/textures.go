package compositor

import (
	"fmt"
	"slices"

	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/shm"
	"github.com/stubbedev/tilde/internal/surface"
)

type texture struct {
	tex    render.Texture
	format render.Format
}

func (c *Compositor) Committed(s *surface.Surface) error {
	if s.Attached() {
		if err := c.attach(s); err != nil {
			return err
		}
	}
	return c.scheduleAll()
}

func (c *Compositor) attach(s *surface.Surface) error {
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

func (c *Compositor) Destroyed(s *surface.Surface) error {
	if err := c.seat.SurfaceDestroyed(s); err != nil {
		return err
	}
	c.dropTexture(s)
	for o, shown := range c.shown {
		c.shown[o] = slices.DeleteFunc(shown, func(x *surface.Surface) bool { return x == s })
	}
	c.entered = slices.DeleteFunc(c.entered, func(e *entered) bool { return e.s == s })
	return nil
}
