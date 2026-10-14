// Package compositor connects backend events to the server and draws each
// output's frames.
package compositor

import (
	"errors"
	"fmt"
	"image"
	"os"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/binds"
	"github.com/stubbedev/tilde/internal/data"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/seat"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/shm"
	"github.com/stubbedev/tilde/internal/singlepixel"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wm"
	"github.com/stubbedev/tilde/internal/xcursor"
	"github.com/stubbedev/tilde/internal/xkb"
)

var Background = render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}

type Config struct {
	Seat   seat.Config
	Cursor CursorConfig
	WM     wm.Settings
	Binds  []binds.Bind
	Report func(error)
}

type CursorConfig struct {
	Theme *xcursor.Theme
	Size  int
}

const DefaultCursorSize = 24

func DefaultConfig(keymap *xkb.Keymap) Config {
	return Config{
		Seat:   seat.Config{Keymap: keymap, RepeatRate: seat.DefaultRepeatRate, RepeatDelay: seat.DefaultRepeatDelay},
		Cursor: CursorConfig{Size: DefaultCursorSize},
		WM:     wm.Settings{Collapse: wm.CollapseEmptySplits},
		Report: func(err error) { fmt.Fprintln(os.Stderr, "tilde:", err) },
	}
}

type Compositor struct {
	outputs    []*output.Output
	layout     *output.Manager
	renderer   render.Renderer
	surfaces   *surface.Manager
	shm        *shm.Manager
	seat       *seat.Seat
	shell      *shell.Shell
	textures   map[*surface.Surface]*texture
	windows    []*window
	wm         wm.State
	settings   wm.Settings
	binds      binds.Map
	submap     string
	held       []uint32
	repeat     repeat
	report     func(error)
	seatConfig seat.Config
	loop       *loop.Loop
	display    *server.Display
	nextWindow wm.WindowID
	active     *window
	consumed   []uint32
	layers     []*layer
	data       *data.Manager
	drag       *data.Drag
	usable     map[*output.Output]image.Rectangle
	layerFocus *shell.LayerSurface
	shown      map[*output.Output][]*surface.Surface
	entered    []*entered
	cursor     point
	touches    map[int32]*output.Output
	cursors    CursorConfig
	pointers   map[cursorKey]*cursorTexture
}

type entered struct {
	s       *surface.Surface
	outputs []*output.Output
}

func New(l *loop.Loop, display *server.Display, b backend.Backend, cfg Config) (*Compositor, error) {
	c := &Compositor{
		renderer:   b.Renderer(),
		textures:   map[*surface.Surface]*texture{},
		shown:      map[*output.Output][]*surface.Surface{},
		touches:    map[int32]*output.Output{},
		cursors:    cfg.Cursor,
		settings:   cfg.WM,
		report:     cfg.Report,
		seatConfig: cfg.Seat,
		loop:       l,
		display:    display,
		pointers:   map[cursorKey]*cursorTexture{},
		usable:     map[*output.Output]image.Rectangle{},
	}
	if c.report == nil {
		return nil, errors.New("compositor: Config.Report is nil")
	}
	relevant, err := binds.Relevant(cfg.Seat.Keymap)
	if err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.binds, err = binds.NewMap(cfg.Binds, relevant); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.repeat.timer, err = l.AddTimer(c.repeatFired); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.cursors.Size <= 0 {
		return nil, fmt.Errorf("compositor: cursor size %d is not positive", c.cursors.Size)
	}
	if c.layout, err = output.New(display); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.surfaces, err = surface.New(display, c); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.shm, err = shm.New(display, c.renderer.Formats()); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if err := singlepixel.New(display); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.data, err = data.New(display, c); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if c.seat, err = seat.New(display, cfg.Seat, c); err != nil {
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
	for _, ct := range c.pointers {
		ct.tex.Destroy()
	}
	clear(c.pointers)
	c.seat.Close()
}

func (c *Compositor) Seat() *seat.Seat {
	return c.seat
}

func (c *Compositor) Outputs() []*output.Output {
	return slices.Clone(c.outputs)
}

func (c *Compositor) scheduleAll() error {
	var errs []error
	for _, o := range c.outputs {
		errs = append(errs, c.schedule(o))
	}
	return errors.Join(errs...)
}

func (c *Compositor) schedule(o *output.Output) error {
	if err := o.Backend().ScheduleFrame(); err != nil {
		return fmt.Errorf("compositor: schedule a frame on %s: %w", o.Backend().Name(), err)
	}
	return nil
}

func (c *Compositor) relayout() error {
	x := 0
	var errs []error
	for _, o := range c.outputs {
		if o.Position() != image.Pt(x, 0) {
			errs = append(errs, o.Update(image.Pt(x, 0), o.Scale()))
		}
		x += o.LogicalSize().X
	}
	return errors.Join(errs...)
}

func (c *Compositor) output(b backend.Output) *output.Output {
	for _, o := range c.outputs {
		if o.Backend() == b {
			return o
		}
	}
	return nil
}

func (c *Compositor) OutputAdded(b backend.Output) error {
	x := 0
	for _, o := range c.outputs {
		x = max(x, o.Logical().Max.X)
	}
	o, err := c.layout.Add(b, image.Pt(x, 0), output.Unit)
	if err != nil {
		return err
	}
	c.outputs = append(c.outputs, o)
	if len(c.outputs) == 1 {
		center := o.Logical().Min.Add(o.LogicalSize().Div(2))
		c.cursor = point{float64(center.X), float64(center.Y)}
	}
	if _, err := c.arrange(o); err != nil {
		return err
	}
	if err := c.update(c.wm.AddOutput(b.Name(), c.usable[o].Add(o.Position()))); err != nil {
		return err
	}
	return c.schedule(o)
}

func (c *Compositor) OutputRemoved(b backend.Output) error {
	o := c.output(b)
	if o == nil {
		return fmt.Errorf("compositor: removal of unknown output %s", b.Name())
	}
	c.outputs = slices.DeleteFunc(c.outputs, func(x *output.Output) bool { return x == o })
	delete(c.shown, o)
	errs := []error{c.closeLayers(o), o.Remove()}
	delete(c.usable, o)
	for _, e := range c.entered {
		if slices.Contains(e.outputs, o) {
			errs = append(errs, e.s.Client().Sent(e.s.Leave(o.Resources(e.s.Client()))))
			e.outputs = slices.DeleteFunc(e.outputs, func(x *output.Output) bool { return x == o })
		}
	}
	for _, w := range c.windows {
		if w.output == o {
			w.output = nil
		}
	}
	s, err := c.wm.RemoveOutput(b.Name())
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	c.wm = s
	errs = append(errs, c.relayout(), c.syncAreas())
	return errors.Join(errs...)
}

func (c *Compositor) OutputChanged(b backend.Output) error {
	o := c.output(b)
	if o == nil {
		return fmt.Errorf("compositor: change of unknown output %s", b.Name())
	}
	if err := errors.Join(o.Changed(), c.relayout()); err != nil {
		return err
	}
	_, err := c.arrange(o)
	return errors.Join(err, c.syncAreas(), c.schedule(o))
}

func (c *Compositor) SetOutputScale(b backend.Output, scale output.Scale) error {
	o := c.output(b)
	if o == nil {
		return fmt.Errorf("compositor: scale for unknown output %s", b.Name())
	}
	if err := o.Update(o.Position(), scale); err != nil {
		return err
	}
	if err := c.relayout(); err != nil {
		return err
	}
	_, err := c.arrange(o)
	return errors.Join(err, c.syncAreas(), c.schedule(o))
}
