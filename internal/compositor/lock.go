package compositor

import (
	"errors"
	"image"

	"github.com/stubbedev/tilde/internal/output"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/surface"
)

var (
	LockedColour    = render.Color{A: 0xff}
	AbandonedColour = render.Color{R: 0x60, A: 0xff}
)

func (c *Compositor) LockChanged() error {
	if !c.shell.LockConfirmed() {
		clear(c.blanked)
	}
	var errs []error
	if c.shell.Locked() {
		errs = append(errs, c.shell.DismissGrab())
	}
	return errors.Join(append(errs, c.refocus(), c.refreshPointer(), c.scheduleAll(), c.confirmLock())...)
}

func (c *Compositor) LockOutputSize(wl *sw.WlOutput) (image.Point, bool) {
	o := c.layout.Owner(wl)
	if !c.outputLive(o) {
		return image.Point{}, false
	}
	return o.LogicalSize(), true
}

func (c *Compositor) lockSurfaceOn(o *output.Output) *shell.LockSurface {
	for _, s := range c.shell.LockSurfaces() {
		if c.layout.Owner(s.Output()) == o {
			return s
		}
	}
	return nil
}

func (c *Compositor) lockTarget() *surface.Surface {
	surfaces := c.shell.LockSurfaces()
	if len(surfaces) == 0 {
		return nil
	}
	focused := c.wm.FocusedOutput()
	for _, s := range surfaces {
		if o := c.layout.Owner(s.Output()); o != nil && o.Backend().Name() == focused {
			return s.Surface()
		}
	}
	return surfaces[0].Surface()
}

func (c *Compositor) hitLock(p point) (*surface.Surface, point) {
	o := c.outputAt(p)
	s := c.lockSurfaceOn(o)
	if s == nil {
		return nil, point{}
	}
	return hitTree(s.Surface(), flat(o.Position()), p)
}

func (c *Compositor) drawLocked(pass *frame, o *output.Output, bounds image.Rectangle, shown []*surface.Surface) []*surface.Surface {
	colour := LockedColour
	if !c.shell.LockAlive() {
		colour = AbandonedColour
	}
	pass.FillRect(bounds, colour)
	if s := c.lockSurfaceOn(o); s != nil {
		shown = c.drawTree(pass, o, s.Surface(), flat(o.Position()), shown)
	}
	return shown
}

func (c *Compositor) lockBlanked(o *output.Output) error {
	if !c.shell.Locked() || c.shell.LockConfirmed() {
		return nil
	}
	c.blanked[o] = true
	return c.confirmLock()
}

func (c *Compositor) confirmLock() error {
	if !c.shell.Locked() || c.shell.LockConfirmed() {
		return nil
	}
	for _, o := range c.outputs {
		if !o.Backend().State().Off && !c.blanked[o] {
			return nil
		}
	}
	return c.shell.ConfirmLock()
}
