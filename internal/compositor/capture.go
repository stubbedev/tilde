package compositor

import (
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/shm"
)

type capture struct {
	output *output.Output
	region image.Rectangle
	cursor bool
	buffer *shm.Buffer
	done   func(at time.Duration, err error) error
}

func (c *Compositor) queueCapture(k capture, waitForDamage bool) error {
	sc := c.sceneOf(k.output)
	if sc.drawn && !waitForDamage {
		return c.runCapture(k)
	}
	c.captures[k.output] = append(c.captures[k.output], k)
	return c.schedule(k.output)
}

func (c *Compositor) runCaptures(o *output.Output) error {
	pending := c.captures[o]
	if len(pending) == 0 {
		return nil
	}
	delete(c.captures, o)
	errs := make([]error, 0, len(pending))
	for _, k := range pending {
		errs = append(errs, c.runCapture(k))
	}
	return errors.Join(errs...)
}

func (c *Compositor) dropCaptures(o *output.Output) error {
	pending := c.captures[o]
	delete(c.captures, o)
	errs := make([]error, 0, len(pending))
	for _, k := range pending {
		errs = append(errs, k.done(0, fmt.Errorf("compositor: %s went away before the capture", o.Backend().Name())))
	}
	return errors.Join(errs...)
}

func (c *Compositor) runCapture(k capture) error {
	err := c.captureInto(k)
	return k.done(backend.Now(), err)
}

func (c *Compositor) captureInto(k capture) (err error) {
	size := k.region.Size()
	target, err := c.renderer.NewTarget(size)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, target.Destroy()) }()
	p, err := c.renderer.Begin(target, render.Region{image.Rectangle{Max: size}})
	if err != nil {
		return err
	}
	p.Clear(render.Color{A: 0xff})
	last := c.sceneOf(k.output).prev
	(&frame{ops: last.ops[:last.cursorFrom]}).replayFrom(p, k.region.Min)
	if k.cursor {
		cursor := frame{ops: last.ops[last.cursorFrom:]}
		if len(cursor.ops) == 0 {
			if _, err := c.drawCursor(&cursor, k.output, nil); err != nil {
				return errors.Join(err, p.End())
			}
		}
		cursor.replayFrom(p, k.region.Min)
	}
	if err := p.End(); err != nil {
		return err
	}
	return k.buffer.Write(func(data []byte, stride int) error { return target.Read(data, stride) })
}

func (c *Compositor) captureRegion(o *output.Output, logical image.Rectangle) image.Rectangle {
	bounds := image.Rectangle{Max: o.Backend().State().Mode.Size}
	if logical == (image.Rectangle{}) {
		return bounds
	}
	return o.Scale().Rect(logical).Intersect(bounds)
}

func validCaptureBuffer(b *shm.Buffer, size image.Point, formats []render.Format) error {
	switch {
	case b == nil:
		return errors.New("not a shared-memory buffer")
	case b.Size() != size:
		return fmt.Errorf("the buffer is %v, the capture is %v", b.Size(), size)
	}
	for _, f := range formats {
		if b.Format() == f {
			return nil
		}
	}
	return fmt.Errorf("format %d cannot hold a capture", b.Format())
}
