package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/output"
	spt "github.com/stubbedev/tilde/internal/protocol/server/presentationtime"
	swlr "github.com/stubbedev/tilde/internal/protocol/server/wlrlayershell"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wm"
)

func (c *Compositor) Frame(b backend.Output) error {
	o := c.output(b)
	if o == nil {
		return fmt.Errorf("compositor: frame on unknown output %s", b.Name())
	}
	if err := c.render(o); err != nil {
		return fmt.Errorf("compositor: frame on %s: %w", b.Name(), err)
	}
	return errors.Join(c.updateEntered(o), c.updateInhibition())
}

func (c *Compositor) updateInhibition() error {
	for _, i := range c.idle.Inhibitors() {
		for _, shown := range c.shown {
			if slices.Contains(shown, i.Surface()) {
				return c.idle.SetInhibited(true)
			}
		}
	}
	return c.idle.SetInhibited(false)
}

func (c *Compositor) sceneOf(o *output.Output) *scene {
	sc, ok := c.scenes[o]
	if !ok {
		sc = newScene()
		c.scenes[o] = sc
	}
	return sc
}

func (c *Compositor) render(o *output.Output) error {
	sc := c.sceneOf(o)
	bounds := image.Rectangle{Max: o.Backend().State().Mode.Size}
	pass := &sc.cur
	pass.FillRect(bounds, Background)
	shown := c.shown[o][:0]
	var err error
	if c.shell.Locked() {
		shown = c.drawLocked(pass, o, bounds, shown)
	} else if shown, err = c.drawDesktop(pass, o, shown); err != nil {
		return err
	}
	placed, err := c.placeCursor(o)
	if err != nil {
		return err
	}
	if hw, ok := c.hwCursors[o]; ok {
		hw.inFrame = !placed
	}
	if placed {
		shown = append(shown, c.cursorShown...)
	} else if shown, err = c.drawCursor(pass, o, shown); err != nil {
		return err
	}
	c.shown[o] = shown
	if sc.diff(bounds).Empty() {
		sc.skipped()
		return errors.Join(c.unchanged(o, sc), c.lockBlanked(o))
	}
	f, err := o.Backend().BeginFrame()
	if err != nil {
		return err
	}
	repaint := render.Region(sc.repaintFor(f, bounds).Rects())
	p, err := f.Renderer.Begin(f.Target, repaint)
	if err != nil {
		return err
	}
	sc.cur.replay(p)
	if err := p.End(); err != nil {
		return err
	}
	if err := o.Backend().Present(repaint); err != nil {
		return err
	}
	sc.presented()
	return c.lockBlanked(o)
}

func (c *Compositor) drawDesktop(pass *frame, o *output.Output, shown []*surface.Surface) ([]*surface.Surface, error) {
	for _, e := range c.layersAt(o, belowWindows...) {
		shown = c.drawTree(pass, o, e.surface.Surface(), flat(e.bounds().Min), shown)
	}
	for _, layer := range []wm.PlacementLayer{wm.LayerTiled, wm.LayerFloating} {
		for _, w := range c.stack {
			if w.layer == layer {
				shown = c.drawWindow(pass, o, w, shown)
			}
		}
		if layer == wm.LayerTiled {
			if err := c.drawTabBars(pass, o); err != nil {
				return shown, err
			}
		}
	}
	for _, w := range c.stack {
		if w.layer != wm.LayerFullscreen {
			shown = c.drawPopups(pass, o, c.popups(w), shown)
		}
	}
	for _, e := range c.layersAt(o, swlr.ZwlrLayerShellV1LayerTop) {
		shown = c.drawTree(pass, o, e.surface.Surface(), flat(e.bounds().Min), shown)
	}
	for _, w := range c.stack {
		if w.layer == wm.LayerFullscreen {
			shown = c.drawWindow(pass, o, w, shown)
			shown = c.drawPopups(pass, o, c.popups(w), shown)
		}
	}
	if err := c.evictLabels(o); err != nil {
		return shown, err
	}
	for _, e := range c.layersAt(o, swlr.ZwlrLayerShellV1LayerOverlay) {
		shown = c.drawTree(pass, o, e.surface.Surface(), flat(e.bounds().Min), shown)
	}
	for _, e := range c.layers {
		shown = c.drawPopups(pass, o, c.layerPopups(e), shown)
	}
	shown = c.drawUnmanaged(pass, o, shown)
	shown = c.drawDragIcon(pass, o, shown)
	return shown, nil
}

func (c *Compositor) unchanged(o *output.Output, sc *scene) error {
	now := backend.Now()
	var err error
	for _, s := range c.shown[o] {
		c.outputResources = o.AppendResources(c.outputResources[:0], s.Client())
		if e := errors.Join(s.Client().Sent(s.FrameDone(wire.Millis(now))), s.Client().Sent(s.Presented(c.outputResources, now, 0, sc.seq, 0))); e != nil {
			err = errors.Join(err, e)
		}
	}
	return err
}

func (c *Compositor) drawWindow(pass *frame, o *output.Output, w *window, shown []*surface.Surface) []*surface.Surface {
	if !w.visible || !w.tile.Overlaps(o.Logical()) {
		return shown
	}
	shown = c.drawTree(pass, o, w.top.Surface(), c.windowOrigin(w), shown)
	c.drawBorder(pass, o, w)
	return shown
}

func (c *Compositor) drawPopups(pass *frame, o *output.Output, popups []placedPopup, shown []*surface.Surface) []*surface.Surface {
	for _, p := range popups {
		if p.bounds().Overlaps(o.Logical()) {
			shown = c.drawTree(pass, o, p.popup.Surface(), flat(p.origin), shown)
		}
	}
	return shown
}

func (c *Compositor) drawTree(pass *frame, o *output.Output, s *surface.Surface, org origin, shown []*surface.Surface) []*surface.Surface {
	for member := range s.Stack() {
		if member != s {
			if sub := member.Subsurface(); sub != nil {
				shown = c.drawTree(pass, o, member, org.child(sub.Position()), shown)
			}
			continue
		}
		tex := c.Texture(s)
		if tex == nil {
			continue
		}
		logical := image.Rectangle{Min: org.at, Max: org.at.Add(org.size(s.Size()))}.Sub(o.Position())
		pass.drawSurface(s, tex, s.BufferSource(), o.Scale().Rect(logical), s.Transform().Invert())
		shown = append(shown, s)
	}
	return shown
}

func (c *Compositor) enteredOf(s *surface.Surface) *entered {
	for _, e := range c.entered {
		if e.s == s {
			return e
		}
	}
	e := &entered{s: s}
	c.entered = append(c.entered, e)
	return e
}

func (c *Compositor) updateEntered(o *output.Output) error {
	var errs []error
	shown := c.shown[o]
	for _, s := range shown {
		e := c.enteredOf(s)
		if slices.Contains(e.outputs, o) {
			continue
		}
		e.outputs = append(e.outputs, o)
		errs = append(errs, s.Client().Sent(s.Enter(o.Resources(s.Client()))), s.Client().Sent(s.SetPreferredScale(uint32(o.Scale()))))
	}
	for _, e := range c.entered {
		if slices.Contains(e.outputs, o) && !slices.Contains(shown, e.s) {
			e.outputs = slices.DeleteFunc(e.outputs, func(x *output.Output) bool { return x == o })
			errs = append(errs, e.s.Client().Sent(e.s.Leave(o.Resources(e.s.Client()))))
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) Presented(b backend.Output, p backend.Presentation) error {
	o := c.output(b)
	if o == nil {
		return nil
	}
	c.sceneOf(o).seq = p.Seq
	var errs []error
	for _, s := range c.shown[o] {
		errs = append(errs,
			s.Client().Sent(s.FrameDone(wire.Millis(p.Time))),
			s.Client().Sent(s.Presented(o.Resources(s.Client()), p.Time, p.Refresh, p.Seq, spt.WpPresentationFeedbackKindVsync)))
	}
	return errors.Join(errs...)
}
