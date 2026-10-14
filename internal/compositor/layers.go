package compositor

import (
	"errors"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/output"
	swlr "github.com/stubbedev/tilde/internal/protocol/server/wlrlayershell"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/surface"
)

type layer struct {
	surface *shell.LayerSurface
	output  *output.Output
	box     image.Rectangle
	placed  bool
}

func (e *layer) bounds() image.Rectangle {
	return e.box.Add(e.output.Position())
}

func (e *layer) shown() bool {
	return e.placed && e.surface.Mapped()
}

func (c *Compositor) layerOf(l *shell.LayerSurface) *layer {
	for _, e := range c.layers {
		if e.surface == l {
			return e
		}
	}
	return nil
}

func (c *Compositor) layerOutput(l *shell.LayerSurface) *output.Output {
	if res := l.Output(); res != nil {
		for _, o := range c.outputs {
			if slices.Contains(o.Resources(l.Surface().Client()), res) {
				return o
			}
		}
		return nil
	}
	for _, o := range c.outputs {
		if image.Pt(int(c.cursor.X), int(c.cursor.Y)).In(o.Logical()) {
			return o
		}
	}
	return c.firstOutput()
}

func (c *Compositor) arrange(o *output.Output) (bool, error) {
	var entries []*layer
	var states []shell.LayerState
	for _, e := range c.layers {
		if e.output != o {
			continue
		}
		if !e.surface.Live() {
			e.placed = false
			continue
		}
		entries = append(entries, e)
		states = append(states, e.surface.Current())
	}
	result, usable := arrangeLayers(image.Rectangle{Max: o.LogicalSize()}, states)
	var errs []error
	for i, e := range entries {
		moved := e.box != result[i].box
		e.box, e.placed = result[i].box, result[i].ok
		if !e.placed {
			continue
		}
		errs = append(errs, e.surface.Configure(e.box.Size()))
		if moved {
			for _, p := range c.layerPopups(e) {
				errs = append(errs, p.popup.Surface().Client().Sent(p.popup.Reconstrain()))
			}
		}
	}
	changed := usable != c.usable[o]
	c.usable[o] = usable
	return changed, errors.Join(errs...)
}

func (c *Compositor) rearrange(o *output.Output) error {
	changed, err := c.arrange(o)
	if changed {
		err = errors.Join(err, c.refit(o))
	}
	return err
}

func (c *Compositor) layersChanged(o *output.Output) error {
	var err error
	if o != nil {
		err = c.rearrange(o)
	}
	return errors.Join(err, c.refocus(), c.refreshPointer(), c.scheduleAll())
}

func (c *Compositor) LayerCommitted(l *shell.LayerSurface) error {
	e := c.layerOf(l)
	if e == nil {
		o := c.layerOutput(l)
		if o == nil {
			return l.Close()
		}
		e = &layer{surface: l, output: o}
		c.layers = append(c.layers, e)
	}
	return c.layersChanged(e.output)
}

func (c *Compositor) LayerUnmapped(l *shell.LayerSurface) error {
	if c.layerFocus == l {
		c.layerFocus = nil
	}
	var o *output.Output
	if e := c.layerOf(l); e != nil {
		o = e.output
	}
	return c.layersChanged(o)
}

func (c *Compositor) LayerDestroyed(l *shell.LayerSurface) error {
	if c.layerFocus == l {
		c.layerFocus = nil
	}
	e := c.layerOf(l)
	if e == nil {
		return nil
	}
	c.layers = slices.DeleteFunc(c.layers, func(x *layer) bool { return x == e })
	return c.layersChanged(e.output)
}

func (c *Compositor) closeLayers(o *output.Output) error {
	var errs []error
	for _, e := range c.layers {
		if e.output == o {
			errs = append(errs, e.surface.Close())
		}
	}
	c.layers = slices.DeleteFunc(c.layers, func(e *layer) bool { return e.output == o })
	return errors.Join(errs...)
}

func (c *Compositor) layersAt(o *output.Output, levels ...shell.Layer) []*layer {
	var out []*layer
	for _, level := range levels {
		for _, e := range c.layers {
			if e.output == o && e.shown() && e.surface.Current().Layer == level {
				out = append(out, e)
			}
		}
	}
	return out
}

var (
	belowWindows = []shell.Layer{swlr.ZwlrLayerShellV1LayerBackground, swlr.ZwlrLayerShellV1LayerBottom}
	aboveWindows = []shell.Layer{swlr.ZwlrLayerShellV1LayerTop, swlr.ZwlrLayerShellV1LayerOverlay}
)

func (c *Compositor) layerPopups(e *layer) []placedPopup {
	if !e.shown() {
		return nil
	}
	return c.popupTree(e.surface, e.bounds().Min)
}

func (c *Compositor) exclusiveLayer() *shell.LayerSurface {
	for _, level := range []shell.Layer{swlr.ZwlrLayerShellV1LayerOverlay, swlr.ZwlrLayerShellV1LayerTop} {
		for _, e := range slices.Backward(c.layers) {
			s := e.surface.Current()
			if e.surface.Mapped() && s.Layer == level && s.Interactivity == swlr.ZwlrLayerSurfaceV1KeyboardInteractivityExclusive {
				return e.surface
			}
		}
	}
	return nil
}

func (c *Compositor) layerOwning(s *surface.Surface) *shell.LayerSurface {
	if s == nil {
		return nil
	}
	for _, e := range c.layers {
		if _, ok := treeOrigin(e.surface.Surface(), image.Point{}, s); ok {
			return e.surface
		}
		for _, p := range c.layerPopups(e) {
			if _, ok := treeOrigin(p.popup.Surface(), image.Point{}, s); ok {
				return e.surface
			}
		}
	}
	return nil
}

func (c *Compositor) clickFocus(s *surface.Surface) error {
	l := c.layerOwning(s)
	if l == nil || l.Current().Interactivity == swlr.ZwlrLayerSurfaceV1KeyboardInteractivityNone {
		return nil
	}
	c.layerFocus = l
	return c.refocus()
}
