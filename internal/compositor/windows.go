package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wm"
)

type window struct {
	id      wm.WindowID
	top     toplevel
	tile    image.Rectangle
	rect    image.Rectangle
	visible bool
	layer   wm.PlacementLayer
	output  *output.Output
	urgent  bool
	hung    bool
	scale   output.Scale
}

func (c *Compositor) windowOrigin(w *window) origin {
	at := w.rect.Min.Sub(w.top.Geometry().Min)
	if _, ok := w.top.(xToplevel); ok {
		return origin{at: at, k: c.xScale(w.output)}
	}
	return flat(at)
}

func (c *Compositor) firstOutput() *output.Output {
	if len(c.outputs) == 0 {
		return nil
	}
	return c.outputs[0]
}

func (c *Compositor) windowOf(t toplevel) *window {
	for _, w := range c.windows {
		if w.top == t {
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
		if t, ok := w.top.(xdgToplevel); ok {
			out = append(out, t.Toplevel)
		}
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

func (c *Compositor) wanted(rect image.Rectangle, layer wm.PlacementLayer, activated bool) want {
	w := want{rect: rect, layer: layer, activated: activated}
	if o := c.outputAtRect(rect); o != nil {
		w.placed, w.scale, w.bounds = true, c.scaleOver(rect), c.usable[o].Size()
	}
	return w
}

func maxScale(outputs []*output.Output) output.Scale {
	var s output.Scale
	for _, o := range outputs {
		s = max(s, o.Scale())
	}
	return s
}

func (c *Compositor) scaleOver(r image.Rectangle) output.Scale {
	var covered []*output.Output
	for _, o := range c.outputs {
		if o.Logical().Overlaps(r) {
			covered = append(covered, o)
		}
	}
	return maxScale(covered)
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

func (c *Compositor) modalChild(w *window) *window {
	for _, x := range slices.Backward(c.windows) {
		if x != w && x.top.modal() && x.top.parent() == w.top {
			if deeper := c.modalChild(x); deeper != nil {
				return deeper
			}
			return x
		}
	}
	return nil
}

func (c *Compositor) reconcile() error {
	focused, hasFocus := c.wm.Focused()
	if hasFocus {
		if m := c.modalChild(c.windowByID(focused)); m != nil {
			return c.update(c.wm.FocusWindow(m.id))
		}
	}
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
			errs = append(errs, target.top.ping())
		}
	}
	return errors.Join(append(errs, c.refocus(), c.refreshPointer(), c.scheduleAll(), c.syncForeign(), c.syncWorkspaces(), c.syncCaptureSessions())...)
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
	want := c.wanted(w.rect, w.layer, activated)
	if w.top.settled(want) && w.scale == want.scale {
		return nil
	}
	w.scale = want.scale
	return w.top.apply(want)
}

func (c *Compositor) floats(t toplevel) (bool, image.Point) {
	d := wm.Decide(c.rules, t.AppID(), t.Title())
	return d.Mode == wm.RuleFloat || ((t.parent() != nil || t.prefersFloating()) && d.Mode != wm.RuleTile), d.Size
}

func (c *Compositor) ToplevelInitialCommit(st *shell.Toplevel) error {
	t := xdgToplevel{st}
	if float, size := c.floats(t); float {
		return t.apply(want{rect: image.Rectangle{Max: floatSize(t, size)}, activated: true})
	}
	s, err := c.wm.MapWindow(c.nextWindow, c.settings.Autotile)
	if err != nil {
		return err
	}
	for _, p := range s.Arrange(c.decor.reserved()).Windows {
		if p.ID == c.nextWindow {
			return t.apply(c.wanted(c.content(p.Rect, p.Layer), p.Layer, true))
		}
	}
	return fmt.Errorf("compositor: the window manager did not place new window %d", c.nextWindow)
}

func (c *Compositor) floatRect(t toplevel, size image.Point) image.Rectangle {
	size = floatSize(t, size).Add(image.Pt(2*c.decor.BorderWidth, 2*c.decor.BorderWidth))
	over := image.Rectangle{}
	if p := c.windowOf(t.parent()); p != nil {
		over = p.tile
	} else if o := c.outputAt(c.cursor); o != nil {
		over = c.usable[o].Add(o.Position())
	}
	corner := over.Min.Add(over.Size().Sub(size).Div(2))
	return image.Rectangle{Min: corner, Max: corner.Add(size)}
}

func floatSize(t toplevel, ruled image.Point) image.Point {
	size := ruled
	if size == (image.Point{}) {
		size = t.Geometry().Size()
	}
	minimum, maximum := t.sizeLimits()
	return clampSize(size, minimum, maximum)
}

func clampSize(size, minimum, maximum image.Point) image.Point {
	clamp := func(v, lo, hi int) int {
		if lo > 0 {
			v = max(v, lo)
		}
		if hi > 0 {
			v = min(v, hi)
		}
		return v
	}
	return image.Pt(clamp(size.X, minimum.X, maximum.X), clamp(size.Y, minimum.Y, maximum.Y))
}

func (c *Compositor) focusWindow(w *window) error {
	c.layerFocus = nil
	return c.update(c.wm.FocusWindow(w.id))
}

func (c *Compositor) keyboardTarget() *surface.Surface {
	if c.shell.Locked() {
		return c.lockTarget()
	}
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
		return c.active.top.Surface()
	}
	return nil
}

func (c *Compositor) refocus() error {
	return c.seat.SetKeyboardFocus(c.keyboardTarget())
}

func (c *Compositor) ToplevelMapped(t *shell.Toplevel) error {
	return c.mapToplevel(xdgToplevel{t})
}

func (c *Compositor) mapToplevel(t toplevel) error {
	w := &window{id: c.nextWindow, top: t}
	c.nextWindow++
	var s wm.State
	var err error
	if float, size := c.floats(t); float {
		s, err = c.wm.MapFloating(w.id, c.floatRect(t, size))
	} else {
		s, err = c.wm.MapWindow(w.id, c.settings.Autotile)
	}
	if err != nil {
		return err
	}
	for _, p := range s.Arrange(c.decor.reserved()).Windows {
		if p.ID == w.id && t.scaled() {
			w.scale = c.scaleOver(p.Rect)
		}
	}
	c.windows = append(c.windows, w)
	return c.update(s, nil)
}

func (c *Compositor) ToplevelUnmapped(t *shell.Toplevel) error {
	return c.unmapToplevel(xdgToplevel{t})
}

func (c *Compositor) unmapToplevel(t toplevel) error {
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

func (c *Compositor) ToplevelChanged(_ *shell.Toplevel, change shell.Change) error {
	if change == shell.ChangedModal {
		return errors.Join(c.reconcile(), c.syncForeign())
	}
	return c.syncForeign()
}

func (c *Compositor) markUrgent(w *window) error {
	if w == c.active || w.urgent {
		return nil
	}
	w.urgent = true
	return errors.Join(c.scheduleAll(), c.syncWorkspaces())
}

func (c *Compositor) ToplevelRequest(t *shell.Toplevel, r shell.Request) error {
	w := c.windowOf(xdgToplevel{t})
	if w == nil {
		return nil
	}
	switch r := r.(type) {
	case shell.Activate:
		return c.markUrgent(w)
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
	w := c.windowOf(xdgToplevel{t})
	return w != nil && w.urgent
}

func (c *Compositor) ClientResponsive(cl *server.Client, responsive bool) error {
	for _, w := range c.windows {
		if s := w.top.Surface(); s != nil && s.Client() == cl {
			w.hung = !responsive
		}
	}
	return nil
}
