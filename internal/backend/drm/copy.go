package drm

import (
	"errors"
	"fmt"
	"image"

	kdrm "github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/edid"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
)

type copyAllocator struct {
	dev     device
	primary render.Renderer
}

func (a *copyAllocator) renderer() render.Renderer {
	return a.primary
}

func (a *copyAllocator) renderable(uint32) ([]uint64, error) {
	return []uint64{gpu.ModifierLinear}, nil
}

func (a *copyAllocator) allocate(size image.Point, format uint32, _ []uint64) (*scanout, error) {
	target, err := a.primary.NewTarget(size)
	if err != nil {
		return nil, fmt.Errorf("drm: a %v target on the primary GPU: %w", size, err)
	}
	dumb, err := a.dev.CreateDumb(size)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: a %v scanout buffer: %w", size, err), target.Destroy())
	}
	fb, err := a.dev.AddFB2(kdrm.FB{Width: uint32(size.X), Height: uint32(size.Y), Format: format, Handles: [4]uint32{dumb.Handle}, Pitches: [4]uint32{dumb.Pitch}}) //nolint:gosec // scanout sizes fit
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: a %v framebuffer: %w", size, err), a.dev.DestroyDumb(dumb), target.Destroy())
	}
	return &scanout{
		target:  target,
		fb:      fb,
		prepare: func() error { return target.Read(dumb.Pix, int(dumb.Pitch)) },
		free:    func() error { return errors.Join(a.dev.RemoveFB(fb), a.dev.DestroyDumb(dumb), target.Destroy()) },
		drop:    target.Destroy,
	}, nil
}

func (a *copyAllocator) fence() (int, error) {
	return -1, nil
}

func (a *copyAllocator) close() error {
	return nil
}

func NewSecondary(l *loop.Loop, dev *kdrm.Device, primary render.Renderer, vendors edid.Vendors) (*Backend, error) {
	if err := atomic(dev); err != nil {
		return nil, err
	}
	return newBackend(l, dev, &copyAllocator{dev: dev, primary: primary}, vendors)
}
