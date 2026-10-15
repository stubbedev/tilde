package drm

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/tilde/internal/fourcc"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/render"
)

type blitAllocator struct {
	primary *gbmAllocator
	local   *gbmAllocator
}

func (a *blitAllocator) renderer() render.Renderer {
	return a.primary.stack.Renderer
}

func (a *blitAllocator) renderable(format fourcc.Format) ([]fourcc.Modifier, error) {
	return a.local.renderable(format)
}

type shared struct {
	buffer *gpu.Buffer
	target render.ReadTarget
	tex    render.Texture
}

func (a *blitAllocator) share(size image.Point, format fourcc.Format) (*shared, error) {
	b, err := a.primary.stack.GBM.Allocate(size, format, []fourcc.Modifier{fourcc.Linear}, gpu.UseRendering)
	if err != nil {
		return nil, fmt.Errorf("a linear buffer on the primary GPU: %w", err)
	}
	target, err := a.primary.stack.Renderer.ImportFramebuffer(b.DMABUF)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("render into it on the primary GPU: %w", err), b.Close())
	}
	tex, err := a.local.stack.Renderer.ImportTexture(b.DMABUF)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("read it on %s: %w", a.local.dev.Path(), err), target.Destroy(), b.Close())
	}
	return &shared{buffer: b, target: target, tex: tex}, nil
}

func (s *shared) free() error {
	return errors.Join(s.tex.Destroy(), s.target.Destroy(), s.buffer.Close())
}

func (a *blitAllocator) probe() error {
	s, err := a.share(image.Pt(16, 16), scanoutFormat)
	if err != nil {
		return err
	}
	return s.free()
}

func (a *blitAllocator) allocate(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier) (*scanout, error) {
	out, err := a.local.allocate(size, format, modifiers)
	if err != nil {
		return nil, err
	}
	s, err := a.share(size, format)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: a %v frame shared with %s: %w", size, a.local.dev.Path(), err), out.free())
	}
	return &scanout{
		target:  s.target,
		fb:      out.fb,
		prepare: func() error { return a.blit(s.tex, out.target, size) },
		free:    func() error { return errors.Join(s.free(), out.free()) },
	}, nil
}

func (a *blitAllocator) blit(tex render.Texture, dst render.Target, size image.Point) error {
	if a.primary.stack.Renderer.NativeFences() && a.local.stack.Renderer.NativeFences() {
		fd, err := a.primary.stack.Renderer.SyncFile()
		if err != nil {
			return err
		}
		if err := a.local.stack.Renderer.WaitFence(fd); err != nil {
			return err
		}
	} else if err := a.primary.stack.Renderer.Finish(); err != nil {
		return err
	}
	all := image.Rectangle{Max: size}
	p, err := a.local.stack.Renderer.Begin(dst, render.Region{all})
	if err != nil {
		return err
	}
	p.DrawTexture(tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, all, render.Normal, 1)
	return p.End()
}

func (a *blitAllocator) fence() (int, error) {
	return a.local.fence()
}

func (a *blitAllocator) close() error {
	return a.local.close()
}
