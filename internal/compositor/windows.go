package compositor

import (
	"errors"
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/output"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/surface"
)

type window struct {
	toplevel *shell.Toplevel
	output   *output.Output
}

func (w *window) placement() image.Rectangle {
	if w.output == nil {
		return image.Rectangle{}
	}
	return w.output.Logical()
}

func (c *Compositor) firstOutput() *output.Output {
	if len(c.outputs) == 0 {
		return nil
	}
	return c.outputs[0]
}

func (c *Compositor) Toplevels() []*shell.Toplevel {
	out := make([]*shell.Toplevel, 0, len(c.windows))
	for _, w := range c.windows {
		out = append(out, w.toplevel)
	}
	return out
}

func (c *Compositor) configure(t *shell.Toplevel, o *output.Output, activated bool) error {
	var s shell.State
	if o != nil {
		s.Size = o.LogicalSize()
		s.Bounds = s.Size
		if err := t.Surface().Client().Sent(t.Surface().SetPreferredScale(uint32(o.Scale()))); err != nil {
			return err
		}
	}
	if activated {
		s.States = []shell.ToplevelState{sxdg.XdgToplevelStateActivated}
	}
	_, err := t.Configure(s)
	return t.Surface().Client().Sent(err)
}

func (c *Compositor) ToplevelInitialCommit(t *shell.Toplevel) error {
	return c.configure(t, c.firstOutput(), true)
}

func (c *Compositor) focus(target *window) error {
	if prev := c.active; prev != nil && prev != target {
		if err := c.configure(prev.toplevel, prev.output, false); err != nil {
			return err
		}
	}
	c.active = target
	if target != nil && !target.toplevel.Requested().Has(sxdg.XdgToplevelStateActivated) {
		if err := c.configure(target.toplevel, target.output, true); err != nil {
			return err
		}
	}
	return c.refocus()
}

func (c *Compositor) keyboardTarget() *surface.Surface {
	if top := c.grabTop(); top != nil {
		return top
	}
	if c.active == nil {
		return nil
	}
	return c.active.toplevel.Surface()
}

func (c *Compositor) refocus() error {
	return c.seat.SetKeyboardFocus(c.keyboardTarget())
}

func (c *Compositor) ToplevelMapped(t *shell.Toplevel) error {
	w := &window{toplevel: t, output: c.firstOutput()}
	c.windows = append(c.windows, w)
	if err := c.focus(w); err != nil {
		return err
	}
	return errors.Join(c.refreshPointer(), c.scheduleAll())
}

func (c *Compositor) ToplevelUnmapped(t *shell.Toplevel) error {
	c.windows = slices.DeleteFunc(c.windows, func(w *window) bool { return w.toplevel == t })
	if c.active != nil && c.active.toplevel == t {
		c.active = nil
		var next *window
		if n := len(c.windows); n > 0 {
			next = c.windows[n-1]
		}
		if err := c.focus(next); err != nil {
			return err
		}
	}
	return errors.Join(c.refreshPointer(), c.scheduleAll())
}

func (c *Compositor) adoptWindows(o *output.Output) error {
	for _, w := range c.windows {
		if w.output == nil {
			w.output = o
			if err := errors.Join(c.configure(w.toplevel, o, c.active == w), c.reconstrain(w)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Compositor) refit(o *output.Output) error {
	for _, w := range c.windows {
		if w.output == o {
			if err := errors.Join(c.configure(w.toplevel, o, c.active == w), c.reconstrain(w)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Compositor) ToplevelCommitted(*shell.Toplevel) error { return nil }

func (c *Compositor) ToplevelChanged(*shell.Toplevel, shell.Change) error { return nil }

func (c *Compositor) ToplevelRequest(*shell.Toplevel, shell.Request) error { return nil }

func (c *Compositor) ClientResponsive(*server.Client, bool) error { return nil }
