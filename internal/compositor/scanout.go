package compositor

import (
	"errors"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/dmabuf"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
)

type scannedOut struct {
	outputs  int
	released *server.Client
	then     []func() error
}

var opaqueFormats = []uint32{gpu.FormatXRGB8888, gpu.FormatXBGR8888}

func (c *Compositor) scanout(o *output.Output, sc *scene, bounds image.Rectangle) (bool, error) {
	out, ok := o.Backend().(backend.Scanner)
	if !ok {
		return false, nil
	}
	ops := sc.cur.ops
	if len(ops) != 2 || sc.cur.cursorFrom != len(ops) || !ops[0].fill {
		return false, nil
	}
	op := ops[1]
	if op.surface == nil || op.dst != bounds || op.transform != render.Normal || op.alpha != 1 {
		return false, nil
	}
	t := c.textures[op.surface]
	if t == nil || t.buffer == nil {
		return false, nil
	}
	b := t.buffer
	d := b.DMABUF()
	if d.Size != bounds.Size() || op.src != (render.FRect{W: float64(d.Size.X), H: float64(d.Size.Y)}) || !slices.Contains(opaqueFormats, d.Format) {
		return false, nil
	}
	shown, err := out.Scanout(d, func() error { return c.leftScreen(b) })
	if err != nil || !shown {
		return false, err
	}
	held, ok := c.scannedOut[b]
	if !ok {
		held = &scannedOut{}
		c.scannedOut[b] = held
	}
	held.outputs++
	return true, nil
}

func (c *Compositor) leftScreen(b *dmabuf.Buffer) error {
	held, ok := c.scannedOut[b]
	if !ok {
		return nil
	}
	if held.outputs--; held.outputs > 0 {
		return nil
	}
	delete(c.scannedOut, b)
	errs := make([]error, 0, len(held.then)+1)
	if held.released != nil {
		errs = append(errs, held.released.Sent(b.Release()))
	}
	for _, f := range held.then {
		errs = append(errs, f())
	}
	return errors.Join(errs...)
}

func (c *Compositor) OffScreen(b *dmabuf.Buffer, f func() error) error {
	held, ok := c.scannedOut[b]
	if !ok {
		return f()
	}
	held.then = append(held.then, f)
	return nil
}

func (c *Compositor) releaseBuffer(b *dmabuf.Buffer, client *server.Client) error {
	if held, ok := c.scannedOut[b]; ok {
		held.released = client
		return nil
	}
	return client.Sent(b.Release())
}

func (c *Compositor) scanoutBufferGone(b *dmabuf.Buffer) {
	if held, ok := c.scannedOut[b]; ok {
		held.released = nil
	}
}
