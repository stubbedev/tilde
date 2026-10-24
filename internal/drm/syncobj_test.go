package drm_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/drm"
)

func device(t *testing.T) *drm.Device {
	t.Helper()
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil || len(nodes) == 0 {
		t.Skip("no DRM render node")
	}
	d, err := drm.Open(nodes[0])
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	if v, err := d.Cap(drm.CapSyncobjTimeline); err != nil || v == 0 {
		t.Skipf("%s has no timeline syncobjs: %v", nodes[0], err)
	}
	return d
}

func signaled(t *testing.T, efd int, within time.Duration) bool {
	t.Helper()
	fds := []unix.PollFd{{Fd: int32(efd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(within/time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestTimelinePointsSignalEventFDs(t *testing.T) {
	d := device(t)
	s, err := d.CreateSyncobj(false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DestroySyncobj(s)
	fd, err := d.ExportSyncobj(s)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := d.ImportSyncobj(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DestroySyncobj(imported)
	if err := unix.Close(fd); err != nil {
		t.Fatal(err)
	}
	efd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(efd)
	if err := d.EventFD(imported, 3, efd, false); err != nil {
		t.Fatal(err)
	}
	if signaled(t, efd, 50*time.Millisecond) {
		t.Fatal("point 3 signalled before anything signalled it")
	}
	if err := d.Signal(s, 2); err != nil {
		t.Fatal(err)
	}
	if signaled(t, efd, 50*time.Millisecond) {
		t.Fatal("point 2 woke a wait for point 3")
	}
	if err := d.Signal(s, 3); err != nil {
		t.Fatal(err)
	}
	if !signaled(t, efd, time.Second) {
		t.Fatal("point 3 never woke the eventfd")
	}
	if v, err := d.Query(imported); err != nil || v != 3 {
		t.Fatalf("timeline at %d, %v", v, err)
	}

	sync, err := d.ExportSyncFile(s, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ImportSyncFile(s, 5, sync); err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(sync); err != nil {
		t.Fatal(err)
	}
	if v, err := d.Query(s); err != nil || v != 5 {
		t.Fatalf("a signalled sync file at point 5 left the timeline at %d, %v", v, err)
	}
	if _, err := d.ImportSyncobj(-1); err == nil || !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EBADF) {
		t.Fatalf("importing fd -1: %v", err)
	}
}
