package compositor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) Dispatch(a wm.Action) error {
	return c.dispatch(a, nil)
}

func (c *Compositor) dispatch(a wm.Action, trigger binds.Trigger) error {
	s, effects, err := wm.Dispatch(c.wm, a, c.settings)
	if err != nil {
		return err
	}
	if err := c.update(s, nil); err != nil {
		return err
	}
	if c.dispatched != nil {
		c.dispatched(a)
	}
	for _, e := range effects {
		if err := c.perform(e, trigger); err != nil {
			return fmt.Errorf("compositor: %s: %w", a, err)
		}
	}
	return nil
}

func (c *Compositor) perform(e wm.Effect, trigger binds.Trigger) error {
	switch e := e.(type) {
	case wm.Close:
		w := c.windowByID(e.ID)
		if w == nil {
			return fmt.Errorf("close unknown window %d", e.ID)
		}
		return w.top.close()
	case wm.ExecAction:
		return c.spawn(e.Command)
	case wm.GlobalAction:
		return c.pressShortcut(e, trigger)
	case wm.SubmapAction:
		c.submap = e.Name
		return nil
	case wm.DragAction:
		return c.beginDrag(e.Mode, trigger)
	case wm.DPMSAction:
		return c.dpms(e)
	case wm.ReloadAction:
		if c.reload == nil {
			return errors.New("no config file to reload")
		}
		return c.reload()
	case wm.ExitAction:
		if c.exit == nil {
			return errors.New("nothing to exit to: this compositor was started without a way to stop")
		}
		return c.exit()
	}
	return fmt.Errorf("no handler for effect %T", e)
}

func (c *Compositor) spawn(command string) error {
	sh, err := exec.LookPath("sh")
	if err != nil {
		return fmt.Errorf("exec %q: %w", command, err)
	}
	cmd := exec.CommandContext(context.Background(), sh, "-c", command) //nolint:gosec // binds and lys-wmctl dispatch run the user's own commands
	cmd.Env = append(os.Environ(), "WAYLAND_DISPLAY="+c.display.Path())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec %q: %w", command, err)
	}
	go reap(cmd)
	return nil
}

func reap(cmd *exec.Cmd) {
	_ = cmd.Wait()
}
