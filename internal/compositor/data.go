package compositor

import (
	"errors"
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
	return errors.Join(c.data.SetFocus(client), c.refreshShortcutsInhibitor(), c.refreshConstraint(), c.inputMethods.Focus(s))
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

type dragging struct {
	*data.Drag
	by dragInput
}

type dragInput interface {
	at(c *Compositor) point
}

type pointerInput struct{}

type touchInput struct{ id int32 }

func (pointerInput) at(c *Compositor) point { return c.cursor }

func (t touchInput) at(c *Compositor) point { return c.touches[t.id].at }

type touchAt struct {
	output *output.Output
	at     point
}

func (c *Compositor) CanDrag(origin *surface.Surface, serial uint32) bool {
	if c.drag != nil {
		return false
	}
	if _, ok := c.seat.TouchBySerial(origin, serial); ok {
		return true
	}
	return c.seat.PointerGrabbed() && c.seat.PointerFocus() == origin
}

func (c *Compositor) DragStarted(d *data.Drag) error {
	if id, ok := c.seat.TouchBySerial(d.Origin(), d.Serial()); ok {
		c.drag = &dragging{d, touchInput{id}}
		return errors.Join(c.dragMotion(backend.Now()), c.scheduleAll())
	}
	c.drag = &dragging{d, pointerInput{}}
	return errors.Join(c.seat.SetPointerFocus(nil, 0, 0), c.seat.PointerFrame(), c.dragMotion(backend.Now()), c.scheduleAll())
}

func (c *Compositor) DragEnded(d *data.Drag) error {
	if c.drag == nil || c.drag.Drag != d {
		return nil
	}
	c.drag = nil
	return errors.Join(c.refreshPointer(), c.seat.PointerFrame(), c.scheduleAll())
}

func (c *Compositor) pointerDrag() bool {
	if c.drag == nil {
		return false
	}
	_, ok := c.drag.by.(pointerInput)
	return ok
}

func (c *Compositor) touchDrag(id int32) bool {
	if c.drag == nil {
		return false
	}
	t, ok := c.drag.by.(touchInput)
	return ok && t.id == id
}

func (c *Compositor) touchDragging() bool {
	if c.drag == nil {
		return false
	}
	_, ok := c.drag.by.(touchInput)
	return ok
}

func (c *Compositor) dragMotion(at time.Duration) error {
	s, local, _ := c.surfaceAt(c.drag.by.at(c))
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
	at := c.drag.by.at(c).pixel().Add(offset)
	return c.drawTree(pass, o, icon, flat(at), shown)
}
