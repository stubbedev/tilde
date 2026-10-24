// Package testenv decides whether tests that need tools or libraries from the
// dev shell skip or fail when they are missing, and gives tests directories
// short enough to hold Unix sockets.
package testenv

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gtext "github.com/stubbedev/lys-ui/render"

	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/xcursor"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

const strictEnv = "LYS_WM_HEADLESS"

func missing(t testing.TB, format string, args ...any) {
	t.Helper()
	if os.Getenv(strictEnv) == "1" {
		t.Fatalf(format+"; "+strictEnv+"=1 forbids skipping", args...)
	}
	t.Skipf(format, args...)
}

func Require(t testing.TB, binary string) string {
	t.Helper()
	path, err := exec.LookPath(binary)
	if err != nil {
		missing(t, "%s is not on PATH", binary)
	}
	return path
}

func GPU(t testing.TB) *gpu.Library {
	t.Helper()
	l, err := gpu.Load(gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		missing(t, "%v", err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	})
	return l
}

func XKBLibrary(t testing.TB) *xkb.Library {
	t.Helper()
	l, err := xkb.Load(xkb.LibraryPath(os.Getenv))
	if err != nil {
		missing(t, "%v", err)
	}
	t.Cleanup(l.Close)
	return l
}

func Keymap(t testing.TB) *xkb.Keymap {
	t.Helper()
	km, err := XKBLibrary(t).Compile(xkb.NamesFromEnv(os.Getenv))
	if err != nil {
		missing(t, "no keymap: %v", err)
	}
	t.Cleanup(func() { _ = km.Close() })
	return km
}

func CursorTheme(t testing.TB) *xcursor.Theme {
	t.Helper()
	path := os.Getenv("LYS_WM_TEST_XCURSOR_PATH")
	if path == "" {
		missing(t, "LYS_WM_TEST_XCURSOR_PATH is not set")
	}
	theme, err := xcursor.Open("Vanilla-DMZ", []string{path})
	if err != nil {
		missing(t, "no test cursor theme: %v", err)
	}
	return theme
}

func Font(t testing.TB) *gtext.Typeface {
	t.Helper()
	path := os.Getenv("LYS_WM_FONT")
	if path == "" {
		missing(t, "LYS_WM_FONT is not set")
	}
	data, err := os.ReadFile(path) //nolint:gosec // the test font path comes from the dev shell
	if err != nil {
		missing(t, "no test font: %v", err)
	}
	f, err := gtext.LoadFont(data)
	if err != nil {
		t.Fatalf("testenv: load %s: %v", path, err)
	}
	return f
}

const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

func SocketDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lys-wm-")
	if err != nil {
		t.Fatalf("testenv: socket dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("testenv: remove socket dir: %v", err)
		}
	})
	return dir
}

func DBus(t testing.TB) string {
	t.Helper()
	daemon := Require(t, "dbus-daemon")
	dir := SocketDir(t)
	config := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(config, fmt.Appendf(nil, busConfig, filepath.Join(dir, "bus")), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), daemon, "--config-file="+config, "--nofork", "--print-address") //nolint:gosec // the dev shell's dbus-daemon with a config written here
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	address, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("dbus-daemon printed no address: %v", err)
	}
	return strings.TrimSpace(address)
}
