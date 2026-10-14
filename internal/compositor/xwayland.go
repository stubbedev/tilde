package compositor

import (
	"errors"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wm"
	"github.com/stubbedev/tilde/internal/xwayland"
)

type xToplevel struct {
	*xwayland.Window
}

func (xToplevel) Popups() []*shell.Popup {
	return nil
}

func (t xToplevel) Geometry() image.Rectangle {
	return image.Rectangle{Max: t.Rect().Size()}
}

func (t xToplevel) AppID() string {
	return t.Class().Class
}

func (t xToplevel) pid() int32 {
	return t.Pid()
}

func (t xToplevel) parent() toplevel {
	if p := t.Parent(); p != nil {
		return xToplevel{p}
	}
	return nil
}

func (t xToplevel) prefersFloating() bool {
	return t.PrefersFloating()
}

func (xToplevel) scaled() bool {
	return false
}

func xState(w want) xwayland.State {
	return xwayland.State{Rect: w.rect, Activated: w.activated, Fullscreen: w.layer == wm.LayerFullscreen}
}

func (t xToplevel) settled(w want) bool {
	return t.Applied() == xState(w)
}

func (t xToplevel) apply(w want) error {
	return t.Apply(xState(w))
}

func (t xToplevel) close() error {
	return t.Close()
}

func (c *Compositor) XWindowMapped(w *xwayland.Window) error {
	if w.Override() {
		c.unmanaged = append(c.unmanaged, w)
		return errors.Join(c.refreshPointer(), c.scheduleAll())
	}
	t := xToplevel{w}
	if err := c.mapToplevel(t); err != nil {
		return err
	}
	if !w.Fullscreen() {
		return nil
	}
	return c.update(c.wm.SetFullscreen(c.windowOf(t).id, true))
}

func (c *Compositor) XWindowUnmapped(w *xwayland.Window) error {
	if i := slices.Index(c.unmanaged, w); i >= 0 {
		c.unmanaged = slices.Delete(c.unmanaged, i, i+1)
		return errors.Join(c.refreshPointer(), c.scheduleAll())
	}
	return c.unmapToplevel(xToplevel{w})
}

func (c *Compositor) XWindowChanged(w *xwayland.Window) error {
	if win := c.windowOf(xToplevel{w}); win != nil && w.Urgent() && win != c.active {
		win.urgent = true
	}
	return errors.Join(c.refreshPointer(), c.scheduleAll())
}

func (c *Compositor) XWindowRequest(w *xwayland.Window, r xwayland.Request) error {
	win := c.windowOf(xToplevel{w})
	if win == nil {
		return nil
	}
	switch r := r.(type) {
	case xwayland.Activate:
		if win != c.active {
			win.urgent = true
		}
	case xwayland.SetFullscreen:
		return c.update(c.wm.SetFullscreen(win.id, r.On))
	case xwayland.Configure:
		if !c.wm.Floats(win.id) {
			return nil
		}
		return c.update(c.wm.SetFloatingRect(win.id, r.Rect.Inset(-c.decor.BorderWidth)))
	}
	return nil
}

func (c *Compositor) drawUnmanaged(pass *frame, o *output.Output, shown []*surface.Surface) []*surface.Surface {
	for _, w := range c.unmanaged {
		if w.Rect().Overlaps(o.Logical()) {
			shown = c.drawTree(pass, o, w.Surface(), w.Rect().Min, shown)
		}
	}
	return shown
}

func (c *Compositor) hitUnmanaged(p point) (*surface.Surface, point) {
	for _, w := range slices.Backward(c.unmanaged) {
		if s, local := hitTree(w.Surface(), w.Rect().Min, p); s != nil {
			return s, local
		}
	}
	return nil, point{}
}
