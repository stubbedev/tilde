package compositor

import (
	"slices"
	"time"

	"github.com/stubbedev/tilde/internal/binds"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/wm"
)

type repeat struct {
	timer  *loop.Timer
	key    uint32
	action wm.Action
	active bool
}

func (c *Compositor) run(a wm.Action) {
	if err := c.Dispatch(a); err != nil {
		c.report(err)
	}
}

func (c *Compositor) match(t binds.Trigger) (binds.Bind, bool) {
	return c.binds.Match(c.submap, c.seat.DepressedModifiers(), t, false)
}

func (c *Compositor) bindKey(code uint32, pressed bool) (bool, error) {
	if !pressed {
		i := slices.Index(c.held, code)
		if i < 0 {
			return false, nil
		}
		c.held = slices.Delete(c.held, i, i+1)
		if c.repeat.active && c.repeat.key == code {
			c.repeat.active = false
			return true, c.repeat.timer.Disarm()
		}
		return true, nil
	}
	b, ok := c.match(binds.Key{Code: code})
	if !ok {
		return false, nil
	}
	c.held = append(c.held, code)
	c.run(b.Action)
	if !b.Flags.Repeating || c.seatConfig.RepeatRate == 0 {
		return true, nil
	}
	c.repeat.key, c.repeat.action, c.repeat.active = code, b.Action, true
	return true, c.repeat.timer.Arm(time.Duration(c.seatConfig.RepeatDelay) * time.Millisecond)
}

func (c *Compositor) repeatFired() error {
	if !c.repeat.active {
		return nil
	}
	c.run(c.repeat.action)
	return c.repeat.timer.Arm(time.Second / time.Duration(c.seatConfig.RepeatRate))
}
