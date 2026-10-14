package headless

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/soft"
)

type Cursor struct {
	o                *Output
	target           render.ReadTarget
	image            *soft.Buffer
	hotspot, at      image.Point
	visible, drawing bool
	images, moves    uint64
	refused          error
}

func (b *Backend) SetCursorPlanes(size image.Point) error {
	if len(b.outputs) > 0 {
		return errors.New("headless: cursor planes are set before the first output")
	}
	if size.X <= 0 || size.Y <= 0 {
		return fmt.Errorf("headless: cursor planes of %v", size)
	}
	b.cursorSize = size
	return nil
}

func (o *Output) CursorPlane() (backend.CursorPlane, error) {
	if o.cursor == nil {
		return nil, backend.ErrNoCursorPlanes
	}
	return o.cursor, nil
}

func (o *Output) Cursor() *Cursor {
	return o.cursor
}

func (c *Cursor) CursorSize() image.Point {
	return c.o.backend.cursorSize
}

func (c *Cursor) Refuse(err error) {
	c.refused, c.visible = err, false
}

func (c *Cursor) BeginCursor() (backend.Frame, error) {
	if c.refused != nil {
		return backend.Frame{}, c.refused
	}
	if c.drawing {
		return backend.Frame{}, fmt.Errorf("headless: %s already has a cursor image in progress", c.o.name)
	}
	if c.target == nil {
		t, err := c.o.backend.renderer.NewTarget(c.CursorSize())
		if err != nil {
			return backend.Frame{}, fmt.Errorf("headless: cursor target for %s: %w", c.o.name, err)
		}
		c.target = t
	}
	c.drawing = true
	return backend.Frame{Renderer: c.o.backend.renderer, Target: c.target}, nil
}

func (c *Cursor) SetCursor(hotspot image.Point) error {
	if !c.drawing {
		return fmt.Errorf("headless: SetCursor on %s without BeginCursor", c.o.name)
	}
	c.drawing = false
	size := c.CursorSize()
	img := soft.NewBuffer(size.X, size.Y)
	if err := c.target.Read(img.Pix, img.Stride); err != nil {
		return fmt.Errorf("headless: read the cursor image of %s: %w", c.o.name, err)
	}
	c.image, c.hotspot, c.visible = img, hotspot, true
	c.images++
	return nil
}

func (c *Cursor) MoveCursor(at image.Point) error {
	if c.refused != nil {
		return c.refused
	}
	if at != c.at {
		c.at = at
		c.moves++
	}
	return nil
}

func (c *Cursor) HideCursor() error {
	c.visible = false
	return nil
}

func (c *Cursor) Visible() bool {
	return c.visible
}

func (c *Cursor) Position() image.Point {
	return c.at
}

func (c *Cursor) Images() uint64 {
	return c.images
}

func (c *Cursor) Moves() uint64 {
	return c.moves
}

func (c *Cursor) composite(dst *soft.Buffer) {
	if !c.visible || c.image == nil {
		return
	}
	origin := c.at.Sub(c.hotspot)
	area := image.Rectangle{Min: origin, Max: origin.Add(c.image.Size())}.Intersect(image.Rect(0, 0, dst.W, dst.H))
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			s := c.image.Pix[(y-origin.Y)*c.image.Stride+(x-origin.X)*4:]
			d := dst.Pix[y*dst.Stride+x*4:]
			inv := uint32(0xff - s[3])
			for i := range 4 {
				d[i] = s[i] + byte((uint32(d[i])*inv+127)/255) //nolint:gosec // a product of two bytes over 255 fits a byte
			}
		}
	}
}

func (c *Cursor) destroy() error {
	if c.target == nil {
		return nil
	}
	err := c.target.Destroy()
	c.target = nil
	return err
}
