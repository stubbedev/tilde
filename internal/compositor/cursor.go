package compositor

import (
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/stubbedev/tilde/internal/backend"
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

func (c *Compositor) drawCursor(pass *frame, o *output.Output, shown []*surface.Surface) ([]*surface.Surface, error) {
	if !c.seat.HasPointer() || !image.Pt(int(math.Floor(c.cursor.X)), int(math.Floor(c.cursor.Y))).In(o.Logical()) {
		return shown, nil
	}
	cur := c.seat.Cursor()
	name := "default"
	switch {
	case cur.Set() && cur.Surface() != nil:
		at := image.Pt(int(math.Floor(c.cursor.X)), int(math.Floor(c.cursor.Y))).Sub(cur.Hotspot())
		return c.drawTree(pass, o, cur.Surface(), flat(at), shown), nil
	case cur.Set() && cur.Shape() == "":
		return shown, nil
	case cur.Set():
		name = cur.Shape()
	}
	ct, err := c.cursorTexture(name, o.Scale())
	if err != nil {
		return shown, err
	}
	tip := c.cursorTip(o)
	size := ct.tex.Size()
	pass.DrawTexture(ct.tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, image.Rectangle{Min: tip.Sub(ct.hotspot), Max: tip.Sub(ct.hotspot).Add(ct.size)}, render.Normal, 1)
	return shown, nil
}

func (c *Compositor) cursorTip(o *output.Output) image.Point {
	local := c.cursor.sub(o.Position())
	scale := float64(o.Scale()) / float64(output.Unit)
	return image.Pt(int(math.Floor(local.X*scale)), int(math.Floor(local.Y*scale)))
}

type hwCursor struct {
	plane   backend.CursorPlane
	loaded  cursorImage
	ok      bool
	inFrame bool
}

type cursorImage struct {
	gen     uint64
	scale   output.Scale
	hotspot image.Point
	size    image.Point
}

func (c *Compositor) addCursorPlane(o *output.Output) {
	plane, err := o.Backend().CursorPlane()
	switch {
	case err == nil:
		c.hwCursors[o] = &hwCursor{plane: plane}
	case !errors.Is(err, backend.ErrNoCursorPlanes):
		c.report(fmt.Errorf("compositor: drawing the cursor in the frame on %s: %w", o.Backend().Name(), err))
	}
}

func (c *Compositor) placeCursor(o *output.Output) (bool, error) {
	hw, ok := c.hwCursors[o]
	if !ok {
		return false, nil
	}
	ops := &c.cursorOps
	ops.ops = ops.ops[:0]
	var err error
	if c.cursorShown, err = c.drawCursor(ops, o, c.cursorShown[:0]); err != nil {
		return false, err
	}
	if len(ops.ops) == 0 {
		return true, hw.plane.HideCursor()
	}
	box, tip := ops.bounds(), c.cursorTip(o)
	want := cursorImage{gen: c.cursorGen, scale: o.Scale(), hotspot: tip.Sub(box.Min), size: box.Size()}
	if limit := hw.plane.CursorSize(); want.size.X > limit.X || want.size.Y > limit.Y {
		return false, hw.plane.HideCursor()
	}
	if !hw.ok || hw.loaded != want {
		if err := c.loadCursor(hw, box.Min, want.hotspot); err != nil {
			return false, c.dropCursorPlane(o, err)
		}
		hw.loaded, hw.ok = want, true
	}
	if err := hw.plane.MoveCursor(tip); err != nil {
		return false, c.dropCursorPlane(o, err)
	}
	return true, nil
}

func (c *Compositor) loadCursor(hw *hwCursor, origin, hotspot image.Point) error {
	f, err := hw.plane.BeginCursor()
	if err != nil {
		return err
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rectangle{Max: f.Target.Size()}})
	if err != nil {
		return err
	}
	p.Clear(render.Color{})
	c.cursorOps.replayFrom(p, origin)
	if err := p.End(); err != nil {
		return err
	}
	return hw.plane.SetCursor(hotspot)
}

func (c *Compositor) dropCursorPlane(o *output.Output, cause error) error {
	hw := c.hwCursors[o]
	delete(c.hwCursors, o)
	c.report(fmt.Errorf("compositor: drawing the cursor in the frame on %s: %w", o.Backend().Name(), cause))
	return hw.plane.HideCursor()
}

func (c *Compositor) cursorMoved() error {
	var errs []error
	for _, o := range c.outputs {
		placed, err := c.placeCursor(o)
		errs = append(errs, err)
		hw, ok := c.hwCursors[o]
		repaint := !placed || (ok && hw.inFrame)
		if repaint {
			errs = append(errs, c.schedule(o))
		}
		errs = append(errs, c.noteCursor(o, repaint))
	}
	return errors.Join(errs...)
}

func inTree(root, s *surface.Surface) bool {
	for member := range root.Stack() {
		if member == s || (member != root && inTree(member, s)) {
			return true
		}
	}
	return false
}

func (c *Compositor) cursorCommitted(s *surface.Surface) {
	if cur := c.seat.Cursor(); cur.Set() && cur.Surface() != nil && inTree(cur.Surface(), s) {
		c.cursorGen++
	}
}

func (c *Compositor) CursorChanged() error {
	c.cursorGen++
	return c.cursorMoved()
}

func (c *Compositor) dropPointers() error {
	c.cursorGen++
	var errs []error
	for _, ct := range c.pointers {
		errs = append(errs, ct.tex.Destroy())
	}
	clear(c.pointers)
	return errors.Join(errs...)
}
