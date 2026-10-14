// Package compositor connects backend events to the server and draws each
// output's frames.
package compositor

import (
	"errors"
	"fmt"
	"image"
	"os"
	"slices"

	gtext "github.com/stubbedev/gelm/render"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/binds"
	"github.com/stubbedev/tilde/internal/data"
	"github.com/stubbedev/tilde/internal/dmabuf"
	"github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/idle"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/outputpower"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/seat"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/shm"
	"github.com/stubbedev/tilde/internal/singlepixel"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/syncobj"
	"github.com/stubbedev/tilde/internal/wm"
	"github.com/stubbedev/tilde/internal/xcursor"
	"github.com/stubbedev/tilde/internal/xkb"
	"github.com/stubbedev/tilde/internal/xwayland"
)

var Background = render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}

type Config struct {
	Seat     seat.Config
	Cursor   CursorConfig
	WM       wm.Settings
	Binds    []binds.Bind
	Report   func(error)
	Decor    Decor
	Font     *gtext.Typeface
	Rules    []wm.Rule
	Outputs  []OutputRule
	Reload   func() error
	SwitchVT func(vt uint32) error
}

type CursorConfig struct {
	Theme *xcursor.Theme
	Size  int
}

const DefaultCursorSize = 24

func DefaultSettings() wm.Settings {
	return wm.Settings{
		Collapse: wm.CollapseEmptySplits,
		Autotile: wm.Autotile{Enabled: true, Ephemeral: true, MinTrigger: 400, Divisor: 3},
	}
}

func DefaultConfig(keymap *xkb.Keymap, font *gtext.Typeface) Config {
	return Config{
		Seat:   seat.Config{Keymap: keymap, RepeatRate: seat.DefaultRepeatRate, RepeatDelay: seat.DefaultRepeatDelay},
		Cursor: CursorConfig{Size: DefaultCursorSize},
		WM:     DefaultSettings(),
		Report: func(err error) { fmt.Fprintln(os.Stderr, "tilde:", err) },
		Decor:  DefaultDecor(),
		Font:   font,
	}
}

type Compositor struct {
	outputs         []*output.Output
	layout          *output.Manager
	renderer        render.Renderer
	surfaces        *surface.Manager
	shm             *shm.Manager
	seat            *seat.Seat
	shell           *shell.Shell
	textures        map[*surface.Surface]*texture
	imported        map[*dmabuf.Buffer]render.Texture
	dmabuf          *dmabuf.Manager
	importer        dmabufRenderer
	power           *outputpower.Manager
	drm             *drm.Device
	lidClosed       bool
	windows         []*window
	stack           []*window
	interaction     *interaction
	bindButton      uint32
	wm              wm.State
	settings        wm.Settings
	rules           []wm.Rule
	decor           Decor
	font            *gtext.Typeface
	labels          map[*output.Output]map[labelKey]*label
	scenes          map[*output.Output]*scene
	outputResources []*sw.WlOutput
	tabBars         []wm.TabBar
	binds           binds.Map
	submap          string
	held            []uint32
	repeat          repeat
	report          func(error)
	seatConfig      seat.Config
	outputRules     []OutputRule
	disabled        []backend.Output
	reload          func() error
	switchVT        func(vt uint32) error
	dispatched      func(wm.Action)
	loop            *loop.Loop
	display         *server.Display
	nextWindow      wm.WindowID
	active          *window
	consumed        []uint32
	layers          []*layer
	data            *data.Manager
	drag            *data.Drag
	usable          map[*output.Output]image.Rectangle
	layerFocus      *shell.LayerSurface
	shown           map[*output.Output][]*surface.Surface
	entered         []*entered
	cursor          point
	touches         map[int32]*output.Output
	cursors         CursorConfig
	pointers        map[cursorKey]*cursorTexture
	hwCursors       map[*output.Output]*hwCursor
	unmanaged       []*xwayland.Window
	selectionWatch  func(data.Kind, data.Source) error
	xwaylandClients func(*server.Client) bool
	blanked         map[*output.Output]bool
	idle            *idle.Manager
	extLists        []*extList
	wlrManagers     []*wlrManager
	wsManagers      []*wsManager
	captures        map[*output.Output][]capture
	captureSessions []*captureSession
	cursorGen       uint64
	cursorOps       frame
	cursorShown     []*surface.Surface
}

type entered struct {
	s       *surface.Surface
	outputs []*output.Output
}

func validate(cfg Config) (binds.Map, error) {
	if cfg.Report == nil {
		return binds.Map{}, errors.New("compositor: Config.Report is nil")
	}
	if cfg.Font == nil {
		return binds.Map{}, errors.New("compositor: Config.Font is nil")
	}
	if cfg.Cursor.Size <= 0 {
		return binds.Map{}, fmt.Errorf("compositor: cursor size %d is not positive", cfg.Cursor.Size)
	}
	relevant, err := binds.Relevant(cfg.Seat.Keymap)
	if err != nil {
		return binds.Map{}, fmt.Errorf("compositor: %w", err)
	}
	m, err := binds.NewMap(cfg.Binds, relevant)
	if err != nil {
		return binds.Map{}, fmt.Errorf("compositor: %w", err)
	}
	return m, nil
}

func (c *Compositor) adopt(cfg Config, m binds.Map) {
	c.cursors, c.settings, c.rules, c.decor, c.font = cfg.Cursor, cfg.WM, cfg.Rules, cfg.Decor, cfg.Font
	c.report, c.seatConfig, c.outputRules, c.reload, c.binds = cfg.Report, cfg.Seat, cfg.Outputs, cfg.Reload, m
	c.switchVT = cfg.SwitchVT
}

func New(l *loop.Loop, display *server.Display, b backend.Backend, cfg Config) (*Compositor, error) {
	m, err := validate(cfg)
	if err != nil {
		return nil, err
	}
	c := &Compositor{
		renderer:   b.Renderer(),
		textures:   map[*surface.Surface]*texture{},
		imported:   map[*dmabuf.Buffer]render.Texture{},
		shown:      map[*output.Output][]*surface.Surface{},
		touches:    map[int32]*output.Output{},
		labels:     map[*output.Output]map[labelKey]*label{},
		scenes:     map[*output.Output]*scene{},
		loop:       l,
		display:    display,
		pointers:   map[cursorKey]*cursorTexture{},
		hwCursors:  map[*output.Output]*hwCursor{},
		blanked:    map[*output.Output]bool{},
		captures:   map[*output.Output][]capture{},
		usable:     map[*output.Output]image.Rectangle{},
		nextWindow: 1,
	}
	c.adopt(cfg, m)
	if c.repeat.timer, err = l.AddTimer(c.repeatFired); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
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
	if c.power, err = outputpower.New(display, c); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if imp, ok := c.renderer.(dmabufRenderer); ok {
		c.importer = imp
		if c.dmabuf, err = dmabuf.New(display, imp, c); err != nil {
			return nil, fmt.Errorf("compositor: %w", err)
		}
	}
	if err := c.explicitSync(l, display); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	if c.idle, err = idle.New(display, l, backend.Now, c.updateInhibition); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if err := c.addVirtualInput(display); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if err := c.addForeignToplevels(display); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if err := c.addWorkspaces(display); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if err := c.addScreencopy(display); err != nil {
		return nil, fmt.Errorf("compositor: %w", err)
	}
	if err := c.addImageCapture(display); err != nil {
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

func (c *Compositor) Reconfigure(cfg Config) error {
	m, err := validate(cfg)
	if err != nil {
		return err
	}
	pending, err := seat.Prepare(cfg.Seat)
	if err != nil {
		return fmt.Errorf("compositor: %w", err)
	}
	c.adopt(cfg, m)
	c.submap = ""
	c.repeat.active = false
	if err := errors.Join(c.dropDecor(), c.seat.Reconfigure(pending), c.repeat.timer.Disarm(), c.applyOutputRules(), c.scheduleAll()); err != nil {
		c.report(fmt.Errorf("compositor: apply the new config: %w", err))
	}
	return nil
}

func (c *Compositor) dropDecor() error {
	var errs []error
	for o := range c.labels {
		errs = append(errs, c.dropLabels(o))
	}
	return errors.Join(append(errs, c.dropPointers())...)
}

func (c *Compositor) explicitSync(l *loop.Loop, display *server.Display) error {
	r, ok := c.renderer.(syncRenderer)
	if !ok || !r.NativeFences() {
		return nil
	}
	node, err := r.RenderNode()
	if errors.Is(err, gpu.ErrUnsupported) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("compositor: %w", err)
	}
	if c.drm, err = drm.Open(node); err != nil {
		return fmt.Errorf("compositor: %w", err)
	}
	timelines, err := c.drm.Cap(drm.CapSyncobjTimeline)
	if err != nil || timelines == 0 {
		dev := c.drm
		c.drm = nil
		return errors.Join(err, dev.Close())
	}
	if _, err := syncobj.New(display, l, c.drm, r); err != nil {
		return fmt.Errorf("compositor: %w", err)
	}
	return nil
}

func (c *Compositor) Close() error {
	errs := []error{c.dropDecor()}
	for b, tex := range c.imported {
		errs = append(errs, tex.Destroy())
		delete(c.imported, b)
	}
	if c.dmabuf != nil {
		errs = append(errs, c.dmabuf.Close())
	}
	if c.drm != nil {
		errs = append(errs, c.drm.Close())
		c.drm = nil
	}
	c.seat.Close()
	return errors.Join(errs...)
}

func (c *Compositor) Seat() *seat.Seat {
	return c.seat
}

func (c *Compositor) Outputs() []*output.Output {
	return slices.Clone(c.outputs)
}

func (c *Compositor) scheduleAll() error {
	var err error
	for _, o := range c.outputs {
		if e := c.schedule(o); e != nil {
			err = errors.Join(err, e)
		}
	}
	return err
}

func (c *Compositor) schedule(o *output.Output) error {
	if err := o.Backend().ScheduleFrame(); err != nil {
		return fmt.Errorf("compositor: schedule a frame on %s: %w", o.Backend().Name(), err)
	}
	return nil
}
