package compositor

import (
	"image"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shell"
)

type window struct {
	toplevel *shell.Toplevel
	output   backend.Output
}

func (w *window) placement() image.Rectangle {
	if w.output == nil {
		return image.Rectangle{}
	}
	return image.Rectangle{Max: w.output.State().Mode.Size}
}

func (c *Compositor) firstOutput() backend.Output {
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

func (c *Compositor) configure(t *shell.Toplevel, o backend.Output, activated bool) error {
	var s shell.State
	if o != nil {
		s.Size = o.State().Mode.Size
		s.Bounds = s.Size
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
	if prev := c.seat.Focus(); prev != nil {
		for _, w := range c.windows {
			if w.toplevel.Surface() == prev && w != target {
				if err := c.configure(w.toplevel, w.output, false); err != nil {
					return err
				}
			}
		}
	}
	if target == nil {
		return c.seat.SetKeyboardFocus(nil)
	}
	if !target.toplevel.Requested().Has(sxdg.XdgToplevelStateActivated) {
		if err := c.configure(target.toplevel, target.output, true); err != nil {
			return err
		}
	}
	return c.seat.SetKeyboardFocus(target.toplevel.Surface())
}

func (c *Compositor) ToplevelMapped(t *shell.Toplevel) error {
	w := &window{toplevel: t, output: c.firstOutput()}
	c.windows = append(c.windows, w)
	if err := c.focus(w); err != nil {
		return err
	}
	return c.scheduleAll()
}

func (c *Compositor) ToplevelUnmapped(t *shell.Toplevel) error {
	c.windows = slices.DeleteFunc(c.windows, func(w *window) bool { return w.toplevel == t })
	if c.seat.Focus() == t.Surface() {
		var next *window
		if n := len(c.windows); n > 0 {
			next = c.windows[n-1]
		}
		if err := c.focus(next); err != nil {
			return err
		}
	}
	return c.scheduleAll()
}

func (c *Compositor) adoptWindows(o backend.Output) error {
	for _, w := range c.windows {
		if w.output == nil {
			w.output = o
			if err := c.configure(w.toplevel, o, c.seat.Focus() == w.toplevel.Surface()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Compositor) refit(o backend.Output) error {
	for _, w := range c.windows {
		if w.output == o {
			if err := c.configure(w.toplevel, o, c.seat.Focus() == w.toplevel.Surface()); err != nil {
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
