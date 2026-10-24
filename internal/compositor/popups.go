package compositor

import (
	"errors"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type placedPopup struct {
	popup  *shell.Popup
	origin image.Point
}

func (p placedPopup) bounds() image.Rectangle {
	return image.Rectangle{Min: p.origin, Max: p.origin.Add(p.popup.Surface().Size())}
}

func (c *Compositor) popups(w *window) []placedPopup {
	if !w.visible {
		return nil
	}
	return c.popupTree(w.top.Popups(), w.rect.Min)
}

func (c *Compositor) popupTree(roots []*shell.Popup, origin image.Point) []placedPopup {
	var out []placedPopup
	var walk func(ps []*shell.Popup, parent image.Point)
	walk = func(ps []*shell.Popup, parent image.Point) {
		for _, p := range ps {
			if !p.Mapped() {
				continue
			}
			geometry := parent.Add(p.Box().Min)
			out = append(out, placedPopup{p, geometry.Sub(p.Geometry().Min)})
			walk(p.Popups(), geometry)
		}
	}
	walk(roots, origin)
	return out
}

func (c *Compositor) PopupConstraint(root shell.PopupRoot) image.Rectangle {
	switch r := root.(type) {
	case *shell.Toplevel:
		if w := c.windowOf(xdgToplevel{r}); w != nil && w.visible && w.output != nil {
			return w.output.Logical().Sub(w.rect.Min)
		}
	case *shell.LayerSurface:
		if e := c.layerOf(r); e != nil && e.placed {
			return e.output.Logical().Sub(e.bounds().Min)
		}
	}
	return image.Rectangle{}
}

func (c *Compositor) PopupMapped(*shell.Popup) error {
	return errors.Join(c.refreshPointer(), c.scheduleAll())
}

func (c *Compositor) PopupUnmapped(*shell.Popup) error {
	return errors.Join(c.refreshPointer(), c.scheduleAll())
}

func (c *Compositor) UserActive(client *server.Client, serial uint32) bool {
	if c.seat.PressSerial(client, serial) {
		return true
	}
	for _, s := range []*surface.Surface{c.seat.PointerFocus(), c.seat.Focus()} {
		if s != nil && s.Client() == client {
			return true
		}
	}
	return false
}

func (c *Compositor) GrabChanged() error {
	return errors.Join(c.refocus(), c.refreshPointer())
}

func (c *Compositor) grabClient() *server.Client {
	if g := c.shell.Grab(); len(g) > 0 {
		return g[0].Surface().Client()
	}
	return nil
}

func (c *Compositor) grabTop() *surface.Surface {
	for _, p := range slices.Backward(c.shell.Grab()) {
		if p.Mapped() {
			return p.Surface()
		}
	}
	return nil
}

func (c *Compositor) outsideGrab(s *surface.Surface) bool {
	g := c.grabClient()
	return g != nil && (s == nil || s.Client() != g)
}

func (c *Compositor) reconstrain(w *window) error {
	var errs []error
	for _, p := range c.popups(w) {
		errs = append(errs, p.popup.Reconstrain())
	}
	return errors.Join(errs...)
}
