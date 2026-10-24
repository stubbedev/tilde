package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"golang.org/x/sys/unix"

	kdrm "github.com/stubbedev/lys-wm/internal/drm"
	"github.com/stubbedev/lys-wm/internal/edid"
	"github.com/stubbedev/lys-wm/internal/fourcc"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/gles"
)

type gbmAllocator struct {
	dev       *kdrm.Device
	modifiers bool
	stack     *gles.Stack
}

func newGBMAllocator(dev *kdrm.Device, paths gpu.Paths) (*gbmAllocator, error) {
	modifiers, err := dev.Cap(kdrm.CapAddFB2Modifiers)
	if err != nil {
		return nil, fmt.Errorf("drm: ADDFB2 modifier support of %s: %w", dev.Path(), err)
	}
	stack, err := gles.OnGBM(dev.FD(), paths)
	if err != nil {
		return nil, fmt.Errorf("drm: GLES on %s: %w", dev.Path(), err)
	}
	return &gbmAllocator{dev: dev, modifiers: modifiers == 1, stack: stack}, nil
}

func (a *gbmAllocator) renderer() render.Renderer {
	return a.stack.Renderer
}

func (a *gbmAllocator) renderable(format fourcc.Format) ([]fourcc.Modifier, error) {
	return a.stack.Renderer.RenderableModifiers(format)
}

func (a *gbmAllocator) allocate(size image.Point, format fourcc.Format, modifiers []fourcc.Modifier) (*scanout, error) {
	usage := gpu.UseRendering
	if len(modifiers) == 0 {
		usage |= gpu.UseScanout
	}
	bo, err := a.stack.GBM.Allocate(size, format, modifiers, usage)
	if err != nil {
		return nil, err
	}
	target, err := a.stack.Renderer.ImportFramebuffer(bo.DMABUF)
	if err != nil {
		return nil, errors.Join(err, bo.Close())
	}
	id, remove, err := importFB(a.dev, bo.DMABUF, a.modifiers)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: %s: %w", a.dev.Path(), err), target.Destroy(), bo.Close())
	}
	return &scanout{target: target, fb: id, free: func() error {
		return errors.Join(remove(), target.Destroy(), bo.Close())
	}}, nil
}

func importFB(dev device, d gpu.DMABUF, withModifiers bool) (uint32, func() error, error) {
	fb, err := framebuffer(d.Size, d.Format, d.Modifier, withModifiers)
	if err != nil {
		return 0, nil, err
	}
	var handles []uint32
	closeHandles := func() error {
		errs := make([]error, 0, len(handles))
		for _, h := range handles {
			errs = append(errs, dev.CloseHandle(h))
		}
		return errors.Join(errs...)
	}
	for i, p := range d.Planes {
		h, err := dev.PrimeFDToHandle(p.FD)
		if err != nil {
			return 0, nil, errors.Join(err, closeHandles())
		}
		if !slices.Contains(handles, h) {
			handles = append(handles, h)
		}
		fb.Handles[i], fb.Pitches[i], fb.Offsets[i] = h, p.Stride, p.Offset
	}
	id, err := dev.AddFB2(fb)
	if err != nil {
		return 0, nil, errors.Join(err, closeHandles())
	}
	return id, func() error { return errors.Join(dev.RemoveFB(id), closeHandles()) }, nil
}

func framebuffer(size image.Point, format fourcc.Format, modifier fourcc.Modifier, withModifiers bool) (kdrm.FB, error) {
	fb := kdrm.FB{Width: uint32(size.X), Height: uint32(size.Y), Format: format} //nolint:gosec // scanout sizes fit
	switch {
	case !addable(modifier, withModifiers):
		return kdrm.FB{}, fmt.Errorf("the buffer has modifier %v but the device takes no ADDFB2 modifiers, so only an implicit or linear layout can be scanned out", modifier)
	case withModifiers && modifier != fourcc.Invalid:
		fb.Modifier, fb.WithModifier = modifier, true
	}
	return fb, nil
}

func addable(modifier fourcc.Modifier, withModifiers bool) bool {
	return withModifiers || modifier == fourcc.Invalid || modifier == fourcc.Linear
}

func (a *gbmAllocator) fence() (int, error) {
	if !a.stack.Renderer.NativeFences() {
		return -1, a.stack.Renderer.Finish()
	}
	return a.stack.Renderer.SyncFile()
}

func (a *gbmAllocator) close() error {
	return a.stack.Close()
}

func atomic(dev *kdrm.Device) error {
	for _, c := range []uint64{kdrm.ClientCapUniversal, kdrm.ClientCapAtomic} {
		if err := dev.SetClientCap(c, 1); err != nil {
			return fmt.Errorf("drm: %s needs atomic KMS: %w", dev.Path(), err)
		}
	}
	if err := dev.SetClientCap(kdrm.ClientCapCursorHotspot, 1); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) {
		return fmt.Errorf("drm: cursor hotspots on %s: %w", dev.Path(), err)
	}
	return nil
}

type Options struct {
	Paths   gpu.Paths
	Vendors edid.Vendors
	Report  func(error)
}

func New(l *loop.Loop, dev *kdrm.Device, opts Options) (*Backend, error) {
	if err := atomic(dev); err != nil {
		return nil, err
	}
	alloc, err := newGBMAllocator(dev, opts.Paths)
	if err != nil {
		return nil, err
	}
	b, err := newBackend(l, dev, alloc, opts)
	if err != nil {
		return nil, errors.Join(err, alloc.close())
	}
	return b, nil
}
