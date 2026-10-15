package gles_test

import (
	"image"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/render/gles"
)

func TestAStackOnARenderNodeDraws(t *testing.T) {
	fd, err := unix.Open("/dev/dri/renderD128", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("no render node here: %v", err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	s, err := gles.OnGBM(fd, gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		t.Skipf("no GBM and EGL on the render node: %v", err)
	}
	fb, err := s.Renderer.NewFramebuffer(image.Pt(4, 4))
	if err != nil {
		t.Fatal(err)
	}
	if err := fb.Destroy(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAStackOnNoDeviceFailsWhole(t *testing.T) {
	if s, err := gles.OnGBM(-1, gpu.PathsFromEnv(os.Getenv)); err == nil || s != nil {
		t.Fatalf("a GBM stack on fd -1: %v, %v", s, err)
	}
}
