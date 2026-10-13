// Command tilde is a pure-Go tiling Wayland compositor.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/backend/nested"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wlclient"
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
	if _, err := compositor.New(b); err != nil {
		return err
	}
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
