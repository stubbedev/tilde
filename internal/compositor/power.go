package compositor

import (
	"errors"
	"fmt"

	"github.com/stubbedev/tilde/internal/output"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/wm"
)

func (c *Compositor) Owner(res *sw.WlOutput) *output.Output {
	return c.layout.Owner(res)
}

func (c *Compositor) Powered(o *output.Output) bool {
	return !o.Backend().State().Off
}

func (c *Compositor) SetPower(o *output.Output, on bool) error {
	b := o.Backend()
	st := b.State()
	if st.Off == !on {
		return nil
	}
	st.Off = !on
	if err := b.Commit(st); err != nil {
		return fmt.Errorf("compositor: power %s on=%v: %w", b.Name(), on, err)
	}
	var err error
	if on {
		c.sceneOf(o).invalidate()
		err = c.schedule(o)
	} else {
		err = errors.Join(c.fifo.Latched(nil), c.confirmLock())
	}
	return errors.Join(err, c.power.Changed(o))
}

func (c *Compositor) dpms(e wm.Power) error {
	var targets []*output.Output
	for _, o := range c.outputs {
		if e.Output == "" || o.Backend().Name() == e.Output {
			targets = append(targets, o)
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("no output %q", e.Output)
	}
	var errs []error
	for _, o := range targets {
		on := e.Mode == wm.PowerOn || e.Mode == wm.PowerToggle && !c.Powered(o)
		errs = append(errs, c.SetPower(o, on))
	}
	return errors.Join(errs...)
}
