// Package shm implements wl_shm: client memory pools and the buffers carved
// out of them, read safely even when a client truncates its pool.
package shm

import (
	"errors"
	"fmt"
	"image"
	"runtime/debug"

	"golang.org/x/sys/unix"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const shmVersion = 2

var wireFormats = map[render.Format]sw.WlShmFormat{
	render.ARGB8888: sw.WlShmFormatArgb8888,
	render.XRGB8888: sw.WlShmFormatXrgb8888,
}

type Manager struct {
	formats map[sw.WlShmFormat]render.Format
	order   []sw.WlShmFormat
}

func New(d *server.Display, formats []render.Format) (*Manager, error) {
	m := &Manager{formats: map[sw.WlShmFormat]render.Format{}}
	for _, f := range formats {
		wf, ok := wireFormats[f]
		if !ok {
			return nil, fmt.Errorf("shm: renderer format %d has no wl_shm code", f)
		}
		m.formats[wf] = f
		m.order = append(m.order, wf)
	}
	if _, ok := m.formats[sw.WlShmFormatArgb8888]; !ok {
		return nil, errors.New("shm: the renderer cannot sample ARGB8888, which wl_shm requires")
	}
	if _, ok := m.formats[sw.WlShmFormatXrgb8888]; !ok {
		return nil, errors.New("shm: the renderer cannot sample XRGB8888, which wl_shm requires")
	}
	if _, err := d.AddGlobal(sw.WlShmInterface, shmVersion, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, version uint32) error {
	shm, err := sw.NewWlShm(c, id, version, shmRequests{m, c})
	if err != nil {
		return err
	}
	for _, f := range m.order {
		if err := shm.SendFormat(f); err != nil {
			return err
		}
	}
	return nil
}

func shmError(object wire.Resource, code sw.WlShmError, format string, args ...any) error {
	return &wire.ProtocolError{Object: object.Base().ID(), Code: uint32(code), Message: fmt.Sprintf(format, args...)}
}

type shmRequests struct {
	m *Manager
	c *server.Client
}

func (r shmRequests) CreatePool(obj *sw.WlShm, res *sw.WlShmPool, fd int, size int32) (sw.WlShmPoolHandler, error) {
	if size <= 0 {
		_ = unix.Close(fd)
		return nil, obj.ProtocolError(sw.WlShmErrorInvalidStride, fmt.Sprintf("invalid pool size %d", size))
	}
	mapping, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Close(fd)
		return nil, obj.ProtocolError(sw.WlShmErrorInvalidFD, fmt.Sprintf("cannot map pool fd: %v", err))
	}
	p := &pool{m: r.m, c: r.c, resource: res, fd: fd, mapping: mapping, refs: 1}
	r.c.OnDestroy(res, p.unref)
	return p, nil
}

func (r shmRequests) Release(*sw.WlShm) error {
	return nil
}

type pool struct {
	m        *Manager
	c        *server.Client
	resource *sw.WlShmPool
	fd       int
	mapping  []byte
	refs     int
}

func (p *pool) unref() error {
	p.refs--
	if p.refs > 0 {
		return nil
	}
	errs := []error{unix.Munmap(p.mapping), unix.Close(p.fd)}
	p.mapping, p.fd = nil, -1
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("shm: release pool: %w", err)
	}
	return nil
}

func (p *pool) CreateBuffer(obj *sw.WlShmPool, res *sw.WlBuffer, offset, width, height, stride int32, format sw.WlShmFormat) (sw.WlBufferHandler, error) {
	f, ok := p.m.formats[format]
	if !ok {
		return nil, shmError(obj, sw.WlShmErrorInvalidFormat, "unsupported format %#x", uint32(format))
	}
	if width <= 0 || height <= 0 || offset < 0 || int64(stride) < int64(width)*4 || int64(offset)+int64(stride)*int64(height) > int64(len(p.mapping)) {
		return nil, shmError(obj, sw.WlShmErrorInvalidStride, "invalid buffer: offset %d, %dx%d, stride %d in a %d-byte pool", offset, width, height, stride, len(p.mapping))
	}
	b := &Buffer{pool: p, resource: res, offset: int(offset), size: image.Pt(int(width), int(height)), stride: int(stride), format: f}
	p.refs++
	p.c.OnDestroy(res, p.unref)
	return b, nil
}

func (p *pool) Destroy(*sw.WlShmPool) error {
	return nil
}

func (p *pool) Resize(obj *sw.WlShmPool, size int32) error {
	switch {
	case int(size) < len(p.mapping):
		return shmError(obj, sw.WlShmErrorInvalidFD, "shrinking the pool from %d to %d bytes", len(p.mapping), size)
	case int(size) == len(p.mapping):
		return nil
	}
	mapping, err := unix.Mmap(p.fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return shmError(obj, sw.WlShmErrorInvalidFD, "cannot map the pool at %d bytes: %v", size, err)
	}
	if err := unix.Munmap(p.mapping); err != nil {
		return fmt.Errorf("shm: unmap the old pool mapping: %w", err)
	}
	p.mapping = mapping
	return nil
}

type Buffer struct {
	pool     *pool
	resource *sw.WlBuffer
	offset   int
	size     image.Point
	stride   int
	format   render.Format
}

func (b *Buffer) Size() image.Point {
	return b.size
}

func (b *Buffer) Format() render.Format {
	return b.format
}

func (b *Buffer) Destroy(*sw.WlBuffer) error {
	return nil
}

func (b *Buffer) Release() error {
	return b.resource.SendRelease()
}

func (b *Buffer) Read(f func(data []byte, stride int) error) (err error) {
	old := debug.SetPanicOnFault(true)
	defer debug.SetPanicOnFault(old)
	defer func() {
		if r := recover(); r != nil {
			fault, ok := r.(interface{ Addr() uintptr })
			if !ok {
				panic(r)
			}
			err = shmError(b.resource, sw.WlShmErrorInvalidFD, "error accessing the buffer's pool at %#x: the client shrank its file", fault.Addr())
		}
	}()
	return f(b.pool.mapping[b.offset:b.offset+b.stride*b.size.Y], b.stride)
}
