// Command lys-wm is a pure-Go tiling Wayland compositor.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	gtext "github.com/stubbedev/lys-ui/render"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/backend/nested"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/config"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/session"
	"github.com/stubbedev/lys-wm/internal/systemd"
	"github.com/stubbedev/lys-wm/internal/wlclient"
	"github.com/stubbedev/lys-wm/internal/wm"
	"github.com/stubbedev/lys-wm/internal/xcursor"
	"github.com/stubbedev/lys-wm/internal/xkb"
	"github.com/stubbedev/lys-wm/internal/xwayland"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "lys-wm:", err)
		os.Exit(1)
	}
}

type debug struct {
	wire bool
}

func parseDebug(s string) (debug, error) {
	var d debug
	for category := range strings.SplitSeq(s, ",") {
		switch strings.TrimSpace(category) {
		case "":
		case "wire":
			d.wire = true
		default:
			return debug{}, fmt.Errorf("LYS_WM_DEBUG: unknown category %q (known: wire)", category)
		}
	}
	return d, nil
}

var headlessMode = backend.Mode{Size: image.Pt(1920, 1080), RefreshMHz: 60000, Preferred: true}

type backendKind uint8

const (
	nestedBackend backendKind = iota + 1
	headlessBackend
	drmBackend
)

var backendNames = map[string]backendKind{"nested": nestedBackend, "headless": headlessBackend, "drm": drmBackend}

func (k backendKind) String() string {
	for name, kind := range backendNames {
		if kind == k {
			return name
		}
	}
	return fmt.Sprintf("backend%d", k)
}

func chooseBackend(requested, waylandDisplay string) (backendKind, error) {
	if requested == "" {
		if waylandDisplay != "" {
			return nestedBackend, nil
		}
		return drmBackend, nil
	}
	if k, ok := backendNames[requested]; ok {
		return k, nil
	}
	return 0, fmt.Errorf("unknown backend %q (available: drm, nested, headless)", requested)
}

type rendererKind uint8

const (
	softRenderer rendererKind = iota + 1
	glesRenderer
)

func chooseRenderer(requested string, kind backendKind) (rendererKind, error) {
	switch requested {
	case "":
		if kind == headlessBackend {
			return softRenderer, nil
		}
		return glesRenderer, nil
	case "soft":
		if kind == drmBackend {
			return 0, errors.New("the drm backend renders with GLES")
		}
		return softRenderer, nil
	case "gles":
		return glesRenderer, nil
	}
	return 0, fmt.Errorf("unknown renderer %q (available: gles, soft)", requested)
}

func openHeadless(l *loop.Loop, renderer rendererKind, paths gpu.Paths) (*headless.Backend, error) {
	if renderer == glesRenderer {
		return headless.NewGLES(l, paths)
	}
	return headless.New(l), nil
}

type opened struct {
	backend.Backend
	switchVT       func(vt uint32) error
	configureInput func(session.InputConfig) error
}

func hostInput(session.InputConfig) error { return nil }

func openBackend(kind backendKind, renderer rendererKind, l *loop.Loop, input session.InputConfig, gpu string, report func(error)) (opened, error) {
	if kind == drmBackend {
		n, err := openNative(l, os.Getenv, input, gpu, report)
		if err != nil {
			return opened{}, err
		}
		return opened{Backend: n, switchVT: n.logind.SwitchTo, configureInput: n.input.Reconfigure}, nil
	}
	b, err := openOther(kind, renderer, l)
	return opened{Backend: b, configureInput: hostInput}, err
}

func openOther(kind backendKind, renderer rendererKind, l *loop.Loop) (backend.Backend, error) {
	paths := gpu.PathsFromEnv(os.Getenv)
	hint := func(err error) error {
		if err != nil && renderer == glesRenderer {
			return fmt.Errorf("%w (run with --renderer soft to render in software)", err)
		}
		return err
	}
	if kind == nestedBackend {
		host, err := wlclient.SocketPath(os.Getenv)
		if err != nil {
			return nil, err
		}
		opts := nested.Options{Renderer: nested.Soft, GPU: paths}
		if renderer == glesRenderer {
			opts.Renderer = nested.GLES
		}
		b, err := nested.New(l, host, opts)
		return b, hint(err)
	}
	b, err := openHeadless(l, renderer, paths)
	if err != nil {
		return nil, hint(err)
	}
	if _, err := b.AddOutput(headlessMode); err != nil {
		return nil, errors.Join(err, b.Close())
	}
	return b, nil
}

var defaultFont = "DejaVuSans.ttf"

func fontPath(configured string, getenv func(string) string) string {
	if configured != "" {
		return configured
	}
	if env := getenv("LYS_WM_FONT"); env != "" {
		return env
	}
	return defaultFont
}

func loadFont(path string) (*gtext.Typeface, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the font path comes from the build, the config or the environment
	if err != nil {
		return nil, fmt.Errorf("read the tab font (set tabs.font in the config, or LYS_WM_FONT): %w", err)
	}
	f, err := gtext.LoadFont(data)
	if err != nil {
		return nil, fmt.Errorf("load the tab font %s: %w", path, err)
	}
	return f, nil
}

func configPath(getenv func(string) string) (string, error) {
	dir := getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(dir) {
		home := getenv("HOME")
		if !filepath.IsAbs(home) {
			return "", errors.New("neither XDG_CONFIG_HOME nor HOME is an absolute path; pass --config")
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "lys-wm", "config.yaml"), nil
}

type source struct {
	path     string
	explicit bool
	lib      *xkb.Library
	getenv   func(string) string
}

func (s source) read() (string, []byte, error) {
	data, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		return s.path, data, nil
	case errors.Is(err, fs.ErrNotExist) && !s.explicit:
		fmt.Fprintf(os.Stderr, "lys-wm: no config at %s; using the built-in one\n", s.path)
		return "built-in config", config.Default, nil
	}
	return "", nil, fmt.Errorf("read the config: %w", err)
}

type loaded struct {
	cfg       compositor.Config
	keymap    *xkb.Keymap
	env       [][2]string
	autostart []string
	input     session.InputConfig
	xwayland  bool
	zero      bool
	gpu       string
}

func (s source) load() (loaded, error) {
	name, data, err := s.read()
	if err != nil {
		return loaded{}, err
	}
	file, err := config.Parse(name, data)
	if err != nil {
		return loaded{}, err
	}
	font, err := loadFont(fontPath(file.Font, s.getenv))
	if err != nil {
		return loaded{}, err
	}
	names := file.Keyboard
	if names == (xkb.Names{}) {
		names = xkb.NamesFromEnv(s.getenv)
	}
	keymap, err := s.lib.Compile(names)
	if err != nil {
		return loaded{}, err
	}
	cfg, err := file.Compositor(keymap, font)
	if err != nil {
		return loaded{}, errors.Join(err, keymap.Close())
	}
	return loaded{cfg: cfg, keymap: keymap, env: file.Env, autostart: file.Autostart, input: file.Input, xwayland: file.Xwayland, zero: file.ZeroScaling, gpu: file.GPU}, nil
}

func sessionVars(env [][2]string, getenv func(string) string) [][2]string {
	out := slices.Clone(env)
	for _, name := range []string{"WAYLAND_DISPLAY", "DISPLAY", ipc.SocketEnv} {
		if v := getenv(name); v != "" {
			out = append(out, [2]string{name, v})
		}
	}
	return out
}

type reloader struct {
	source
	cursor         compositor.CursorConfig
	comp           *compositor.Compositor
	exit           func() error
	keymap         *xkb.Keymap
	configureInput func(session.InputConfig) error
}

func (s *reloader) reload() error {
	next, err := s.load()
	if err != nil {
		return err
	}
	next.cfg.Reload, next.cfg.Exit, next.cfg.Cursor = s.reload, s.exit, s.cursor
	if err := s.comp.Reconfigure(next.cfg); err != nil {
		return errors.Join(err, next.keymap.Close())
	}
	old := s.keymap
	s.keymap = next.keymap
	return errors.Join(old.Close(), s.configureInput(next.input))
}

func withEnv(env [][2]string, getenv func(string) string) func(string) string {
	return func(name string) string {
		if i := slices.IndexFunc(env, func(kv [2]string) bool { return kv[0] == name }); i >= 0 {
			return env[i][1]
		}
		return getenv(name)
	}
}

func cursorConfig(getenv func(string) string) (compositor.CursorConfig, error) {
	cfg := compositor.CursorConfig{Size: compositor.DefaultCursorSize}
	if v := getenv("XCURSOR_SIZE"); v != "" {
		size, err := strconv.Atoi(v)
		if err != nil || size <= 0 {
			return cfg, fmt.Errorf("XCURSOR_SIZE %q is not a positive integer", v)
		}
		cfg.Size = size
	}
	name := getenv("XCURSOR_THEME")
	if name == "" {
		name = "default"
	}
	theme, err := xcursor.Open(name, xcursor.SearchPath(getenv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "lys-wm: %v; drawing the built-in arrow\n", err)
		return cfg, nil
	}
	cfg.Theme = theme
	return cfg, nil
}

func run(args []string) (err error) {
	flags := flag.NewFlagSet("lys-wm", flag.ContinueOnError)
	path := flags.String("config", "", "config file (default: $XDG_CONFIG_HOME/lys-wm/config.yaml)")
	withSystemd := flags.Bool("systemd", false, "export the session environment to D-Bus activation and the systemd user manager, and run "+systemd.Target+" while lys-wm runs")
	rendererName := flags.String("renderer", "", "gles or soft (default: gles for the nested backend, soft for headless)")
	requested := flags.String("backend", "", "output and input backend: drm, nested or headless (default: nested when WAYLAND_DISPLAY is set, else drm)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	kind, err := chooseBackend(*requested, os.Getenv("WAYLAND_DISPLAY"))
	if err != nil {
		return err
	}
	renderer, err := chooseRenderer(*rendererName, kind)
	if err != nil {
		return err
	}
	dbg, err := parseDebug(os.Getenv("LYS_WM_DEBUG"))
	if err != nil {
		return err
	}
	var trace io.Writer
	if dbg.wire {
		trace = os.Stderr
	}
	lib, err := xkb.Load(xkb.LibraryPath(os.Getenv))
	if err != nil {
		return err
	}
	defer lib.Close()
	s := &reloader{path: *path, explicit: *path != "", lib: lib, getenv: os.Getenv}
	if !s.explicit {
		if s.path, err = configPath(os.Getenv); err != nil {
			return err
		}
	}
	first, err := s.load()
	if err != nil {
		return err
	}
	s.keymap = first.keymap
	defer func() { err = errors.Join(err, s.keymap.Close()) }()
	if s.cursor, err = cursorConfig(withEnv(first.env, os.Getenv)); err != nil {
		return err
	}
	cfg := first.cfg
	cfg.Cursor = s.cursor
	cfg.Reload = s.reload
	for _, kv := range first.env {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return fmt.Errorf("set %s from the config: %w", kv[0], err)
		}
	}
	l, err := loop.New()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, l.Close()) }()
	s.exit = func() error {
		l.Stop()
		return nil
	}
	cfg.Exit = s.exit
	if _, err := l.AddSignals(func(sig os.Signal) error {
		if sig != syscall.SIGHUP {
			l.Stop()
			return nil
		}
		if err := s.reload(); err != nil {
			fmt.Fprintln(os.Stderr, "lys-wm: reload:", err)
		}
		return nil
	}, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP); err != nil {
		return err
	}
	b, err := openBackend(kind, renderer, l, first.input, first.gpu, cfg.Report)
	if err != nil {
		return err
	}
	cfg.SwitchVT, s.configureInput = b.switchVT, b.configureInput
	defer func() { err = errors.Join(err, b.Close()) }()
	display, err := server.New(l, server.Options{RuntimeDir: os.Getenv("XDG_RUNTIME_DIR"), Trace: trace, Report: cfg.Report})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, display.Close()) }()
	if s.comp, err = compositor.New(l, display, b, cfg); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.comp.Close()) }()
	socket, err := ipc.SocketPath(os.Getenv("XDG_RUNTIME_DIR"), display.Name())
	if err != nil {
		return err
	}
	srv, err := ipc.Listen(l, socket, s.comp, cfg.Report)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, srv.Close()) }()
	s.comp.OnDispatch(srv.Dispatched)
	exports := [][2]string{{"WAYLAND_DISPLAY", display.Name()}, {ipc.SocketEnv, socket}}
	if first.xwayland {
		var xw *xwayland.Server
		xw, err = xwayland.Listen(l, display, xwayland.Options{Binary: xwayland.Path(os.Getenv), Dir: "/tmp", Env: os.Environ(), Log: os.Stderr, Report: cfg.Report, Listener: s.comp, TransferTimeout: 30 * time.Second})
		if err != nil {
			return fmt.Errorf("%w (or turn it off with xwayland.enabled: false)", err)
		}
		defer func() { err = errors.Join(err, xw.Close()) }()
		s.comp.WatchSelection(xw.SelectionChanged)
		if first.zero {
			if err := s.comp.ScaleXwayland(xw.IsClient); err != nil {
				return err
			}
		}
		exports = append(exports, [2]string{"DISPLAY", xw.Display()})
	}
	for _, kv := range exports {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return fmt.Errorf("export %s: %w", kv[0], err)
		}
	}
	if *withSystemd {
		sess, startErr := systemd.Start(os.Getenv("DBUS_SESSION_BUS_ADDRESS"), sessionVars(first.env, os.Getenv))
		if startErr != nil {
			cfg.Report(fmt.Errorf("start %s: %w", systemd.Target, startErr))
		} else {
			defer func() { err = errors.Join(err, sess.Close()) }()
		}
	}
	for _, command := range first.autostart {
		if err := s.comp.Dispatch(wm.ExecAction{Command: command}); err != nil {
			cfg.Report(fmt.Errorf("autostart: %w", err))
		}
	}
	listening := display.Name()
	if first.xwayland {
		listening += " and X display " + os.Getenv("DISPLAY")
	}
	fmt.Fprintf(os.Stderr, "lys-wm: listening on %s with the %s backend\n", listening, kind)
	if err := l.Run(); !errors.Is(err, nested.ErrClosed) {
		return err
	}
	return nil
}
