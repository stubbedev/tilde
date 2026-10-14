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

type window struct {
	id       wm.WindowID
	toplevel *shell.Toplevel
	tile     image.Rectangle
	rect     image.Rectangle
	visible  bool
	layer    wm.PlacementLayer
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

func (c *Compositor) wanted(rect image.Rectangle, layer wm.PlacementLayer, activated bool) (shell.State, output.Scale) {
	want := shell.State{Size: rect.Size()}
	o := c.outputAtRect(rect)
	var scale output.Scale
	if o != nil {
		scale, want.Bounds = o.Scale(), c.usable[o].Size()
	}
	switch {
	case o == nil:
	case layer == wm.LayerTiled:
		want.States = append(want.States,
			sxdg.XdgToplevelStateTiledLeft, sxdg.XdgToplevelStateTiledRight, sxdg.XdgToplevelStateTiledTop, sxdg.XdgToplevelStateTiledBottom,
			sxdg.XdgToplevelStateConstrainedLeft, sxdg.XdgToplevelStateConstrainedRight, sxdg.XdgToplevelStateConstrainedTop, sxdg.XdgToplevelStateConstrainedBottom)
	case layer == wm.LayerFullscreen:
		want.States = append(want.States, sxdg.XdgToplevelStateFullscreen)
	}
	if activated {
		want.States = append(want.States, sxdg.XdgToplevelStateActivated)
	}
	return want, scale
}

func (c *Compositor) configure(t *shell.Toplevel, want shell.State, scale output.Scale) error {
	if scale != 0 {
		if err := t.Surface().Client().Sent(t.Surface().SetPreferredScale(uint32(scale))); err != nil {
			return err
		}
	}
	_, err := t.Configure(want)
	return t.Surface().Client().Sent(err)
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
		full, area := o.Logical(), c.usable[o].Add(o.Position())
		for _, have := range s.Outputs() {
			if have.Name == o.Backend().Name() && (have.Area != area || have.Full != full) {
				var err error
				if s, err = s.SetGeometry(have.Name, full, area); err != nil {
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
	a := c.wm.Arrange(c.decor.reserved())
	c.tabBars = a.TabBars
	c.stack = c.stack[:0]
	for _, p := range a.Windows {
		w := c.windowByID(p.ID)
		if w == nil {
			return fmt.Errorf("compositor: the window manager placed unknown window %d", p.ID)
		}
		c.stack = append(c.stack, w)
		rect := c.content(p.Rect, p.Layer)
		moved := rect != w.rect
		w.tile, w.rect, w.visible, w.layer = p.Rect, rect, p.Visible, p.Layer
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

func (c *Compositor) content(tile image.Rectangle, layer wm.PlacementLayer) image.Rectangle {
	if layer == wm.LayerFullscreen {
		return tile
	}
	return tile.Inset(c.decor.BorderWidth)
}

func sameState(a, b shell.State) bool {
	return a.Size == b.Size && a.Bounds == b.Bounds && slices.Equal(a.States, b.States)
}

func (c *Compositor) settle(w *window, activated bool) error {
	want, scale := c.wanted(w.rect, w.layer, activated)
	if sameState(w.toplevel.Requested(), want) && w.scale == scale {
		return nil
	}
	w.scale = scale
	return c.configure(w.toplevel, want, scale)
}

func (c *Compositor) ToplevelInitialCommit(t *shell.Toplevel) error {
	if t.Parent() != nil {
		return c.configure(t, shell.State{States: []shell.ToplevelState{sxdg.XdgToplevelStateActivated}}, 0)
	}
	s, err := c.wm.MapWindow(c.nextWindow, c.settings.Autotile)
	if err != nil {
		return err
	}
	for _, p := range s.Arrange(c.decor.reserved()).Windows {
		if p.ID == c.nextWindow {
			want, scale := c.wanted(c.content(p.Rect, p.Layer), p.Layer, true)
			return c.configure(t, want, scale)
		}
	}
	return fmt.Errorf("compositor: the window manager did not place new window %d", c.nextWindow)
}

func (c *Compositor) transientRect(t *shell.Toplevel) image.Rectangle {
	size := t.Geometry().Size().Add(image.Pt(2*c.decor.BorderWidth, 2*c.decor.BorderWidth))
	over := image.Rectangle{}
	if p := c.windowOf(t.Parent()); p != nil {
		over = p.tile
	} else if o := c.outputAtRect(image.Rectangle{Min: image.Pt(int(c.cursor.X), int(c.cursor.Y))}); o != nil {
		over = c.usable[o].Add(o.Position())
	}
	corner := over.Min.Add(over.Size().Sub(size).Div(2))
	return image.Rectangle{Min: corner, Max: corner.Add(size)}
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
	var s wm.State
	var err error
	if t.Parent() != nil {
		s, err = c.wm.MapFloating(w.id, c.transientRect(t))
	} else {
		s, err = c.wm.MapWindow(w.id, c.settings.Autotile)
	}
	if err != nil {
		return err
	}
	for _, p := range s.Arrange(c.decor.reserved()).Windows {
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
	if c.interaction != nil && c.interaction.w == w {
		c.interaction = nil
	}
	return c.update(c.wm.UnmapWindow(w.id))
}

func (c *Compositor) ToplevelCommitted(*shell.Toplevel) error { return nil }

func (c *Compositor) ToplevelChanged(*shell.Toplevel, shell.Change) error { return nil }

func (c *Compositor) ToplevelRequest(t *shell.Toplevel, r shell.Request) error {
	w := c.windowOf(t)
	if w == nil {
		return nil
	}
	switch r := r.(type) {
	case shell.Activate:
		if w != c.active {
			w.urgent = true
		}
	case shell.SetFullscreen:
		return c.update(c.wm.SetFullscreen(w.id, r.On))
	case shell.SetMaximized:
		_, err := t.Configure(t.Requested())
		return t.Surface().Client().Sent(err)
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
