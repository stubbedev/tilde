package compositor

import (
	"errors"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/dmabuf"
	"github.com/stubbedev/tilde/internal/fourcc"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
)

type scannedOut struct {
	outputs  int
	released *server.Client
	then     []func() error
}

var opaqueFormats = []fourcc.Format{fourcc.XRGB8888, fourcc.XBGR8888}

type filling struct {
	surface *surface.Surface
	out     backend.Scanner
}

func (c *Compositor) scanout(o *output.Output, sc *scene, bounds image.Rectangle) (bool, error) {
	out, ok := o.Backend().(backend.Scanner)
	if !ok {
		return false, nil
	}
	op, alone := fills(sc.cur.ops[:sc.cur.cursorFrom], bounds)
	if err := c.fill(o, op.surface, out); err != nil || !alone || sc.cur.cursorFrom != len(sc.cur.ops) {
		return false, err
	}
	t, ok := c.textures[op.surface].(imported)
	if !ok {
		return false, nil
	}
	b := t.buffer
	d := b.DMABUF()
	if d.Size != bounds.Size() || op.src != (render.FRect{W: float64(d.Size.X), H: float64(d.Size.Y)}) || !slices.Contains(opaqueFormats, d.Format) {
		return false, nil
	}
	shown, err := out.Scanout(d, func() error { return c.leftScreen(b) }, c.tears(op.surface))
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

func fills(ops []drawOp, bounds image.Rectangle) (drawOp, bool) {
	if len(ops) != 2 || !ops[0].fill {
		return drawOp{}, false
	}
	op := ops[1]
	if op.surface == nil || op.dst != bounds || op.transform != render.Normal || op.alpha != 1 {
		return drawOp{}, false
	}
	return op, true
}

func (c *Compositor) fill(o *output.Output, s *surface.Surface, out backend.Scanner) error {
	prev := c.filled[o].surface
	if prev == s {
		return nil
	}
	if s == nil {
		delete(c.filled, o)
	} else {
		c.filled[o] = filling{surface: s, out: out}
	}
	if c.dmabuf == nil {
		return nil
	}
	var err error
	for _, changed := range []*surface.Surface{prev, s} {
		if changed != nil {
			err = errors.Join(err, c.dmabuf.ScanoutChanged(changed))
		}
	}
	return err
}

func (c *Compositor) SurfaceScanout(s *surface.Surface) gpu.ScanoutTarget {
	for _, o := range c.outputs {
		if f, ok := c.filled[o]; ok && f.surface == s {
			t := f.out.ScanoutFormats()
			t.Formats = slices.DeleteFunc(slices.Clone(t.Formats), func(f gpu.FormatModifier) bool { return !slices.Contains(opaqueFormats, f.Format) })
			return t
		}
	}
	return gpu.ScanoutTarget{}
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
		errs = append(errs, b.Release())
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
	return b.Release()
}

func (c *Compositor) scanoutBufferGone(b *dmabuf.Buffer) {
	if held, ok := c.scannedOut[b]; ok {
		held.released = nil
	}
}
