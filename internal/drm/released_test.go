package drm

import (
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReleasingOnAGoneDeviceSucceeds(t *testing.T) {
	if err := released(fmt.Errorf("drm: DRM_IOCTL_MODE_RMFB on /dev/dri/card1: %w", unix.ENODEV)); err != nil {
		t.Fatalf("a release on an unplugged device failed: %v", err)
	}
	busy := fmt.Errorf("drm: DRM_IOCTL_MODE_RMFB on /dev/dri/card1: %w", unix.EBUSY)
	if err := released(busy); !errors.Is(err, unix.EBUSY) {
		t.Fatalf("a real release failure became %v", err)
	}
	if err := released(nil); err != nil {
		t.Fatal(err)
	}
}
