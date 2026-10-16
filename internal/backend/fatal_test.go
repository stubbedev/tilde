package backend_test

import (
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/gpu"
)

func TestOnlyALostDeviceOrABrokenInvariantIsFatal(t *testing.T) {
	for _, tc := range []struct {
		err   error
		fatal bool
	}{
		{fmt.Errorf("drm: commit the frame: %w", unix.ENODEV), true},
		{fmt.Errorf("gles: render pass: %w", &gpu.EGLError{Op: "eglMakeCurrent", Code: 0x300E}), true},
		{fmt.Errorf("gles: render pass: %w", &gpu.GLError{Op: "draw", Code: 0x0507}), true},
		{fmt.Errorf("%w: nested: host gone", backend.ErrDeviceLost), true},
		{backend.Broken("present without BeginFrame"), true},
		{fmt.Errorf("drm: commit the frame: %w", unix.EBUSY), false},
		{fmt.Errorf("drm: test the frame: %w", unix.EINVAL), false},
		{fmt.Errorf("drm: commit the frame: %w", unix.EACCES), false},
		{fmt.Errorf("gles: render pass: %w", &gpu.GLError{Op: "draw", Code: 0x0505}), false},
		{fmt.Errorf("gles: render pass: %w", &gpu.EGLError{Op: "eglMakeCurrent", Code: 0x3002}), false},
		{errors.New("compositor: tab label: no glyphs"), false},
	} {
		if got := backend.Fatal(tc.err); got != tc.fatal {
			t.Errorf("Fatal(%v) = %v, want %v", tc.err, got, tc.fatal)
		}
	}
}
