package harness

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
)

func failingPresents(t *testing.T, err error) (*Harness, *reports) {
	t.Helper()
	got := &reports{}
	h := StartWith(t, func(cfg *compositor.Config) { cfg.Report = got.report }, big)
	h.Eventually("the first frame", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Presents() > 0
	})
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		b.Outputs()[0].FailPresents(err)
		return c.OutputDamaged(b.Outputs()[0])
	})
	return h, got
}

func TestAFailedFrameIsDroppedAndRetried(t *testing.T) {
	h, got := failingPresents(t, fmt.Errorf("commit: %w", unix.EBUSY))
	var failed uint64
	h.Eventually("frames failing", func(b *headless.Backend, _ *compositor.Compositor) bool {
		failed = b.Outputs()[0].Frames()
		return len(got.errs) > 0 && failed > 3
	})
	errs := got.take()
	if len(errs) != 1 || !errors.Is(errs[0], unix.EBUSY) || !strings.Contains(errs[0].Error(), "dropping frames until one draws") {
		t.Fatalf("reported %v, want one report of the dropped frames", errs)
	}
	var presents uint64
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		presents = b.Outputs()[0].Presents()
		b.Outputs()[0].FailPresents(nil)
		return nil
	})
	h.Eventually("a frame presented again", func(b *headless.Backend, _ *compositor.Compositor) bool {
		return b.Outputs()[0].Presents() > presents
	})
	if errs := got.take(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "HEADLESS-1 draws again after") {
		t.Fatalf("reported %v, want one report of the recovery", errs)
	}
}

func TestALostDeviceOrABrokenInvariantEndsTheSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"ENODEV", fmt.Errorf("commit: %w", unix.ENODEV)},
		{"device lost", fmt.Errorf("%w: host gone", backend.ErrDeviceLost)},
		{"invariant", backend.Broken("a frame in progress twice")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, got := failingPresents(t, tc.err)
			if err := h.Ended(); !errors.Is(err, tc.err) {
				t.Fatalf("the loop ended with %v, want %v", err, tc.err)
			}
			if errs := got.take(); len(errs) != 0 {
				t.Fatalf("reported %v; a fatal error ends the session without a report", errs)
			}
		})
	}
}
