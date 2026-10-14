package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/ebitengine/purego"
)

const (
	eglSuccess                 = 0x3000
	eglNone                    = 0x3038
	eglExtensions              = 0x3055
	eglVendor                  = 0x3053
	eglOpenGLESAPI             = 0x30A0
	eglContextMajorVersion     = 0x3098
	eglContextPriorityLevelIMG = 0x3100
	eglContextPriorityHighIMG  = 0x3101
	eglPlatformGBM             = 0x31D7
	eglPlatformSurfaceless     = 0x31DD
	eglPlatformDevice          = 0x313F
	eglDeviceEXT               = 0x322C
	eglDRMRenderNodeFile       = 0x3377
	eglLinuxDMABUF             = 0x3270
	eglWidth                   = 0x3057
	eglHeight                  = 0x3056
	eglLinuxDRMFourCC          = 0x3271
	eglImagePreserved          = 0x30D2
	eglTrue                    = 1
	eglSyncNativeFence         = 0x3144
	eglSyncNativeFenceFD       = 0x3145
	eglNoNativeFenceFD         = -1
)

var (
	planeFD       = [4]int32{0x3272, 0x3275, 0x3278, 0x3440}
	planeOffset   = [4]int32{0x3273, 0x3276, 0x3279, 0x3441}
	planePitch    = [4]int32{0x3274, 0x3277, 0x327A, 0x3442}
	planeModLo    = [4]int32{0x3443, 0x3445, 0x3447, 0x3449}
	planeModHi    = [4]int32{0x3444, 0x3446, 0x3448, 0x344A}
	eglErrorNames = map[int32]string{
		0x3001: "EGL_NOT_INITIALIZED", 0x3002: "EGL_BAD_ACCESS", 0x3003: "EGL_BAD_ALLOC",
		0x3004: "EGL_BAD_ATTRIBUTE", 0x3005: "EGL_BAD_CONFIG", 0x3006: "EGL_BAD_CONTEXT",
		0x3007: "EGL_BAD_CURRENT_SURFACE", 0x3008: "EGL_BAD_DISPLAY", 0x3009: "EGL_BAD_MATCH",
		0x300A: "EGL_BAD_NATIVE_PIXMAP", 0x300B: "EGL_BAD_NATIVE_WINDOW", 0x300C: "EGL_BAD_PARAMETER",
		0x300D: "EGL_BAD_SURFACE", 0x300E: "EGL_CONTEXT_LOST",
	}
)

var ErrUnsupported = errors.New("gpu: unsupported")

type EGLError struct {
	Op   string
	Code int32
}

func (e *EGLError) Error() string {
	name, ok := eglErrorNames[e.Code]
	if !ok {
		name = fmt.Sprintf("0x%x", e.Code)
	}
	return fmt.Sprintf("gpu: %s: %s", e.Op, name)
}

type eglFuncs struct {
	getError           func() int32
	getProcAddress     func(name string) uintptr
	queryString        func(dpy uintptr, name int32) string
	initialize         func(dpy uintptr, major, minor *int32) uint32
	terminate          func(dpy uintptr) uint32
	bindAPI            func(api uint32) uint32
	createContext      func(dpy, config, share uintptr, attribs *int32) uintptr
	destroyContext     func(dpy, ctx uintptr) uint32
	makeCurrent        func(dpy, draw, read, ctx uintptr) uint32
	getPlatformDisplay func(platform uint32, native uintptr, attribs *int32) uintptr
	queryDevices       func(max int32, devices *uintptr, num *int32) uint32
	queryDeviceString  func(dev uintptr, name int32) string
	queryDisplayAttrib func(dpy uintptr, attr int32, value *uintptr) uint32
	createImage        func(dpy, ctx uintptr, target uint32, buffer uintptr, attribs *int32) uintptr
	destroyImage       func(dpy, image uintptr) uint32
	queryDMABUFFormats func(dpy uintptr, max int32, formats *int32, num *int32) uint32
	queryDMABUFMods    func(dpy uintptr, format, max int32, modifiers *uint64, externalOnly *uint32, num *int32) uint32
	createSync         func(dpy uintptr, typ uint32, attribs *int32) uintptr
	destroySync        func(dpy, sync uintptr) uint32
	dupNativeFenceFD   func(dpy, sync uintptr) int32
	waitSync           func(dpy, sync uintptr, flags int32) int32
}

type Library struct {
	egl        eglFuncs
	gl         GL
	clientExts []string
	eglLib     uintptr
	glesLib    uintptr
}

func Load(paths Paths) (*Library, error) {
	eglLib, err := open(paths.EGL, EGLEnv)
	if err != nil {
		return nil, err
	}
	glesLib, err := open(paths.GLESv2, GLESv2Env)
	if err != nil {
		return nil, errors.Join(err, purego.Dlclose(eglLib))
	}
	l := &Library{eglLib: eglLib, glesLib: glesLib}
	e := &l.egl
	if err := bind(eglLib, paths.EGL, []symbol{
		{&e.getError, "eglGetError"},
		{&e.getProcAddress, "eglGetProcAddress"},
		{&e.queryString, "eglQueryString"},
		{&e.initialize, "eglInitialize"},
		{&e.terminate, "eglTerminate"},
		{&e.bindAPI, "eglBindAPI"},
		{&e.createContext, "eglCreateContext"},
		{&e.destroyContext, "eglDestroyContext"},
		{&e.makeCurrent, "eglMakeCurrent"},
	}); err != nil {
		return nil, errors.Join(err, l.Close())
	}
	for _, x := range []symbol{
		{&e.getPlatformDisplay, "eglGetPlatformDisplayEXT"},
		{&e.queryDevices, "eglQueryDevicesEXT"},
		{&e.queryDeviceString, "eglQueryDeviceStringEXT"},
		{&e.queryDisplayAttrib, "eglQueryDisplayAttribEXT"},
		{&e.createImage, "eglCreateImageKHR"},
		{&e.destroyImage, "eglDestroyImageKHR"},
		{&e.queryDMABUFFormats, "eglQueryDmaBufFormatsEXT"},
		{&e.queryDMABUFMods, "eglQueryDmaBufModifiersEXT"},
		{&e.createSync, "eglCreateSyncKHR"},
		{&e.destroySync, "eglDestroySyncKHR"},
		{&e.dupNativeFenceFD, "eglDupNativeFenceFDANDROID"},
		{&e.waitSync, "eglWaitSyncKHR"},
	} {
		if err := l.proc(x); err != nil {
			return nil, errors.Join(err, l.Close())
		}
	}
	if err := l.gl.load(l, paths.GLESv2); err != nil {
		return nil, errors.Join(err, l.Close())
	}
	l.clientExts = strings.Fields(e.queryString(0, eglExtensions))
	if !slices.Contains(l.clientExts, "EGL_EXT_platform_base") {
		return nil, errors.Join(fmt.Errorf("gpu: %s lacks EGL_EXT_platform_base", paths.EGL), l.Close())
	}
	return l, nil
}

func (l *Library) proc(s symbol) error {
	p := l.egl.getProcAddress(s.name)
	if p == 0 {
		return fmt.Errorf("gpu: eglGetProcAddress(%s) found nothing", s.name)
	}
	purego.RegisterFunc(s.fn, p)
	return nil
}

func (l *Library) Close() error {
	return errors.Join(purego.Dlclose(l.glesLib), purego.Dlclose(l.eglLib))
}

func (l *Library) fail(op string) error {
	return &EGLError{Op: op, Code: l.egl.getError()}
}

func (l *Library) ClientExtensions() []string {
	return slices.Clone(l.clientExts)
}

func (l *Library) requireClient(ext string) error {
	if !slices.Contains(l.clientExts, ext) {
		return fmt.Errorf("%w: EGL client extension %s", ErrUnsupported, ext)
	}
	return nil
}

type Device struct {
	lib *Library
	h   uintptr
}

func (l *Library) Devices() ([]Device, error) {
	if err := l.requireClient("EGL_EXT_device_enumeration"); err != nil {
		return nil, err
	}
	var n int32
	if l.egl.queryDevices(0, nil, &n) != eglTrue {
		return nil, l.fail("eglQueryDevicesEXT")
	}
	handles := make([]uintptr, n)
	if n > 0 && l.egl.queryDevices(n, &handles[0], &n) != eglTrue {
		return nil, l.fail("eglQueryDevicesEXT")
	}
	devices := make([]Device, n)
	for i := range devices {
		devices[i] = Device{lib: l, h: handles[i]}
	}
	return devices, nil
}

func (d Device) Extensions() []string {
	return strings.Fields(d.lib.egl.queryDeviceString(d.h, eglExtensions))
}

func (d Device) Software() bool {
	return slices.Contains(d.Extensions(), "EGL_MESA_device_software")
}

func (d Device) RenderNode() string {
	if !slices.Contains(d.Extensions(), "EGL_EXT_device_drm_render_node") {
		return ""
	}
	return d.lib.egl.queryDeviceString(d.h, eglDRMRenderNodeFile)
}

type Display struct {
	lib  *Library
	dpy  uintptr
	ctx  uintptr
	exts []string
}

func (l *Library) Surfaceless() (*Display, error) {
	if err := l.requireClient("EGL_MESA_platform_surfaceless"); err != nil {
		return nil, err
	}
	return l.display("surfaceless", eglPlatformSurfaceless, 0)
}

func (l *Library) OnDevice(d Device) (*Display, error) {
	if err := l.requireClient("EGL_EXT_platform_device"); err != nil {
		return nil, err
	}
	return l.display("device", eglPlatformDevice, d.h)
}

func (l *Library) OnGBM(g *GBM) (*Display, error) {
	if !slices.Contains(l.clientExts, "EGL_KHR_platform_gbm") && !slices.Contains(l.clientExts, "EGL_MESA_platform_gbm") {
		return nil, fmt.Errorf("%w: EGL client extension EGL_KHR_platform_gbm", ErrUnsupported)
	}
	return l.display("gbm", eglPlatformGBM, g.dev)
}

func (l *Library) display(platform string, kind uint32, native uintptr) (*Display, error) {
	none := []int32{eglNone}
	dpy := l.egl.getPlatformDisplay(kind, native, &none[0])
	if dpy == 0 {
		return nil, l.fail("eglGetPlatformDisplayEXT(" + platform + ")")
	}
	var major, minor int32
	if l.egl.initialize(dpy, &major, &minor) != eglTrue {
		return nil, l.fail("eglInitialize(" + platform + ")")
	}
	d := &Display{lib: l, dpy: dpy, exts: strings.Fields(l.egl.queryString(dpy, eglExtensions))}
	for _, ext := range []string{"EGL_KHR_surfaceless_context", "EGL_KHR_no_config_context"} {
		if !d.Has(ext) {
			return nil, errors.Join(fmt.Errorf("%w: %s EGL display without %s", ErrUnsupported, platform, ext), d.Close())
		}
	}
	if l.egl.bindAPI(eglOpenGLESAPI) != eglTrue {
		return nil, errors.Join(l.fail("eglBindAPI"), d.Close())
	}
	attribs := []int32{eglContextMajorVersion, 2}
	if d.Has("EGL_IMG_context_priority") {
		attribs = append(attribs, eglContextPriorityLevelIMG, eglContextPriorityHighIMG)
	}
	attribs = append(attribs, eglNone)
	if d.ctx = l.egl.createContext(dpy, 0, 0, &attribs[0]); d.ctx == 0 {
		return nil, errors.Join(l.fail("eglCreateContext"), d.Close())
	}
	if err := d.MakeCurrent(); err != nil {
		return nil, errors.Join(err, d.Close())
	}
	return d, nil
}

func (d *Display) Has(ext string) bool {
	return slices.Contains(d.exts, ext)
}

func (d *Display) Extensions() []string {
	return slices.Clone(d.exts)
}

func (d *Display) Vendor() string {
	return d.lib.egl.queryString(d.dpy, eglVendor)
}

func (d *Display) GL() *GL {
	return &d.lib.gl
}

func (d *Display) MakeCurrent() error {
	if d.lib.egl.makeCurrent(d.dpy, 0, 0, d.ctx) != eglTrue {
		return d.lib.fail("eglMakeCurrent")
	}
	return nil
}

func (d *Display) Device() (Device, error) {
	if err := d.lib.requireClient("EGL_EXT_device_query"); err != nil {
		return Device{}, err
	}
	var h uintptr
	if d.lib.egl.queryDisplayAttrib(d.dpy, eglDeviceEXT, &h) != eglTrue {
		return Device{}, d.lib.fail("eglQueryDisplayAttribEXT(EGL_DEVICE_EXT)")
	}
	return Device{lib: d.lib, h: h}, nil
}

func (d *Display) Close() error {
	var errs []error
	if d.ctx != 0 {
		if d.lib.egl.makeCurrent(d.dpy, 0, 0, 0) != eglTrue {
			errs = append(errs, d.lib.fail("eglMakeCurrent(none)"))
		}
		if d.lib.egl.destroyContext(d.dpy, d.ctx) != eglTrue {
			errs = append(errs, d.lib.fail("eglDestroyContext"))
		}
	}
	if d.lib.egl.terminate(d.dpy) != eglTrue {
		errs = append(errs, d.lib.fail("eglTerminate"))
	}
	return errors.Join(errs...)
}

type Modifier struct {
	Modifier     uint64
	ExternalOnly bool
}

func (d *Display) DMABUFFormats() ([]uint32, error) {
	if !d.Has("EGL_EXT_image_dma_buf_import_modifiers") {
		return nil, fmt.Errorf("%w: EGL_EXT_image_dma_buf_import_modifiers", ErrUnsupported)
	}
	var n int32
	if d.lib.egl.queryDMABUFFormats(d.dpy, 0, nil, &n) != eglTrue {
		return nil, d.lib.fail("eglQueryDmaBufFormatsEXT")
	}
	formats := make([]int32, n)
	if n > 0 && d.lib.egl.queryDMABUFFormats(d.dpy, n, &formats[0], &n) != eglTrue {
		return nil, d.lib.fail("eglQueryDmaBufFormatsEXT")
	}
	out := make([]uint32, n)
	for i, f := range formats[:n] {
		out[i] = uint32(f) //nolint:gosec // EGL returns DRM fourccs in an EGLint
	}
	return out, nil
}

func (d *Display) DMABUFModifiers(format uint32) ([]Modifier, error) {
	if !d.Has("EGL_EXT_image_dma_buf_import_modifiers") {
		return nil, fmt.Errorf("%w: EGL_EXT_image_dma_buf_import_modifiers", ErrUnsupported)
	}
	f := int32(format) //nolint:gosec // EGL takes DRM fourccs as an EGLint
	var n int32
	if d.lib.egl.queryDMABUFMods(d.dpy, f, 0, nil, nil, &n) != eglTrue {
		return nil, d.lib.fail("eglQueryDmaBufModifiersEXT")
	}
	if n == 0 {
		return nil, nil
	}
	mods := make([]uint64, n)
	external := make([]uint32, n)
	if d.lib.egl.queryDMABUFMods(d.dpy, f, n, &mods[0], &external[0], &n) != eglTrue {
		return nil, d.lib.fail("eglQueryDmaBufModifiersEXT")
	}
	out := make([]Modifier, n)
	for i := range out {
		out[i] = Modifier{Modifier: mods[i], ExternalOnly: external[i] == eglTrue}
	}
	return out, nil
}

type Image struct {
	d *Display
	h uintptr
}

func (d *Display) ImportDMABUF(b DMABUF) (*Image, error) {
	if !d.Has("EGL_EXT_image_dma_buf_import") {
		return nil, fmt.Errorf("%w: EGL_EXT_image_dma_buf_import", ErrUnsupported)
	}
	if len(b.Planes) == 0 || len(b.Planes) > len(planeFD) {
		return nil, fmt.Errorf("gpu: a dmabuf has 1 to %d planes, not %d", len(planeFD), len(b.Planes))
	}
	withModifier := b.Modifier != ModifierInvalid
	if withModifier && !d.Has("EGL_EXT_image_dma_buf_import_modifiers") {
		return nil, fmt.Errorf("%w: EGL_EXT_image_dma_buf_import_modifiers for modifier %#x", ErrUnsupported, b.Modifier)
	}
	attribs := make([]int32, 0, 7+len(b.Planes)*10+3)
	attribs = append(attribs, eglWidth, int32(b.Size.X), eglHeight, int32(b.Size.Y), eglLinuxDRMFourCC, int32(b.Format)) //nolint:gosec // sizes and fourccs fit EGLint by the dmabuf protocol
	for i, p := range b.Planes {
		attribs = append(attribs, planeFD[i], int32(p.FD), planeOffset[i], int32(p.Offset), planePitch[i], int32(p.Stride)) //nolint:gosec // fds, offsets and strides fit EGLint by the dmabuf protocol
		if withModifier {
			attribs = append(attribs, planeModLo[i], int32(uint32(b.Modifier)), planeModHi[i], int32(uint32(b.Modifier>>32))) //nolint:gosec // EGL splits the 64-bit modifier into two EGLint halves
		}
	}
	attribs = append(attribs, eglImagePreserved, eglTrue, eglNone)
	h := d.lib.egl.createImage(d.dpy, 0, eglLinuxDMABUF, 0, &attribs[0])
	runtime.KeepAlive(attribs)
	if h == 0 {
		return nil, d.lib.fail("eglCreateImageKHR(dmabuf)")
	}
	return &Image{d: d, h: h}, nil
}

func (i *Image) Destroy() error {
	if i.d.lib.egl.destroyImage(i.d.dpy, i.h) != eglTrue {
		return i.d.lib.fail("eglDestroyImageKHR")
	}
	return nil
}

type Sync struct {
	d *Display
	h uintptr
}

func (d *Display) requireFences() error {
	if !d.Has("EGL_ANDROID_native_fence_sync") {
		return fmt.Errorf("%w: EGL_ANDROID_native_fence_sync", ErrUnsupported)
	}
	return nil
}

func (d *Display) Fence() (*Sync, error) {
	if err := d.requireFences(); err != nil {
		return nil, err
	}
	attribs := []int32{eglNone}
	h := d.lib.egl.createSync(d.dpy, eglSyncNativeFence, &attribs[0])
	if h == 0 {
		return nil, d.lib.fail("eglCreateSyncKHR(native fence)")
	}
	d.lib.gl.Flush()
	return &Sync{d: d, h: h}, nil
}

func (d *Display) ImportFence(fd int) (*Sync, error) {
	if err := d.requireFences(); err != nil {
		return nil, err
	}
	attribs := []int32{eglSyncNativeFenceFD, int32(fd), eglNone} //nolint:gosec // file descriptors fit an EGLint
	h := d.lib.egl.createSync(d.dpy, eglSyncNativeFence, &attribs[0])
	if h == 0 {
		return nil, d.lib.fail("eglCreateSyncKHR(import fence fd)")
	}
	return &Sync{d: d, h: h}, nil
}

func (s *Sync) FD() (int, error) {
	fd := s.d.lib.egl.dupNativeFenceFD(s.d.dpy, s.h)
	if fd == eglNoNativeFenceFD {
		return -1, s.d.lib.fail("eglDupNativeFenceFDANDROID")
	}
	return int(fd), nil
}

func (s *Sync) Wait() error {
	if !s.d.Has("EGL_KHR_wait_sync") {
		return fmt.Errorf("%w: EGL_KHR_wait_sync", ErrUnsupported)
	}
	if s.d.lib.egl.waitSync(s.d.dpy, s.h, 0) != eglTrue {
		return s.d.lib.fail("eglWaitSyncKHR")
	}
	return nil
}

func (s *Sync) Destroy() error {
	if s.d.lib.egl.destroySync(s.d.dpy, s.h) != eglTrue {
		return s.d.lib.fail("eglDestroySyncKHR")
	}
	return nil
}

func (i *Image) handle() uintptr {
	return i.h
}
