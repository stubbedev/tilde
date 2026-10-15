package gles

import (
	"errors"
	"fmt"
	"image"
	"strconv"
	"strings"

	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/render"
)

func esMajor(version string) int {
	rest, ok := strings.CutPrefix(version, "OpenGL ES ")
	if !ok {
		return 0
	}
	major, _, _ := strings.Cut(rest, ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}
	return n
}

func (r *Renderer) AsyncReads() bool {
	return r.async
}

func (r *Renderer) NewAsyncTarget(size image.Point) (render.AsyncReadTarget, error) {
	if !r.async {
		return nil, fmt.Errorf("%w: %s reads framebuffers back only synchronously", gpu.ErrUnsupported, r.gl.Renderer())
	}
	return r.NewFramebuffer(size)
}

type pendingRead struct {
	f      *Framebuffer
	fence  *gpu.Sync
	mapped bool
}

func (f *Framebuffer) ReadAsync() (render.PendingRead, error) {
	r := f.r
	switch {
	case !r.async:
		return nil, fmt.Errorf("%w: %s reads framebuffers back only synchronously", gpu.ErrUnsupported, r.gl.Renderer())
	case f.reading:
		return nil, fmt.Errorf("gles: a read of the %v framebuffer is still pending", f.size)
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	gl := r.gl
	if f.pbo == 0 {
		gl.GenBuffers(1, &f.pbo)
		gl.BindBuffer(gpu.PixelPackBuffer, f.pbo)
		gl.AllocateBuffer(gpu.PixelPackBuffer, f.size.X*4*f.size.Y, gpu.StreamRead)
	} else {
		gl.BindBuffer(gpu.PixelPackBuffer, f.pbo)
	}
	gl.BindFramebuffer(gpu.Framebuffer, f.fbo)
	gl.ReadPixelsIntoBuffer(0, 0, int32(f.size.X), int32(f.size.Y), gpu.BGRAExt) //nolint:gosec // framebuffer sizes fit GLsizei
	gl.BindFramebuffer(gpu.Framebuffer, 0)
	gl.BindBuffer(gpu.PixelPackBuffer, 0)
	fence, err := r.d.PlainFence()
	if err = errors.Join(err, r.leave("gles: start reading a framebuffer")); err != nil {
		if fence != nil {
			err = errors.Join(err, fence.Destroy())
		}
		return nil, err
	}
	f.reading = true
	return &pendingRead{f: f, fence: fence}, nil
}

func (p *pendingRead) Wait() error {
	return p.fence.ClientWait()
}

func (p *pendingRead) Map() ([]byte, error) {
	f := p.f
	if p.mapped {
		return nil, fmt.Errorf("gles: the %v framebuffer's pixel buffer is already mapped", f.size)
	}
	if err := f.r.enter(); err != nil {
		return nil, err
	}
	gl := f.r.gl
	gl.BindBuffer(gpu.PixelPackBuffer, f.pbo)
	m := gl.MapBufferForReading(gpu.PixelPackBuffer, f.size.X*4*f.size.Y)
	gl.BindBuffer(gpu.PixelPackBuffer, 0)
	err := f.r.leave("gles: map a framebuffer's pixel buffer")
	if m == nil {
		err = errors.Join(fmt.Errorf("gles: map the %v framebuffer's pixel buffer", f.size), err)
	}
	if err != nil {
		return nil, err
	}
	p.mapped = true
	return m, nil
}

func (p *pendingRead) Release() error {
	f := p.f
	f.reading = false
	err := p.fence.Destroy()
	if !p.mapped {
		return err
	}
	p.mapped = false
	if entered := f.r.enter(); entered != nil {
		return errors.Join(err, entered)
	}
	gl := f.r.gl
	gl.BindBuffer(gpu.PixelPackBuffer, f.pbo)
	if !gl.UnmapBuffer(gpu.PixelPackBuffer) {
		err = errors.Join(err, fmt.Errorf("gles: the %v framebuffer's pixel buffer was lost while mapped", f.size))
	}
	gl.BindBuffer(gpu.PixelPackBuffer, 0)
	return errors.Join(err, f.r.leave("gles: unmap a framebuffer's pixel buffer"))
}
