// Package compositor connects backend events to the server and draws each
// output's frames.
package compositor

import (
	"errors"
	"fmt"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/seat"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/shm"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/xkb"
)

var Background = render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}

type Config struct {
	Seat seat.Config
}

func DefaultConfig(keymap *xkb.Keymap) Config {
	return Config{Seat: seat.Config{Keymap: keymap, RepeatRate: seat.DefaultRepeatRate, RepeatDelay: seat.DefaultRepeatDelay}}
}

type Compositor struct {
	outputs  []backend.Output
	renderer render.Renderer
	surfaces *surface.Manager
	shm      *shm.Manager
	seat     *seat.Seat
	shell    *shell.Shell
	textures map[*surface.Surface]*texture
	windows  []*window
	shown    map[backend.Output][]*surface.Surface
}

func New(l *loop.Loop, display *server.Display, b backend.Backend, cfg Config) (*Compositor, error) {
	c := &Compositor{renderer: b.Renderer(), textures: map[*surface.Surface]*texture{}, shown: map[backend.Output][]*surface.Surface{}}
	var err error
	if c.surfaces, err = surface.New(display, c); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.shm, err = shm.New(display, c.renderer.Formats()); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.seat, err = seat.New(display, cfg.Seat); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.shell, err = shell.New(display, l, c); err != nil {
		c.seat.Close()
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if err := b.Start(c); err != nil {
		return nil, fmt.Errorf("compositor: start backend: %w", err)
	}
	return c, nil
}

func (c *Compositor) Close() {
	c.seat.Close()
}

func (c *Compositor) Seat() *seat.Seat {
	return c.seat
}

func (c *Compositor) Outputs() []backend.Output {
	return slices.Clone(c.outputs)
}

func (c *Compositor) scheduleAll() error {
	var errs []error
	for _, o := range c.outputs {
		if err := o.ScheduleFrame(); err != nil {
			errs = append(errs, fmt.Errorf("compositor: schedule a frame on %s: %w", o.Name(), err))
		}
	}
	return errors.Join(errs...)
}

func (c *Compositor) OutputAdded(o backend.Output) error {
	c.outputs = append(c.outputs, o)
	if err := c.adoptWindows(o); err != nil {
		return err
	}
	if err := o.ScheduleFrame(); err != nil {
		return fmt.Errorf("compositor: first frame on %s: %w", o.Name(), err)
	}
	return nil
}

func (c *Compositor) OutputRemoved(o backend.Output) error {
	c.outputs = slices.DeleteFunc(c.outputs, func(x backend.Output) bool { return x == o })
	delete(c.shown, o)
	for _, w := range c.windows {
		if w.output == o {
			w.output = nil
		}
	}
	if len(c.outputs) == 0 {
		return nil
	}
	return c.adoptWindows(c.outputs[0])
}

func (c *Compositor) OutputChanged(o backend.Output) error {
	if err := c.refit(o); err != nil {
		return err
	}
	if err := o.ScheduleFrame(); err != nil {
		return fmt.Errorf("compositor: frame after %s changed: %w", o.Name(), err)
	}
	return nil
}

func (c *Compositor) DeviceAdded(d backend.Device) error {
	if d.Capabilities().Has(backend.Keyboard) {
		return c.seat.AddKeyboard()
	}
	return nil
}

func (c *Compositor) DeviceRemoved(d backend.Device) error {
	if d.Capabilities().Has(backend.Keyboard) {
		return c.seat.RemoveKeyboard()
	}
	return nil
}

func (c *Compositor) Input(_ backend.Device, e backend.Event) error {
	if k, ok := e.(backend.Key); ok {
		return c.seat.Key(k.Code, k.Pressed, k.Time)
	}
	return nil
}
