package drm

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	kdrm "github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/render"
)

type cursorBuffer struct {
	dumb kdrm.Dumb
	fb   uint32
}

type cursor struct {
	o                       *Output
	plane                   *plane
	target                  render.ReadTarget
	buffers                 []*cursorBuffer
	pending, queued, front  *cursorBuffer
	hotspot, at             image.Point
	drawing, visible, dirty bool
	refused                 error
}

func (o *Output) CursorPlane() (backend.CursorPlane, error) {
	if o.cursor == nil {
		return nil, fmt.Errorf("drm: %s has no free cursor plane that scans out linear ARGB8888", o.Name())
	}
	return o.cursor, nil
}

func (c *cursor) CursorSize() image.Point {
	return c.o.b.cursor
}

func (c *cursor) BeginCursor() (backend.Frame, error) {
	switch {
	case c.refused != nil:
		return backend.Frame{}, c.refused
	case c.drawing:
		return backend.Frame{}, fmt.Errorf("drm: %s already has a cursor image in progress", c.o.Name())
	}
	if err := c.allocate(); err != nil {
		return backend.Frame{}, err
	}
	c.drawing = true
	return backend.Frame{Renderer: c.o.b.alloc.renderer(), Target: c.target}, nil
}

func (c *cursor) allocate() error {
	b := c.o.b
	if c.target == nil {
		t, err := b.alloc.offscreen(b.cursor)
		if err != nil {
			return fmt.Errorf("drm: cursor target for %s: %w", c.o.Name(), err)
		}
		c.target = t
	}
	for len(c.buffers) < cursorBuffers {
		dumb, err := b.dev.CreateDumb(b.cursor)
		if err != nil {
			return fmt.Errorf("drm: cursor buffer for %s: %w", c.o.Name(), err)
		}
		fb, err := b.dev.AddFB2(kdrm.FB{Width: uint32(b.cursor.X), Height: uint32(b.cursor.Y), Format: cursorFormat, Handles: [4]uint32{dumb.Handle}, Pitches: [4]uint32{dumb.Pitch}}) //nolint:gosec // cursor sizes fit
		if err != nil {
			return errors.Join(fmt.Errorf("drm: cursor framebuffer for %s: %w", c.o.Name(), err), b.dev.DestroyDumb(dumb))
		}
		c.buffers = append(c.buffers, &cursorBuffer{dumb: dumb, fb: fb})
	}
	return nil
}

func (c *cursor) SetCursor(hotspot image.Point) error {
	if !c.drawing {
		return fmt.Errorf("drm: SetCursor on %s without BeginCursor", c.o.Name())
	}
	c.drawing = false
	idle := func(b *cursorBuffer) bool { return b != c.front && b != c.queued }
	i := slices.IndexFunc(c.buffers, func(b *cursorBuffer) bool { return idle(b) && b != c.pending })
	if i < 0 {
		i = slices.IndexFunc(c.buffers, idle)
	}
	b := c.buffers[i]
	if err := c.target.Read(b.dumb.Pix, int(b.dumb.Pitch)); err != nil {
		return fmt.Errorf("drm: copy the cursor image of %s: %w", c.o.Name(), err)
	}
	c.pending, c.hotspot, c.visible, c.dirty = b, hotspot, true, true
	if err := c.test(); err != nil {
		return err
	}
	return c.o.flushCursor()
}

func (c *cursor) test() error {
	o := c.o
	if !o.lit || o.off || o.b.paused {
		return nil
	}
	o.b.req.Reset()
	c.add()
	if err := o.b.dev.Commit(&o.b.req, kdrm.AtomicTestOnly, 0); err != nil {
		c.refused, c.visible = fmt.Errorf("drm: the cursor plane of %s refuses a %v image: %w", o.Name(), o.b.cursor, err), false
		return c.refused
	}
	return nil
}

func (c *cursor) MoveCursor(at image.Point) error {
	switch {
	case c.refused != nil:
		return c.refused
	case at == c.at:
		return nil
	}
	c.at = at
	if !c.visible {
		return nil
	}
	c.dirty = true
	return c.o.flushCursor()
}

func (c *cursor) HideCursor() error {
	if !c.visible {
		return nil
	}
	c.visible, c.dirty = false, true
	return c.o.flushCursor()
}

func (c *cursor) add() {
	o, p := c.o, c.plane
	if !c.visible {
		o.add(p.id, p.props, "FB_ID", 0)
		o.add(p.id, p.props, "CRTC_ID", 0)
		return
	}
	at := c.at.Sub(c.hotspot)
	o.place(p, c.pending.fb, image.Rectangle{Min: at, Max: at.Add(o.b.cursor)}, -1)
}

func (c *cursor) sent() {
	c.queued, c.dirty = nil, false
	if c.visible {
		c.queued = c.pending
	}
}

func (c *cursor) release() error {
	errs := make([]error, 0, 2*len(c.buffers)+1)
	for _, b := range c.buffers {
		errs = append(errs, c.o.b.dev.RemoveFB(b.fb), c.o.b.dev.DestroyDumb(b.dumb))
	}
	if c.target != nil {
		errs = append(errs, c.target.Destroy())
	}
	c.plane.taken = false
	c.target, c.buffers, c.pending, c.queued, c.front = nil, nil, nil, nil, nil
	return errors.Join(errs...)
}
