package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"

	kdrm "github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/edid"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/gles"
)

type gbmAllocator struct {
	dev       *kdrm.Device
	modifiers bool
	eglLib    *gpu.Library
	gbmLib    *gpu.GBMLibrary
	gbm       *gpu.GBM
	display   *gpu.Display
	gles      *gles.Renderer
}

func newGBMAllocator(dev *kdrm.Device, paths gpu.Paths) (*gbmAllocator, error) {
	a := &gbmAllocator{dev: dev}
	if err := a.open(paths); err != nil {
		return nil, errors.Join(err, a.close())
	}
	return a, nil
}

func (a *gbmAllocator) open(paths gpu.Paths) error {
	modifiers, err := a.dev.Cap(kdrm.CapAddFB2Modifiers)
	if err != nil {
		return fmt.Errorf("drm: ADDFB2 modifier support of %s: %w", a.dev.Path(), err)
	}
	a.modifiers = modifiers == 1
	if a.gbmLib, err = gpu.LoadGBM(paths); err != nil {
		return err
	}
	if a.gbm, err = a.gbmLib.Open(a.dev.FD()); err != nil {
		return err
	}
	if a.eglLib, err = gpu.Load(paths); err != nil {
		return err
	}
	if a.display, err = a.eglLib.OnGBM(a.gbm); err != nil {
		return fmt.Errorf("drm: EGL on %s: %w", a.dev.Path(), err)
	}
	a.gles, err = gles.New(a.display)
	return err
}

func (a *gbmAllocator) renderer() render.Renderer {
	return a.gles
}

func (a *gbmAllocator) renderable(format uint32) ([]uint64, error) {
	return a.gles.RenderableModifiers(format)
}

func (a *gbmAllocator) allocate(size image.Point, format uint32, modifiers []uint64) (*scanout, error) {
	usage := gpu.UseRendering
	if len(modifiers) == 0 {
		usage |= gpu.UseScanout
	}
	bo, err := a.gbm.Allocate(size, format, modifiers, usage)
	if err != nil {
		return nil, err
	}
	target, err := a.gles.ImportFramebuffer(bo.DMABUF)
	if err != nil {
		return nil, errors.Join(err, bo.Close())
	}
	fb, err := framebuffer(size, format, bo.Modifier, a.modifiers)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drm: %s: %w", a.dev.Path(), err), target.Destroy(), bo.Close())
	}
	var handles []uint32
	release := func() error {
		var errs []error
		for _, h := range handles {
			errs = append(errs, a.dev.CloseHandle(h))
		}
		return errors.Join(append(errs, target.Destroy(), bo.Close())...)
	}
	for i, p := range bo.Planes {
		h, err := a.dev.PrimeFDToHandle(p.FD)
		if err != nil {
			return nil, errors.Join(err, release())
		}
		if !slices.Contains(handles, h) {
			handles = append(handles, h)
		}
		fb.Handles[i], fb.Pitches[i], fb.Offsets[i] = h, p.Stride, p.Offset
	}
	id, err := a.dev.AddFB2(fb)
	if err != nil {
		return nil, errors.Join(err, release())
	}
	return &scanout{target: target, fb: id, free: func() error {
		return errors.Join(a.dev.RemoveFB(id), release())
	}}, nil
}

func framebuffer(size image.Point, format uint32, modifier uint64, withModifiers bool) (kdrm.FB, error) {
	fb := kdrm.FB{Width: uint32(size.X), Height: uint32(size.Y), Format: format} //nolint:gosec // scanout sizes fit
	switch {
	case withModifiers && modifier != gpu.ModifierInvalid:
		fb.Modifier, fb.WithModifier = modifier, true
	case !withModifiers && modifier != gpu.ModifierInvalid && modifier != gpu.ModifierLinear:
		return kdrm.FB{}, fmt.Errorf("the buffer has modifier %#x but the device takes no ADDFB2 modifiers, so only an implicit or linear layout can be scanned out", modifier)
	}
	return fb, nil
}

func (a *gbmAllocator) fence() (int, error) {
	if !a.gles.NativeFences() {
		return -1, a.gles.Finish()
	}
	return a.gles.SyncFile()
}

func (a *gbmAllocator) close() error {
	var errs []error
	if a.gles != nil {
		errs = append(errs, a.gles.Close())
	}
	if a.display != nil {
		errs = append(errs, a.display.Close())
	}
	if a.gbm != nil {
		a.gbm.Close()
	}
	if a.gbmLib != nil {
		errs = append(errs, a.gbmLib.Close())
	}
	if a.eglLib != nil {
		errs = append(errs, a.eglLib.Close())
	}
	return errors.Join(errs...)
}

func New(l *loop.Loop, dev *kdrm.Device, paths gpu.Paths, vendors edid.Vendors) (*Backend, error) {
	for _, c := range []uint64{kdrm.ClientCapUniversal, kdrm.ClientCapAtomic} {
		if err := dev.SetClientCap(c, 1); err != nil {
			return nil, fmt.Errorf("drm: %s needs atomic KMS: %w", dev.Path(), err)
		}
	}
	alloc, err := newGBMAllocator(dev, paths)
	if err != nil {
		return nil, err
	}
	b, err := newBackend(l, dev, alloc, vendors)
	if err != nil {
		return nil, errors.Join(err, alloc.close())
	}
	return b, nil
}
