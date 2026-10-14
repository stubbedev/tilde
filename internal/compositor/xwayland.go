package compositor

import (
	"errors"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wm"
	"github.com/stubbedev/tilde/internal/xwayland"
)

type xToplevel struct {
	*xwayland.Window
	c *Compositor
}

func (xToplevel) Popups() []*shell.Popup {
	return nil
}

func (t xToplevel) Geometry() image.Rectangle {
	r, _ := t.c.fromX(t.Rect())
	return image.Rectangle{Max: r.Size()}
}

func (t xToplevel) AppID() string {
	return t.Class().Class
}

func (t xToplevel) pid() int32 {
	return t.Pid()
}

func (t xToplevel) parent() toplevel {
	if p := t.Parent(); p != nil {
		return xToplevel{p, t.c}
	}
	return nil
}

func (t xToplevel) prefersFloating() bool {
	return t.PrefersFloating()
}

func (xToplevel) scaled() bool {
	return false
}

func (t xToplevel) state(w want) xwayland.State {
	return xwayland.State{Rect: t.c.toX(w.rect), Activated: w.activated, Fullscreen: w.layer == wm.LayerFullscreen}
}

func (t xToplevel) settled(w want) bool {
	return t.Applied() == t.state(w)
}

func (t xToplevel) apply(w want) error {
	return t.Apply(t.state(w))
}

func (c *Compositor) ScaleXwayland(clients func(*server.Client) bool) error {
	c.xwaylandClients = clients
	return errors.Join(c.layout.Physical(clients), c.reconcile())
}

func (c *Compositor) xScale(o *output.Output) output.Scale {
	if c.xwaylandClients == nil || o == nil {
		return output.Unit
	}
	return o.Scale()
}

func (c *Compositor) toX(r image.Rectangle) image.Rectangle {
	o := c.outputAtRect(r)
	if c.xwaylandClients == nil || o == nil {
		return r
	}
	k := o.Scale()
	at := o.XPosition().Add(k.DevicePoint(r.Min.Sub(o.Position())))
	return image.Rectangle{Min: at, Max: at.Add(k.DevicePoint(r.Size()))}
}

func (c *Compositor) fromX(r image.Rectangle) (image.Rectangle, output.Scale) {
	if c.xwaylandClients == nil {
		return r, output.Unit
	}
	for _, o := range c.outputs {
		if !r.Min.In(o.XRect()) {
			continue
		}
		k := o.Scale()
		at := o.Position().Add(k.LogicalPoint(r.Min.Sub(o.XPosition())))
		return image.Rectangle{Min: at, Max: at.Add(k.LogicalPoint(r.Size()))}, k
	}
	return r, output.Unit
}

func (c *Compositor) unmanagedOrigin(w *xwayland.Window) origin {
	r, k := c.fromX(w.Rect())
	return origin{at: r.Min, k: k}
}

func (t xToplevel) close() error {
	return t.Close()
}

func (c *Compositor) XWindowMapped(w *xwayland.Window) error {
	if w.Override() {
		c.unmanaged = append(c.unmanaged, w)
		return errors.Join(c.refreshPointer(), c.scheduleAll())
	}
	t := xToplevel{w, c}
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
	return c.unmapToplevel(xToplevel{w, c})
}

func (c *Compositor) XWindowChanged(w *xwayland.Window) error {
	if win := c.windowOf(xToplevel{w, c}); win != nil && w.Urgent() && win != c.active {
		win.urgent = true
	}
	return errors.Join(c.refreshPointer(), c.scheduleAll(), c.syncForeign())
}

func (c *Compositor) XWindowRequest(w *xwayland.Window, r xwayland.Request) error {
	win := c.windowOf(xToplevel{w, c})
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
		rect, _ := c.fromX(r.Rect)
		return c.update(c.wm.SetFloatingRect(win.id, rect.Inset(-c.decor.BorderWidth)))
	}
	return nil
}

func (c *Compositor) drawUnmanaged(pass *frame, o *output.Output, shown []*surface.Surface) []*surface.Surface {
	for _, w := range c.unmanaged {
		if r, _ := c.fromX(w.Rect()); r.Overlaps(o.Logical()) {
			shown = c.drawTree(pass, o, w.Surface(), c.unmanagedOrigin(w), shown)
		}
	}
	return shown
}

func (c *Compositor) hitUnmanaged(p point) (*surface.Surface, point) {
	for _, w := range slices.Backward(c.unmanaged) {
		if s, local := hitTree(w.Surface(), c.unmanagedOrigin(w), p); s != nil {
			return s, local
		}
	}
	return nil, point{}
}
