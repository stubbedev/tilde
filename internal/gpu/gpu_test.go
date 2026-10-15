package gpu_test

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/fourcc"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/testenv"
)

func surfaceless(t *testing.T) (*gpu.Display, *gpu.GL) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	d, err := testenv.GPU(t).Surfaceless()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MakeCurrent(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d, d.GL()
}

func TestMissingLibrariesNameTheirVariable(t *testing.T) {
	paths := gpu.PathsFromEnv(os.Getenv)
	paths.EGL = "/nonexistent/libEGL.so.1"
	if _, err := gpu.Load(paths); err == nil || !strings.Contains(err.Error(), gpu.EGLEnv) {
		t.Fatalf("a missing libEGL: %v", err)
	}
	paths = gpu.PathsFromEnv(os.Getenv)
	paths.GBM = "/nonexistent/libgbm.so.1"
	if _, err := gpu.LoadGBM(paths); err == nil || !strings.Contains(err.Error(), gpu.GBMEnv) {
		t.Fatalf("a missing libgbm: %v", err)
	}
	env := map[string]string{gpu.EGLEnv: "/x/libEGL.so.1"}
	if got := gpu.PathsFromEnv(func(k string) string { return env[k] }); got.EGL != "/x/libEGL.so.1" || got.GLESv2 != "libGLESv2.so.2" || got.GBM != "libgbm.so.1" {
		t.Fatalf("paths %+v", got)
	}
}

func framebuffer(t *testing.T, gl *gpu.GL, size image.Point) {
	t.Helper()
	var tex, fb uint32
	gl.GenTextures(1, &tex)
	gl.BindTexture(gpu.Texture2D, tex)
	gl.TexImage2D(gpu.Texture2D, gpu.RGBA, int32(size.X), int32(size.Y), gpu.RGBA, nil)
	gl.GenFramebuffers(1, &fb)
	gl.BindFramebuffer(gpu.Framebuffer, fb)
	gl.FramebufferTexture2D(gpu.Framebuffer, gpu.ColorAttachment0, gpu.Texture2D, tex, 0)
	if status := gl.CheckFramebufferStatus(gpu.Framebuffer); status != gpu.FramebufferComplete {
		t.Fatalf("framebuffer status %#x", status)
	}
	gl.Viewport(0, 0, int32(size.X), int32(size.Y))
	t.Cleanup(func() {
		gl.DeleteFramebuffers(1, &fb)
		gl.DeleteTextures(1, &tex)
	})
}

func pixel(gl *gpu.GL, x, y int32) [4]byte {
	var px [4]byte
	gl.ReadPixels(x, y, 1, 1, gpu.RGBA, px[:])
	return px
}

func near(a, b [4]byte) bool {
	for i := range a {
		if d := int(a[i]) - int(b[i]); d < -1 || d > 1 {
			return false
		}
	}
	return true
}

func TestSurfacelessContextClearsAFramebuffer(t *testing.T) {
	d, gl := surfaceless(t)
	if !strings.Contains(gl.Version(), "OpenGL ES") || d.Vendor() == "" {
		t.Fatalf("version %q, vendor %q", gl.Version(), d.Vendor())
	}
	framebuffer(t, gl, image.Pt(4, 4))
	gl.ClearColor(0.25, 0.5, 0.75, 1)
	gl.Clear(gpu.ColorBufferBit)
	if got := pixel(gl, 2, 2); !near(got, [4]byte{64, 128, 191, 255}) {
		t.Fatalf("cleared pixel %v", got)
	}
	if err := gl.Check("clear and read back"); err != nil {
		t.Fatal(err)
	}
}

const (
	vertex = `attribute vec2 pos;
void main() { gl_Position = vec4(pos * 2.0 - 1.0, 0.0, 1.0); }`
	fragment = `precision mediump float;
uniform vec4 color;
void main() { gl_FragColor = color; }`
)

func TestShadersDrawFromABuffer(t *testing.T) {
	_, gl := surfaceless(t)
	framebuffer(t, gl, image.Pt(8, 8))
	gl.ClearColor(0, 0, 0, 1)
	gl.Clear(gpu.ColorBufferBit)
	program, err := gl.Link(vertex, fragment)
	if err != nil {
		t.Fatal(err)
	}
	defer gl.DeleteProgram(program)
	gl.UseProgram(program)
	gl.Uniform4f(gl.GetUniformLocation(program, "color"), 1, 0, 0, 1)
	var vbo uint32
	gl.GenBuffers(1, &vbo)
	defer gl.DeleteBuffers(1, &vbo)
	gl.BindBuffer(gpu.ArrayBuffer, vbo)
	gl.BufferData(gpu.ArrayBuffer, []float32{0, 0, 0.5, 0, 0, 1, 0.5, 1}, gpu.StaticDraw)
	pos := uint32(gl.GetAttribLocation(program, "pos"))
	gl.VertexAttribPointer(pos, 2, gpu.Float, 0, 0, 0)
	gl.EnableVertexAttribArray(pos)
	gl.DrawArrays(gpu.TriangleStrip, 0, 4)
	if left, right := pixel(gl, 1, 4), pixel(gl, 6, 4); left != [4]byte{255, 0, 0, 255} || right != [4]byte{0, 0, 0, 255} {
		t.Fatalf("left half %v, right half %v", left, right)
	}
	if err := gl.Check("draw"); err != nil {
		t.Fatal(err)
	}
	if _, err := gl.Link(vertex, "void main() { nonsense; }"); err == nil || !strings.Contains(err.Error(), "compile shader") {
		t.Fatalf("a broken shader: %v", err)
	}
}

func TestDevicesOpenDisplays(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	lib := testenv.GPU(t)
	devices, err := lib.Devices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) == 0 {
		t.Fatal("EGL lists no devices")
	}
	opened := 0
	for _, dev := range devices {
		d, err := lib.OnDevice(dev)
		if err != nil {
			if dev.Software() {
				t.Errorf("the software device: %v", err)
			}
			t.Logf("device %q, which another vendor may drive: %v", dev.RenderNode(), err)
			continue
		}
		opened++
		if err := d.MakeCurrent(); err != nil {
			t.Error(err)
		}
		t.Logf("device %q: %s on %s", dev.RenderNode(), d.GL().Renderer(), d.Vendor())
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	}
	if opened == 0 {
		t.Fatal("no EGL device opened a display")
	}
}

func renderNode(t *testing.T) int {
	t.Helper()
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil || len(nodes) == 0 {
		t.Skip("no DRM render node")
	}
	fd, err := unix.Open(nodes[0], unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("open %s: %v", nodes[0], err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

func TestGBMAllocatesExportableBuffers(t *testing.T) {
	fd := renderNode(t)
	lib, err := gpu.LoadGBM(gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	dev, err := lib.Open(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	b, err := dev.Allocate(image.Pt(64, 32), fourcc.XRGB8888, nil, gpu.UseRendering)
	if err != nil {
		t.Fatalf("%s: %v", dev.Backend(), err)
	}
	if len(b.Planes) == 0 || b.Planes[0].FD < 0 || b.Planes[0].Stride < 64*4 || b.Size != image.Pt(64, 32) {
		t.Fatalf("buffer %+v", b.DMABUF)
	}
	t.Logf("%s: modifier %#x, planes %+v", dev.Backend(), b.Modifier, b.Planes)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dev.Allocate(image.Pt(0, 4), fourcc.XRGB8888, nil, gpu.UseRendering); err == nil {
		t.Fatal("an empty buffer was allocated")
	}
}

func TestUnsupportedExtensionsFailPrecisely(t *testing.T) {
	d, _ := surfaceless(t)
	for _, ext := range []string{"EGL_EXT_image_dma_buf_import", "EGL_ANDROID_native_fence_sync"} {
		if d.Has(ext) {
			continue
		}
		var err error
		switch ext {
		case "EGL_EXT_image_dma_buf_import":
			_, err = d.ImportDMABUF(gpu.DMABUF{Size: image.Pt(1, 1), Format: fourcc.XRGB8888, Planes: []gpu.Plane{{FD: 0, Stride: 4}}})
		case "EGL_ANDROID_native_fence_sync":
			_, err = d.Fence()
		}
		if !errors.Is(err, gpu.ErrUnsupported) || !strings.Contains(err.Error(), ext) {
			t.Errorf("%s: %v", ext, err)
		}
	}
}

func hardwareNode(t *testing.T, lib *gpu.Library) string {
	t.Helper()
	devices, err := lib.Devices()
	if err != nil {
		t.Fatal(err)
	}
	for _, dev := range devices {
		if node := dev.RenderNode(); node != "" && !dev.Software() {
			return node
		}
	}
	t.Skip("no EGL vendor drives a render node here; unset __EGL_VENDOR_LIBRARY_DIRS on a GPU machine to run this")
	return ""
}

func TestGBMDisplayRendersIntoADMABUF(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	lib := testenv.GPU(t)
	node := hardwareNode(t, lib)
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	gbmLib, err := gpu.LoadGBM(gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		t.Fatal(err)
	}
	defer gbmLib.Close()
	dev, err := gbmLib.Open(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	d, err := lib.OnGBM(dev)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.MakeCurrent(); err != nil {
		t.Fatal(err)
	}
	gl := d.GL()
	t.Logf("%s on %s through %s", gl.Renderer(), d.Vendor(), dev.Backend())
	formats, err := d.DMABUFFormats()
	if err != nil || len(formats) == 0 {
		t.Fatalf("dmabuf formats %v: %v", formats, err)
	}
	b, err := dev.Allocate(image.Pt(16, 16), fourcc.XRGB8888, nil, gpu.UseRendering)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	img, err := d.ImportDMABUF(b.DMABUF)
	if err != nil {
		t.Fatal(err)
	}
	defer img.Destroy()
	var rb, fb uint32
	gl.GenRenderbuffers(1, &rb)
	gl.BindRenderbuffer(gpu.Renderbuffer, rb)
	gl.EGLImageTargetRenderbuffer(img)
	gl.GenFramebuffers(1, &fb)
	gl.BindFramebuffer(gpu.Framebuffer, fb)
	gl.FramebufferRenderbuffer(gpu.Framebuffer, gpu.ColorAttachment0, gpu.Renderbuffer, rb)
	if status := gl.CheckFramebufferStatus(gpu.Framebuffer); status != gpu.FramebufferComplete {
		t.Fatalf("dmabuf framebuffer status %#x", status)
	}
	gl.Viewport(0, 0, 16, 16)
	gl.ClearColor(1, 0.5, 0, 1)
	gl.Clear(gpu.ColorBufferBit)
	if got := pixel(gl, 8, 8); !near(got, [4]byte{255, 128, 0, 255}) {
		t.Fatalf("dmabuf pixel %v", got)
	}
	if d.Has("EGL_ANDROID_native_fence_sync") {
		fence, err := d.Fence()
		if err != nil {
			t.Fatal(err)
		}
		fenceFD, err := fence.FD()
		if err != nil {
			t.Fatal(err)
		}
		imported, err := d.ImportFence(fenceFD)
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(imported.Wait(), imported.Destroy(), fence.Destroy()); err != nil {
			t.Fatal(err)
		}
	}
	gl.DeleteFramebuffers(1, &fb)
	gl.DeleteRenderbuffers(1, &rb)
	if err := gl.Check("render into a dmabuf"); err != nil {
		t.Fatal(err)
	}
}

func TestDisplaysOnOneDeviceShareItsTermination(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	lib := testenv.GPU(t)
	a, err := lib.Surfaceless()
	if err != nil {
		t.Fatal(err)
	}
	b, err := lib.Surfaceless()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeCurrent(); err != nil {
		t.Fatalf("closing one display ended the other: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}
