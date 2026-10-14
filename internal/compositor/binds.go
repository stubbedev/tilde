package compositor

import (
	"errors"
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

const (
	keysymSwitchVT1  = 0x1008fe01
	keysymSwitchVT12 = 0x1008fe0c
)

func vtSwitch(keysym uint32) uint32 {
	if keysym < keysymSwitchVT1 || keysym > keysymSwitchVT12 {
		return 0
	}
	return keysym - keysymSwitchVT1 + 1
}

func (c *Compositor) match(t binds.Trigger) (binds.Bind, bool) {
	return c.binds.Match(c.submap, c.seat.DepressedModifiers(), t, c.shell.Locked() || c.inhibiting != nil)
}

func (c *Compositor) bindKey(code uint32, pressed bool) (bool, error) {
	if !pressed {
		i := slices.Index(c.held, code)
		if i < 0 {
			return false, nil
		}
		c.held = slices.Delete(c.held, i, i+1)
		released := c.releaseShortcuts(binds.Key{Code: code})
		if c.repeat.active && c.repeat.key == code {
			c.repeat.active = false
			return true, errors.Join(released, c.repeat.timer.Disarm())
		}
		return true, released
	}
	if vt := vtSwitch(c.seat.Keysym(code)); vt != 0 && c.switchVT != nil {
		c.held = append(c.held, code)
		return true, c.switchVT(vt)
	}
	b, ok := c.match(binds.Key{Code: code})
	if !ok {
		return false, nil
	}
	c.held = append(c.held, code)
	c.firing = binds.Key{Code: code}
	c.run(b.Action)
	c.firing = nil
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
