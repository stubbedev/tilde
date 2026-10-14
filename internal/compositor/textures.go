package compositor

import (
	"errors"
	"fmt"
	"slices"

	"github.com/stubbedev/tilde/internal/dmabuf"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/shm"
	"github.com/stubbedev/tilde/internal/singlepixel"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/syncobj"
)

type texture struct {
	tex    render.Texture
	format render.Format
	buffer *dmabuf.Buffer
}

type dmabufRenderer interface {
	dmabuf.Importer
	ImportTexture(b gpu.DMABUF) (render.Texture, error)
}

type syncRenderer interface {
	syncobj.Fencer
	RenderNode() (string, error)
	NativeFences() bool
}

func (c *Compositor) Committed(s *surface.Surface) error {
	c.cursorCommitted(s)
	if d := s.Damage(); !d.Empty() {
		for _, sc := range c.scenes {
			sc.damaged(s, d)
		}
	}
	if s.Attached() {
		if err := c.attach(s); err != nil {
			return err
		}
	}
	return errors.Join(c.noteCommit(s), c.updateCursorSessions(), c.scheduleAll())
}

func (c *Compositor) attach(s *surface.Surface) error {
	switch b := s.Buffer().(type) {
	case nil:
		return c.dropTexture(s)
	case *shm.Buffer:
		if err := c.upload(s, b); err != nil {
			return err
		}
		return b.Release()
	case *dmabuf.Buffer:
		if t := c.textures[s]; t != nil && t.buffer == b {
			return nil
		}
		tex, ok := c.imported[b]
		if !ok {
			var err error
			if tex, err = c.importer.ImportTexture(b.DMABUF()); err != nil {
				return fmt.Errorf("compositor: import the dmabuf of wl_surface@%d: %w", s.Resource().Base().ID(), err)
			}
			c.imported[b] = tex
		}
		if err := c.dropTexture(s); err != nil {
			return err
		}
		c.textures[s] = &texture{tex: tex, buffer: b}
		return nil
	case *singlepixel.Buffer:
		tex, err := c.renderer.NewTexture(render.ARGB8888, b.Size(), 4, b.Pixel())
		if err != nil {
			return fmt.Errorf("compositor: upload single-pixel wl_surface@%d: %w", s.Resource().Base().ID(), err)
		}
		if err := c.dropTexture(s); err != nil {
			return errors.Join(err, tex.Destroy())
		}
		c.textures[s] = &texture{tex: tex, format: render.ARGB8888}
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
		if err := c.dropTexture(s); err != nil {
			return errors.Join(err, tex.Destroy())
		}
		c.textures[s] = &texture{tex: tex, format: b.Format()}
		return nil
	})
}

func (c *Compositor) dropTexture(s *surface.Surface) error {
	t, ok := c.textures[s]
	if !ok {
		return nil
	}
	delete(c.textures, s)
	if t.buffer != nil {
		return c.releaseBuffer(t.buffer, s.Client())
	}
	if err := t.tex.Destroy(); err != nil {
		return fmt.Errorf("compositor: destroy the texture of wl_surface@%d: %w", s.Resource().Base().ID(), err)
	}
	return nil
}

func (c *Compositor) BufferDestroyed(b *dmabuf.Buffer) error {
	c.scanoutBufferGone(b)
	shown := false
	for s, t := range c.textures {
		if t.buffer == b {
			delete(c.textures, s)
			shown = true
		}
	}
	var errs []error
	if tex, ok := c.imported[b]; ok {
		delete(c.imported, b)
		errs = append(errs, tex.Destroy())
	}
	if shown {
		errs = append(errs, c.scheduleAll())
	}
	return errors.Join(errs...)
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
	if err := c.dropTexture(s); err != nil {
		return err
	}
	for _, sc := range c.scenes {
		sc.forget(s)
	}
	for o, shown := range c.shown {
		c.shown[o] = slices.DeleteFunc(shown, func(x *surface.Surface) bool { return x == s })
	}
	c.entered = slices.DeleteFunc(c.entered, func(e *entered) bool { return e.s == s })
	return nil
}
