// Command tilde is a pure-Go tiling Wayland compositor.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	gtext "github.com/stubbedev/gelm/render"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/backend/nested"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wlclient"
	"github.com/stubbedev/tilde/internal/xcursor"
	"github.com/stubbedev/tilde/internal/xkb"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tilde:", err)
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
			return debug{}, fmt.Errorf("TILDE_DEBUG: unknown category %q (known: wire)", category)
		}
	}
	return d, nil
}

var headlessMode = backend.Mode{Size: image.Pt(1920, 1080), RefreshMHz: 60000, Preferred: true}

func chooseBackend(requested, waylandDisplay string) (string, error) {
	switch requested {
	case "":
		if waylandDisplay != "" {
			return "nested", nil
		}
		return "headless", nil
	case "nested", "headless":
		return requested, nil
	}
	return "", fmt.Errorf("unknown backend %q (available: nested, headless)", requested)
}

func openBackend(name string, l *loop.Loop) (backend.Backend, error) {
	if name == "nested" {
		host, err := wlclient.SocketPath(os.Getenv)
		if err != nil {
			return nil, err
		}
		return nested.New(l, host)
	}
	b := headless.New(l)
	if _, err := b.AddOutput(headlessMode); err != nil {
		return nil, errors.Join(err, b.Close())
	}
	return b, nil
}

var defaultFont = "DejaVuSans.ttf"

func loadFont(getenv func(string) string) (*gtext.Typeface, error) {
	path := getenv("TILDE_FONT")
	if path == "" {
		path = defaultFont
	}
	data, err := os.ReadFile(path) //nolint:gosec // the font path comes from the build or the user's environment
	if err != nil {
		return nil, fmt.Errorf("read the tab font (set TILDE_FONT to a font file): %w", err)
	}
	f, err := gtext.LoadFont(data)
	if err != nil {
		return nil, fmt.Errorf("load the tab font %s: %w", path, err)
	}
	return f, nil
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
		fmt.Fprintf(os.Stderr, "tilde: %v; drawing the built-in arrow\n", err)
		return cfg, nil
	}
	cfg.Theme = theme
	return cfg, nil
}

func run() (err error) {
	requested := flag.String("backend", "", "output and input backend: nested or headless (default: nested when WAYLAND_DISPLAY is set, else headless)")
	flag.Parse()
	backendName, err := chooseBackend(*requested, os.Getenv("WAYLAND_DISPLAY"))
	if err != nil {
		return err
	}
	dbg, err := parseDebug(os.Getenv("TILDE_DEBUG"))
	if err != nil {
		return err
	}
	var trace io.Writer
	if dbg.wire {
		trace = os.Stderr
	}
	l, err := loop.New()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, l.Close()) }()
	b, err := openBackend(backendName, l)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, b.Close()) }()
	display, err := server.New(l, server.Options{RuntimeDir: os.Getenv("XDG_RUNTIME_DIR"), Trace: trace})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, display.Close()) }()
	keymap, err := xkb.Open(os.Getenv)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, keymap.Close()) }()
	font, err := loadFont(os.Getenv)
	if err != nil {
		return err
	}
	cfg := compositor.DefaultConfig(keymap, font)
	cfg.Cursor, err = cursorConfig(os.Getenv)
	if err != nil {
		return err
	}
	comp, err := compositor.New(l, display, b, cfg)
	if err != nil {
		return err
	}
	defer comp.Close()
	if err := os.Setenv("WAYLAND_DISPLAY", display.Name()); err != nil {
		return fmt.Errorf("export WAYLAND_DISPLAY: %w", err)
	}
	if _, err := l.AddSignals(func(os.Signal) error {
		l.Stop()
		return nil
	}, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "tilde: listening on %s with the %s backend\n", display.Name(), backendName)
	if err := l.Run(); !errors.Is(err, nested.ErrClosed) {
		return err
	}
	return nil
}
