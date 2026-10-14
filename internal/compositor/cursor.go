package compositor

import (
	"fmt"
	"image"
	"math"

	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/xcursor"
)

type cursorKey struct {
	name string
	px   int
}

type cursorTexture struct {
	tex     render.Texture
	size    image.Point
	hotspot image.Point
}

func (c *Compositor) cursorImage(name string, px int) (xcursor.Image, error) {
	if c.cursors.Theme == nil {
		return xcursor.Builtin(px), nil
	}
	img, err := c.cursors.Theme.Load(px, xcursor.Names(name)...)
	if err == nil || name == "default" {
		return img, err
	}
	return c.cursorImage("default", px)
}

func (c *Compositor) cursorTexture(name string, scale output.Scale) (*cursorTexture, error) {
	px := scale.ToDevice(c.cursors.Size)
	key := cursorKey{name, px}
	if ct, ok := c.pointers[key]; ok {
		return ct, nil
	}
	img, err := c.cursorImage(name, px)
	if err != nil {
		return nil, fmt.Errorf("compositor: cursor %q at %dpx: %w", name, px, err)
	}
	tex, err := c.renderer.NewTexture(render.ARGB8888, img.Size, img.Size.X*4, img.Pixels)
	if err != nil {
		return nil, err
	}
	nominal := max(img.Nominal, 1)
	ct := &cursorTexture{
		tex:     tex,
		size:    image.Pt(img.Size.X*px/nominal, img.Size.Y*px/nominal),
		hotspot: image.Pt(img.Hotspot.X*px/nominal, img.Hotspot.Y*px/nominal),
	}
	c.pointers[key] = ct
	return ct, nil
}

func (c *Compositor) drawCursor(pass render.Pass, o *output.Output, shown []*surface.Surface) ([]*surface.Surface, error) {
	if !c.seat.HasPointer() || !image.Pt(int(math.Floor(c.cursor.X)), int(math.Floor(c.cursor.Y))).In(o.Logical()) {
		return shown, nil
	}
	cur := c.seat.Cursor()
	name := "default"
	switch {
	case cur.Set() && cur.Surface() != nil:
		at := image.Pt(int(math.Floor(c.cursor.X)), int(math.Floor(c.cursor.Y))).Sub(cur.Hotspot())
		return c.drawTree(pass, o, cur.Surface(), at, shown), nil
	case cur.Set() && cur.Shape() == "":
		return shown, nil
	case cur.Set():
		name = cur.Shape()
	}
	ct, err := c.cursorTexture(name, o.Scale())
	if err != nil {
		return shown, err
	}
	local := c.cursor.sub(o.Position())
	scale := float64(o.Scale()) / float64(output.Unit)
	tip := image.Pt(int(math.Floor(local.X*scale)), int(math.Floor(local.Y*scale)))
	size := ct.tex.Size()
	pass.DrawTexture(ct.tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, image.Rectangle{Min: tip.Sub(ct.hotspot), Max: tip.Sub(ct.hotspot).Add(ct.size)}, render.Normal, 1)
	return shown, nil
}

func (c *Compositor) CursorChanged() error {
	return c.scheduleAll()
}

func (c *Compositor) dropPointers() {
	for _, ct := range c.pointers {
		ct.tex.Destroy()
	}
	clear(c.pointers)
}
