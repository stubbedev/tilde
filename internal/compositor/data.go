package compositor

import (
	"errors"
	"image"
	"math"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/data"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
)

func (c *Compositor) KeyboardFocusChanged(s *surface.Surface) error {
	var client *server.Client
	if s != nil {
		client = s.Client()
	}
	return errors.Join(c.data.SetFocus(client), c.refreshShortcutsInhibitor(), c.refreshConstraint())
}

func (c *Compositor) PointerFocusChanged(*surface.Surface) error {
	return c.refreshConstraint()
}

func (c *Compositor) Selection(k data.Kind) bool {
	return c.data.Selection(k) != nil
}

func (c *Compositor) WatchSelection(f func(data.Kind, data.Source) error) {
	c.selectionWatch = f
}

func (c *Compositor) SelectionChanged(k data.Kind, src data.Source) error {
	if c.selectionWatch == nil {
		return nil
	}
	return c.selectionWatch(k, src)
}

func (c *Compositor) CurrentSelection(k data.Kind) data.Source {
	return c.data.Selection(k)
}

func (c *Compositor) SetSelection(k data.Kind, src data.Source) error {
	return c.data.SetSelection(k, src)
}

func (c *Compositor) CanDrag(origin *surface.Surface, _ uint32) bool {
	return c.drag == nil && c.seat.PointerGrabbed() && c.seat.PointerFocus() == origin
}

func (c *Compositor) DragStarted(d *data.Drag) error {
	c.drag = d
	return errors.Join(c.seat.SetPointerFocus(nil, 0, 0), c.seat.PointerFrame(), c.dragMotion(backend.Now()), c.scheduleAll())
}

func (c *Compositor) DragEnded(d *data.Drag) error {
	if c.drag != d {
		return nil
	}
	c.drag = nil
	return errors.Join(c.refreshPointer(), c.seat.PointerFrame(), c.scheduleAll())
}

func (c *Compositor) dragMotion(at time.Duration) error {
	s, local, _ := c.surfaceAt(c.cursor)
	return c.drag.Motion(s, local.X, local.Y, at)
}

func (c *Compositor) drawDragIcon(pass *frame, o *output.Output, shown []*surface.Surface) []*surface.Surface {
	if c.drag == nil {
		return shown
	}
	icon, offset := c.drag.Icon()
	if icon == nil {
		return shown
	}
	at := image.Pt(int(math.Floor(c.cursor.X)), int(math.Floor(c.cursor.Y))).Add(offset)
	return c.drawTree(pass, o, icon, flat(at), shown)
}
