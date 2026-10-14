// Package testenv decides whether tests that need tools or libraries from the
// dev shell skip or fail when they are missing.
package testenv

import (
	"os"
	"os/exec"
	"testing"

	gtext "github.com/stubbedev/gelm/render"

	"github.com/stubbedev/tilde/internal/xcursor"
	"github.com/stubbedev/tilde/internal/xkb"
)

const strictEnv = "TILDE_HEADLESS"

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

func Keymap(t testing.TB) *xkb.Keymap {
	t.Helper()
	km, err := xkb.Open(os.Getenv)
	if err != nil {
		missing(t, "no keymap: %v", err)
	}
	t.Cleanup(func() { _ = km.Close() })
	return km
}

func CursorTheme(t testing.TB) *xcursor.Theme {
	t.Helper()
	path := os.Getenv("TILDE_TEST_XCURSOR_PATH")
	if path == "" {
		missing(t, "TILDE_TEST_XCURSOR_PATH is not set")
	}
	theme, err := xcursor.Open("Vanilla-DMZ", []string{path})
	if err != nil {
		missing(t, "no test cursor theme: %v", err)
	}
	return theme
}

func Font(t testing.TB) *gtext.Typeface {
	t.Helper()
	path := os.Getenv("TILDE_FONT")
	if path == "" {
		missing(t, "TILDE_FONT is not set")
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
