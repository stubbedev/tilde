package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/output"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wm"
)

type configured struct {
	size      image.Point
	bounds    image.Point
	scale     output.Scale
	activated bool
}

type window struct {
	id       wm.WindowID
	toplevel *shell.Toplevel
	rect     image.Rectangle
	visible  bool
	output   *output.Output
	urgent   bool
	scale    output.Scale
}

func (c *Compositor) placement(w *window) image.Rectangle {
	return w.rect
}

func (c *Compositor) firstOutput() *output.Output {
	if len(c.outputs) == 0 {
		return nil
	}
	return c.outputs[0]
}

func (c *Compositor) windowOf(t *shell.Toplevel) *window {
	for _, w := range c.windows {
		if w.toplevel == t {
			return w
		}
	}
	return nil
}

func (c *Compositor) windowByID(id wm.WindowID) *window {
	for _, w := range c.windows {
		if w.id == id {
			return w
		}
	}
	return nil
}

func (c *Compositor) Toplevels() []*shell.Toplevel {
	out := make([]*shell.Toplevel, 0, len(c.windows))
	for _, w := range c.windows {
		out = append(out, w.toplevel)
	}
	return out
}

func (c *Compositor) outputAtRect(r image.Rectangle) *output.Output {
	for _, o := range c.outputs {
		if r.Min.In(o.Logical()) {
			return o
		}
	}
	return nil
}

func (c *Compositor) configure(t *shell.Toplevel, want configured) error {
	s := shell.State{Size: want.size, Bounds: want.bounds}
	if want.scale != 0 {
		if err := t.Surface().Client().Sent(t.Surface().SetPreferredScale(uint32(want.scale))); err != nil {
			return err
		}
		s.States = append(s.States,
			sxdg.XdgToplevelStateTiledLeft, sxdg.XdgToplevelStateTiledRight, sxdg.XdgToplevelStateTiledTop, sxdg.XdgToplevelStateTiledBottom,
			sxdg.XdgToplevelStateConstrainedLeft, sxdg.XdgToplevelStateConstrainedRight, sxdg.XdgToplevelStateConstrainedTop, sxdg.XdgToplevelStateConstrainedBottom)
	}
	if want.activated {
		s.States = append(s.States, sxdg.XdgToplevelStateActivated)
	}
	_, err := t.Configure(s)
	return t.Surface().Client().Sent(err)
}

func (c *Compositor) wanted(rect image.Rectangle, activated bool) configured {
	want := configured{size: rect.Size(), activated: activated}
	if o := c.outputAtRect(rect); o != nil {
		want.scale, want.bounds = o.Scale(), c.usable[o].Size()
	}
	return want
}

func (c *Compositor) update(s wm.State, err error) error {
	if err != nil {
		return err
	}
	c.wm = s
	return c.reconcile()
}

func (c *Compositor) syncAreas() error {
	s := c.wm
	for _, o := range c.outputs {
		area := c.usable[o].Add(o.Position())
		for _, have := range s.Outputs() {
			if have.Name == o.Backend().Name() && have.Area != area {
				var err error
				if s, err = s.SetArea(have.Name, area); err != nil {
					return err
				}
			}
		}
	}
	return c.update(s, nil)
}

func (c *Compositor) reconcile() error {
	focused, hasFocus := c.wm.Focused()
	var errs []error
	for _, p := range c.wm.Arrange(0).Windows {
		w := c.windowByID(p.ID)
		if w == nil {
			return fmt.Errorf("compositor: the window manager placed unknown window %d", p.ID)
		}
		moved := p.Rect != w.rect
		w.rect, w.visible = p.Rect, p.Visible
		if o := c.outputAtRect(p.Rect); o != nil {
			w.output = o
		}
		if moved && w.visible {
			errs = append(errs, c.reconstrain(w))
		}
		errs = append(errs, c.settle(w, hasFocus && focused == w.id))
	}
	var target *window
	if hasFocus {
		target = c.windowByID(focused)
	}
	if target != c.active {
		c.active = target
		if target != nil {
			c.layerFocus, target.urgent = nil, false
		}
	}
	return errors.Join(append(errs, c.refocus(), c.refreshPointer(), c.scheduleAll())...)
}

func (c *Compositor) settle(w *window, activated bool) error {
	want := c.wanted(w.rect, activated)
	r := w.toplevel.Requested()
	if r.Size == want.size && r.Bounds == want.bounds && r.Has(sxdg.XdgToplevelStateActivated) == want.activated && w.scale == want.scale {
		return nil
	}
	w.scale = want.scale
	return c.configure(w.toplevel, want)
}

func (c *Compositor) ToplevelInitialCommit(t *shell.Toplevel) error {
	s, err := c.wm.MapWindow(c.nextWindow)
	if err != nil {
		return err
	}
	for _, p := range s.Arrange(0).Windows {
		if p.ID == c.nextWindow {
			return c.configure(t, c.wanted(p.Rect, true))
		}
	}
	return fmt.Errorf("compositor: the window manager did not place new window %d", c.nextWindow)
}

func (c *Compositor) focusWindow(w *window) error {
	c.layerFocus = nil
	return c.update(c.wm.FocusWindow(w.id))
}

func (c *Compositor) keyboardTarget() *surface.Surface {
	exclusive := c.exclusiveLayer()
	if top := c.grabTop(); top != nil && (exclusive == nil || top.Client() == exclusive.Surface().Client()) {
		return top
	}
	switch {
	case exclusive != nil:
		return exclusive.Surface()
	case c.layerFocus != nil && c.layerFocus.Mapped():
		return c.layerFocus.Surface()
	case c.active != nil:
		return c.active.toplevel.Surface()
	}
	return nil
}

func (c *Compositor) refocus() error {
	return c.seat.SetKeyboardFocus(c.keyboardTarget())
}

func (c *Compositor) ToplevelMapped(t *shell.Toplevel) error {
	w := &window{id: c.nextWindow, toplevel: t}
	c.nextWindow++
	s, err := c.wm.MapWindow(w.id)
	if err != nil {
		return err
	}
	for _, p := range s.Arrange(0).Windows {
		if o := c.outputAtRect(p.Rect); p.ID == w.id && o != nil && t.Requested().Bounds != (image.Point{}) {
			w.scale = o.Scale()
		}
	}
	c.windows = append(c.windows, w)
	return c.update(s, nil)
}

func (c *Compositor) ToplevelUnmapped(t *shell.Toplevel) error {
	w := c.windowOf(t)
	if w == nil {
		return nil
	}
	c.windows = slices.DeleteFunc(c.windows, func(x *window) bool { return x == w })
	if c.active == w {
		c.active = nil
	}
	return c.update(c.wm.UnmapWindow(w.id))
}

func (c *Compositor) ToplevelCommitted(*shell.Toplevel) error { return nil }

func (c *Compositor) ToplevelChanged(*shell.Toplevel, shell.Change) error { return nil }

func (c *Compositor) ToplevelRequest(t *shell.Toplevel, r shell.Request) error {
	if _, ok := r.(shell.Activate); !ok {
		return nil
	}
	if w := c.windowOf(t); w != nil && w != c.active {
		w.urgent = true
	}
	return nil
}

func (c *Compositor) WM() wm.State {
	return c.wm
}

func (c *Compositor) Urgent(t *shell.Toplevel) bool {
	w := c.windowOf(t)
	return w != nil && w.urgent
}

func (c *Compositor) ClientResponsive(*server.Client, bool) error { return nil }
