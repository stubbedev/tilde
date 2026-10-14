package gpu

import (
	"errors"
	"fmt"
	"image"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

type Usage uint32

const (
	UseScanout   Usage = 1 << 0
	UseRendering Usage = 1 << 2
	UseLinear    Usage = 1 << 4
)

type gbmFuncs struct {
	createDevice   func(fd int32) uintptr
	destroyDevice  func(dev uintptr)
	backendName    func(dev uintptr) string
	create         func(dev uintptr, width, height, format, flags uint32) uintptr
	createWithMods func(dev uintptr, width, height, format uint32, modifiers *uint64, count, flags uint32) uintptr
	planeCount     func(bo uintptr) int32
	fdForPlane     func(bo uintptr, plane int32) int32
	strideForPlane func(bo uintptr, plane int32) uint32
	offset         func(bo uintptr, plane int32) uint32
	modifier       func(bo uintptr) uint64
	destroy        func(bo uintptr)
}

type GBMLibrary struct {
	fn  gbmFuncs
	lib uintptr
}

func LoadGBM(paths Paths) (*GBMLibrary, error) {
	lib, err := open(paths.GBM, GBMEnv)
	if err != nil {
		return nil, err
	}
	g := &GBMLibrary{lib: lib}
	f := &g.fn
	if err := bind(lib, paths.GBM, []symbol{
		{&f.createDevice, "gbm_create_device"},
		{&f.destroyDevice, "gbm_device_destroy"},
		{&f.backendName, "gbm_device_get_backend_name"},
		{&f.create, "gbm_bo_create"},
		{&f.createWithMods, "gbm_bo_create_with_modifiers2"},
		{&f.planeCount, "gbm_bo_get_plane_count"},
		{&f.fdForPlane, "gbm_bo_get_fd_for_plane"},
		{&f.strideForPlane, "gbm_bo_get_stride_for_plane"},
		{&f.offset, "gbm_bo_get_offset"},
		{&f.modifier, "gbm_bo_get_modifier"},
		{&f.destroy, "gbm_bo_destroy"},
	}); err != nil {
		return nil, errors.Join(err, purego.Dlclose(lib))
	}
	return g, nil
}

func (g *GBMLibrary) Close() error {
	return purego.Dlclose(g.lib)
}

type GBM struct {
	fn  *gbmFuncs
	dev uintptr
}

func (g *GBMLibrary) Open(fd int) (*GBM, error) {
	dev := g.fn.createDevice(int32(fd)) //nolint:gosec // file descriptors fit an int
	if dev == 0 {
		return nil, fmt.Errorf("gpu: gbm_create_device on fd %d failed", fd)
	}
	return &GBM{fn: &g.fn, dev: dev}, nil
}

func (d *GBM) Backend() string {
	return d.fn.backendName(d.dev)
}

func (d *GBM) Close() {
	d.fn.destroyDevice(d.dev)
}

type Buffer struct {
	DMABUF
	fn *gbmFuncs
	bo uintptr
}

func (d *GBM) Allocate(size image.Point, format uint32, modifiers []uint64, usage Usage) (*Buffer, error) {
	if size.X <= 0 || size.Y <= 0 {
		return nil, fmt.Errorf("gpu: cannot allocate a %dx%d buffer", size.X, size.Y)
	}
	w, h := uint32(size.X), uint32(size.Y) //nolint:gosec // checked positive above
	var bo uintptr
	if len(modifiers) > 0 {
		bo = d.fn.createWithMods(d.dev, w, h, format, &modifiers[0], uint32(len(modifiers)), uint32(usage)) //nolint:gosec // a handful of modifiers
	} else {
		bo = d.fn.create(d.dev, w, h, format, uint32(usage))
	}
	if bo == 0 {
		return nil, fmt.Errorf("gpu: gbm cannot allocate a %dx%d buffer of format %#x with modifiers %#x", size.X, size.Y, format, modifiers)
	}
	b := &Buffer{fn: d.fn, bo: bo, Size: size, Format: format, Modifier: d.fn.modifier(bo)}
	for i := range d.fn.planeCount(bo) {
		fd := d.fn.fdForPlane(bo, i)
		if fd < 0 {
			return nil, errors.Join(fmt.Errorf("gpu: export plane %d of a gbm buffer", i), b.Close())
		}
		b.Planes = append(b.Planes, Plane{FD: int(fd), Offset: d.fn.offset(bo, i), Stride: d.fn.strideForPlane(bo, i)})
	}
	return b, nil
}

func (b *Buffer) Close() error {
	var errs []error
	for _, p := range b.Planes {
		if err := unix.Close(p.FD); err != nil {
			errs = append(errs, fmt.Errorf("gpu: close plane fd %d: %w", p.FD, err))
		}
	}
	b.Planes = nil
	b.fn.destroy(b.bo)
	return errors.Join(errs...)
}
