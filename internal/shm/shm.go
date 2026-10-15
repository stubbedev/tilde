// Package shm implements wl_shm: client memory pools and the buffers carved
// out of them, read safely even when a client truncates its pool.
package shm

import (
	"errors"
	"fmt"
	"image"
	"os"
	"runtime/debug"
	"sync/atomic"

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
	return wire.NewProtocolError(object.Base().ID(), uint32(code), format, args...)
}

type shmRequests struct {
	m *Manager
	c *server.Client
}

func (r shmRequests) CreatePool(obj *sw.WlShm, res *sw.WlShmPool, fd int, size int32) (sw.WlShmPoolHandler, error) {
	if size <= 0 {
		_ = unix.Close(fd)
		return nil, obj.ProtocolError(sw.WlShmErrorInvalidStride, "invalid pool size %d", size)
	}
	mapping, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Close(fd)
		return nil, obj.ProtocolError(sw.WlShmErrorInvalidFD, "cannot map pool fd: %v", err)
	}
	p := &pool{m: r.m, c: r.c, resource: res, fd: fd, mapping: mapping, refs: 1}
	server.OnDestroy(res, p.unref)
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
	writable []byte
	refs     int
}

func (p *pool) unref() error {
	p.refs--
	if p.refs > 0 {
		return nil
	}
	errs := []error{unix.Munmap(p.mapping), p.dropWritable(), unix.Close(p.fd)}
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
	size := image.Pt(int(width), int(height))
	if offset < 0 || int(offset)+int(stride)*int(height) > len(p.mapping) {
		return nil, shmError(obj, sw.WlShmErrorInvalidStride, "invalid buffer: offset %d, %dx%d, stride %d in a %d-byte pool", offset, width, height, stride, len(p.mapping))
	}
	if err := render.CheckPixels(size, int(stride), len(p.mapping)-int(offset)); err != nil {
		return nil, shmError(obj, sw.WlShmErrorInvalidStride, "invalid buffer: %v", err)
	}
	b := &Buffer{pool: p, resource: res, offset: int(offset), size: size, stride: int(stride), format: f}
	p.refs++
	server.OnDestroy(res, func() error { return errors.Join(b.dropWriter(), p.unref()) })
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
	if err := errors.Join(unix.Munmap(p.mapping), p.dropWritable()); err != nil {
		return fmt.Errorf("shm: unmap the old pool mapping: %w", err)
	}
	p.mapping = mapping
	return nil
}

func (p *pool) dropWritable() error {
	if p.writable == nil {
		return nil
	}
	err := unix.Munmap(p.writable)
	p.writable = nil
	return err
}

func (p *pool) writableMapping() ([]byte, error) {
	if p.writable != nil {
		return p.writable, nil
	}
	m, err := unix.Mmap(p.fd, 0, len(p.mapping), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("shm: map the pool for writing: %w", err)
	}
	p.writable = m
	return m, nil
}

type Buffer struct {
	pool     *pool
	resource *sw.WlBuffer
	offset   int
	size     image.Point
	stride   int
	format   render.Format
	writer   *Mapping
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

func (b *Buffer) Write(f func(data []byte, stride int) error) (err error) {
	m, err := b.pool.writableMapping()
	if err != nil {
		return err
	}
	return b.guard(b.within(m, 0), f)
}

func (b *Buffer) Read(f func(data []byte, stride int) error) (err error) {
	return b.guard(b.within(b.pool.mapping, 0), f)
}

func (b *Buffer) within(mapping []byte, at int) []byte {
	return mapping[b.offset-at : b.offset-at+b.stride*b.size.Y]
}

type Mapping struct {
	b     *Buffer
	pages []byte
	at    int
	refs  atomic.Int32
}

func (b *Buffer) MapForWriting() (*Mapping, error) {
	if b.writer == nil {
		at := b.offset &^ (os.Getpagesize() - 1)
		pages, err := unix.Mmap(b.pool.fd, int64(at), b.offset+b.stride*b.size.Y-at, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			return nil, fmt.Errorf("shm: map a %v buffer at offset %d for writing: %w", b.size, b.offset, err)
		}
		b.writer = &Mapping{b: b, pages: pages, at: at}
		b.writer.refs.Store(1)
	}
	b.writer.refs.Add(1)
	return b.writer, nil
}

func (b *Buffer) dropWriter() error {
	if b.writer == nil {
		return nil
	}
	m := b.writer
	b.writer = nil
	return m.Close()
}

func (m *Mapping) Write(f func(data []byte, stride int) error) error {
	return m.b.guard(m.b.within(m.pages, m.at), f)
}

func (m *Mapping) Close() error {
	if m.refs.Add(-1) > 0 {
		return nil
	}
	if err := unix.Munmap(m.pages); err != nil {
		return fmt.Errorf("shm: unmap a %v buffer mapped for writing: %w", m.b.size, err)
	}
	return nil
}

func (b *Buffer) guard(data []byte, f func(data []byte, stride int) error) (err error) {
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
	return f(data, b.stride)
}
