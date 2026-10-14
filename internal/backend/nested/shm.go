package nested

import (
	"errors"
	"fmt"
	"image"
	"math"

	"golang.org/x/sys/unix"

	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/soft"
)

const shmBuffers = 2

type shmChain struct {
	shm  *cw.WlShm
	soft *soft.Renderer
}

func (c *shmChain) renderer() render.Renderer {
	return c.soft
}

func (c *shmChain) finish() error {
	return nil
}

func (c *shmChain) close() error {
	return nil
}

func (c *shmChain) allocate(o *Output, size image.Point) ([]*buffer, func() error, error) {
	stride := size.X * 4
	frame := stride * size.Y
	if size.X > math.MaxInt16 || size.Y > math.MaxInt16 || shmBuffers*frame > math.MaxInt32 {
		return nil, nil, fmt.Errorf("nested: host window %v is too large for shm buffers", size)
	}
	fd, err := unix.MemfdCreate("tilde-nested", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, nil, fmt.Errorf("nested: memfd_create: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Ftruncate(fd, int64(shmBuffers*frame)); err != nil {
		return nil, nil, fmt.Errorf("nested: size the %s buffers to %d bytes: %w", o.name, shmBuffers*frame, err)
	}
	mapping, err := unix.Mmap(fd, 0, shmBuffers*frame, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, nil, fmt.Errorf("nested: map the %s buffers: %w", o.name, err)
	}
	free := func() error { return unix.Munmap(mapping) }
	pool, err := c.shm.CreatePool(fd, int32(shmBuffers*frame)) //nolint:gosec // bounded by the size check above
	if err != nil {
		return nil, nil, errors.Join(err, free())
	}
	buffers := make([]*buffer, shmBuffers)
	for i := range buffers {
		buf := &buffer{target: &soft.Buffer{Pix: mapping[i*frame : (i+1)*frame], Stride: stride, W: size.X, H: size.Y}}
		if buf.wl, err = pool.CreateBuffer(bufferEvents{o, buf}, int32(i*frame), int32(size.X), int32(size.Y), int32(stride), cw.WlShmFormatXrgb8888); err != nil { //nolint:gosec // bounded by the size check above
			return nil, nil, errors.Join(err, free())
		}
		buffers[i] = buf
	}
	return buffers, free, pool.Destroy()
}
