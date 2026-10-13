// Package testenv decides whether tests that need tools or libraries from the
// dev shell skip or fail when they are missing.
package testenv

import (
	"os"
	"os/exec"
	"testing"

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
