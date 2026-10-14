package session

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/loop"
)

const LibinputEnv = "TILDE_LIBINPUT"

var defaultLibinput = "libinput.so.10"

func LibinputPath(getenv func(string) string) string {
	if p := getenv(LibinputEnv); p != "" {
		return p
	}
	return defaultLibinput
}

const (
	evDeviceAdded       = 1
	evDeviceRemoved     = 2
	evKeyboardKey       = 300
	evPointerMotion     = 400
	evPointerAbsolute   = 401
	evPointerButton     = 402
	evScrollWheel       = 404
	evScrollFinger      = 405
	evScrollContinuous  = 406
	evTouchDown         = 500
	evTouchUp           = 501
	evTouchMotion       = 502
	evTouchCancel       = 503
	evTouchFrame        = 504
	evSwitchToggle      = 900
	capKeyboard         = 0
	capPointer          = 1
	capTouch            = 2
	axisVertical        = 0
	axisHorizontal      = 1
	switchLid           = 1
	switchTabletMode    = 2
	configStatusSuccess = 0
)

type libinputFuncs struct {
	createContext  func(iface unsafe.Pointer, userData uintptr) uintptr
	unref          func(li uintptr) uintptr
	addDevice      func(li uintptr, path string) uintptr
	removeDevice   func(dev uintptr)
	getFD          func(li uintptr) int32
	dispatch       func(li uintptr) int32
	getEvent       func(li uintptr) uintptr
	eventType      func(ev uintptr) int32
	eventDevice    func(ev uintptr) uintptr
	destroyEvent   func(ev uintptr)
	suspend        func(li uintptr)
	resume         func(li uintptr) int32
	deviceName     func(dev uintptr) string
	deviceHasCap   func(dev uintptr, capability int32) int32
	deviceRef      func(dev uintptr) uintptr
	deviceUnref    func(dev uintptr) uintptr
	keyboardEvent  func(ev uintptr) uintptr
	keyTime        func(kev uintptr) uint64
	key            func(kev uintptr) uint32
	keyState       func(kev uintptr) int32
	pointerEvent   func(ev uintptr) uintptr
	pointerTime    func(pev uintptr) uint64
	dx, dy         func(pev uintptr) float64
	dxRaw, dyRaw   func(pev uintptr) float64
	absX, absY     func(pev uintptr, size uint32) float64
	button         func(pev uintptr) uint32
	buttonState    func(pev uintptr) int32
	hasAxis        func(pev uintptr, axis int32) int32
	scrollValue    func(pev uintptr, axis int32) float64
	scrollV120     func(pev uintptr, axis int32) float64
	touchEvent     func(ev uintptr) uintptr
	touchTime      func(tev uintptr) uint64
	touchSlot      func(tev uintptr) int32
	touchX, touchY func(tev uintptr, size uint32) float64
	switchEvent    func(ev uintptr) uintptr
	switchTime     func(sev uintptr) uint64
	switchKind     func(sev uintptr) int32
	switchState    func(sev uintptr) int32
	tap            func(dev uintptr, enable int32) int32
	dragLock       func(dev uintptr, enable int32) int32
	naturalScroll  func(dev uintptr, enable int32) int32
	dwt            func(dev uintptr, enable int32) int32
	middle         func(dev uintptr, enable int32) int32
	clickMethod    func(dev uintptr, method uint32) int32
	scrollMethod   func(dev uintptr, method uint32) int32
	accelProfile   func(dev uintptr, profile uint32) int32
	accelSpeed     func(dev uintptr, speed float64) int32
}

type Opener interface {
	TakeDevice(path string) (int, Device, error)
	ReleaseDevice(d Device) error
}

type PlainOpener struct{}

func (PlainOpener) TakeDevice(path string) (int, Device, error) {
	d, err := DeviceOf(path)
	if err != nil {
		return -1, Device{}, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, Device{}, fmt.Errorf("session: open %s: %w", path, err)
	}
	return fd, d, nil
}

func (PlainOpener) ReleaseDevice(Device) error {
	return nil
}

type ClickMethod uint32

const (
	ClickButtonAreas ClickMethod = 1 << 0
	ClickFinger      ClickMethod = 1 << 1
)

type ScrollMethod uint32

const (
	ScrollTwoFinger    ScrollMethod = 1 << 0
	ScrollEdge         ScrollMethod = 1 << 1
	ScrollOnButtonDown ScrollMethod = 1 << 2
)

type AccelProfile uint32

const (
	AccelFlat     AccelProfile = 1 << 0
	AccelAdaptive AccelProfile = 1 << 1
)

type DeviceConfig struct {
	Tap, DragLock, NaturalScroll, DisableWhileTyping, MiddleEmulation *bool
	ClickMethod                                                       *ClickMethod
	ScrollMethod                                                      *ScrollMethod
	AccelProfile                                                      *AccelProfile
	AccelSpeed                                                        *float64
}

type InputDevice struct {
	name string
	caps backend.Capability
	h    uintptr
}

func (d *InputDevice) Name() string                     { return d.name }
func (d *InputDevice) Capabilities() backend.Capability { return d.caps }

type Libinput struct {
	fn      libinputFuncs
	lib     uintptr
	li      uintptr
	id      uintptr
	opener  Opener
	config  func(name string) DeviceConfig
	report  func(error)
	handler backend.Handler
	src     *loop.Source
	devices map[uintptr]*InputDevice
	paths   map[string]uintptr
	fds     map[int32]Device
	openErr error
	out     []backend.Event
}

type libinputInterface struct {
	open, close uintptr
}

var callbacks = struct {
	once   sync.Once
	iface  *libinputInterface
	mu     sync.Mutex
	next   uintptr
	inputs map[uintptr]*Libinput
}{inputs: map[uintptr]*Libinput{}}

func goString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 { //nolint:gosec // a NUL-terminated path from libinput
		n++
	}
	return string(unsafe.Slice(p, n)) //nolint:gosec // n bytes before the NUL
}

func lookup(userData uintptr) *Libinput {
	callbacks.mu.Lock()
	defer callbacks.mu.Unlock()
	return callbacks.inputs[userData]
}

func openRestricted(path *byte, _ int32, userData uintptr) int32 {
	in := lookup(userData)
	if in == nil {
		return -int32(unix.ENODEV)
	}
	fd, dev, err := in.opener.TakeDevice(goString(path))
	if err != nil {
		in.openErr = err
		if errno, ok := errors.AsType[unix.Errno](err); ok {
			return -int32(errno) //nolint:gosec // errno values are small
		}
		return -int32(unix.EACCES)
	}
	in.fds[int32(fd)] = dev //nolint:gosec // file descriptors fit an int32
	return int32(fd)        //nolint:gosec // file descriptors fit an int32
}

func closeRestricted(fd int32, userData uintptr) {
	in := lookup(userData)
	if in == nil {
		return
	}
	dev, ok := in.fds[fd]
	delete(in.fds, fd)
	err := unix.Close(int(fd))
	if ok {
		err = errors.Join(err, in.opener.ReleaseDevice(dev))
	}
	if err != nil {
		in.report(fmt.Errorf("session: close input device fd %d: %w", fd, err))
	}
}

func OpenLibinput(l *loop.Loop, path string, opener Opener, config func(string) DeviceConfig, report func(error)) (*Libinput, error) {
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("session: load libinput from %s (set %s to its path): %w", path, LibinputEnv, err)
	}
	in := &Libinput{lib: lib, opener: opener, config: config, report: report, devices: map[uintptr]*InputDevice{}, paths: map[string]uintptr{}, fds: map[int32]Device{}}
	if err := in.bind(path); err != nil {
		return nil, errors.Join(err, purego.Dlclose(lib))
	}
	callbacks.once.Do(func() {
		callbacks.iface = &libinputInterface{open: purego.NewCallback(openRestricted), close: purego.NewCallback(closeRestricted)}
	})
	callbacks.mu.Lock()
	callbacks.next++
	in.id = callbacks.next
	callbacks.inputs[in.id] = in
	callbacks.mu.Unlock()
	if in.li = in.fn.createContext(unsafe.Pointer(callbacks.iface), in.id); in.li == 0 { //nolint:gosec // the interface is a package-level heap object libinput may keep
		return nil, errors.Join(errors.New("session: libinput_path_create_context failed"), in.forget(), purego.Dlclose(lib))
	}
	if in.src, err = l.AddFD(int(in.fn.getFD(in.li)), loop.Readable, in.read); err != nil {
		return nil, errors.Join(err, in.Close())
	}
	return in, nil
}

func (in *Libinput) bind(path string) error {
	f := &in.fn
	for _, s := range []struct {
		fn   any
		name string
	}{
		{&f.createContext, "libinput_path_create_context"},
		{&f.unref, "libinput_unref"},
		{&f.addDevice, "libinput_path_add_device"},
		{&f.removeDevice, "libinput_path_remove_device"},
		{&f.getFD, "libinput_get_fd"},
		{&f.dispatch, "libinput_dispatch"},
		{&f.getEvent, "libinput_get_event"},
		{&f.eventType, "libinput_event_get_type"},
		{&f.eventDevice, "libinput_event_get_device"},
		{&f.destroyEvent, "libinput_event_destroy"},
		{&f.suspend, "libinput_suspend"},
		{&f.resume, "libinput_resume"},
		{&f.deviceName, "libinput_device_get_name"},
		{&f.deviceHasCap, "libinput_device_has_capability"},
		{&f.deviceRef, "libinput_device_ref"},
		{&f.deviceUnref, "libinput_device_unref"},
		{&f.keyboardEvent, "libinput_event_get_keyboard_event"},
		{&f.keyTime, "libinput_event_keyboard_get_time_usec"},
		{&f.key, "libinput_event_keyboard_get_key"},
		{&f.keyState, "libinput_event_keyboard_get_key_state"},
		{&f.pointerEvent, "libinput_event_get_pointer_event"},
		{&f.pointerTime, "libinput_event_pointer_get_time_usec"},
		{&f.dx, "libinput_event_pointer_get_dx"},
		{&f.dy, "libinput_event_pointer_get_dy"},
		{&f.dxRaw, "libinput_event_pointer_get_dx_unaccelerated"},
		{&f.dyRaw, "libinput_event_pointer_get_dy_unaccelerated"},
		{&f.absX, "libinput_event_pointer_get_absolute_x_transformed"},
		{&f.absY, "libinput_event_pointer_get_absolute_y_transformed"},
		{&f.button, "libinput_event_pointer_get_button"},
		{&f.buttonState, "libinput_event_pointer_get_button_state"},
		{&f.hasAxis, "libinput_event_pointer_has_axis"},
		{&f.scrollValue, "libinput_event_pointer_get_scroll_value"},
		{&f.scrollV120, "libinput_event_pointer_get_scroll_value_v120"},
		{&f.touchEvent, "libinput_event_get_touch_event"},
		{&f.touchTime, "libinput_event_touch_get_time_usec"},
		{&f.touchSlot, "libinput_event_touch_get_seat_slot"},
		{&f.touchX, "libinput_event_touch_get_x_transformed"},
		{&f.touchY, "libinput_event_touch_get_y_transformed"},
		{&f.switchEvent, "libinput_event_get_switch_event"},
		{&f.switchTime, "libinput_event_switch_get_time_usec"},
		{&f.switchKind, "libinput_event_switch_get_switch"},
		{&f.switchState, "libinput_event_switch_get_switch_state"},
		{&f.tap, "libinput_device_config_tap_set_enabled"},
		{&f.dragLock, "libinput_device_config_tap_set_drag_lock_enabled"},
		{&f.naturalScroll, "libinput_device_config_scroll_set_natural_scroll_enabled"},
		{&f.dwt, "libinput_device_config_dwt_set_enabled"},
		{&f.middle, "libinput_device_config_middle_emulation_set_enabled"},
		{&f.clickMethod, "libinput_device_config_click_set_method"},
		{&f.scrollMethod, "libinput_device_config_scroll_set_method"},
		{&f.accelProfile, "libinput_device_config_accel_set_profile"},
		{&f.accelSpeed, "libinput_device_config_accel_set_speed"},
	} {
		if _, err := purego.Dlsym(in.lib, s.name); err != nil {
			return fmt.Errorf("session: %s lacks %s: %w", path, s.name, err)
		}
		purego.RegisterLibFunc(s.fn, in.lib, s.name)
	}
	return nil
}

func (in *Libinput) forget() error {
	callbacks.mu.Lock()
	defer callbacks.mu.Unlock()
	delete(callbacks.inputs, in.id)
	return nil
}

func (in *Libinput) Start(h backend.Handler) {
	in.handler = h
}

func (in *Libinput) Add(path string) error {
	if _, ok := in.paths[path]; ok {
		return nil
	}
	in.openErr = nil
	dev := in.fn.addDevice(in.li, path)
	if dev == 0 {
		if in.openErr != nil {
			return fmt.Errorf("session: add input device %s: %w", path, in.openErr)
		}
		return fmt.Errorf("session: libinput rejected %s", path)
	}
	in.paths[path] = in.fn.deviceRef(dev)
	return in.drain()
}

func (in *Libinput) Remove(path string) error {
	dev, ok := in.paths[path]
	if !ok {
		return nil
	}
	delete(in.paths, path)
	in.fn.removeDevice(dev)
	in.fn.deviceUnref(dev)
	return in.drain()
}

func (in *Libinput) Suspend() {
	in.fn.suspend(in.li)
}

func (in *Libinput) Resume() error {
	if in.fn.resume(in.li) != 0 {
		return errors.New("session: libinput_resume failed")
	}
	return in.drain()
}

func (in *Libinput) read(loop.Events) error {
	if rc := in.fn.dispatch(in.li); rc != 0 {
		return fmt.Errorf("session: libinput_dispatch: %w", unix.Errno(-rc)) //nolint:gosec // libinput returns a negated errno
	}
	return in.drain()
}

func (in *Libinput) drain() error {
	var errs []error
	for ev := in.fn.getEvent(in.li); ev != 0; ev = in.fn.getEvent(in.li) {
		errs = append(errs, in.event(ev))
		in.fn.destroyEvent(ev)
	}
	return errors.Join(errs...)
}

func usec(v uint64) time.Duration {
	return time.Duration(v) * time.Microsecond //nolint:gosec // monotonic microseconds fit a Duration for centuries
}

func (in *Libinput) event(ev uintptr) error {
	if in.handler == nil {
		return nil
	}
	f := &in.fn
	h := f.eventDevice(ev)
	switch typ := f.eventType(ev); typ {
	case evDeviceAdded:
		d := &InputDevice{name: f.deviceName(h), h: f.deviceRef(h)}
		for c, flag := range map[int32]backend.Capability{capKeyboard: backend.Keyboard, capPointer: backend.Pointer, capTouch: backend.Touch} {
			if f.deviceHasCap(h, c) != 0 {
				d.caps |= flag
			}
		}
		in.devices[h] = d
		if err := in.configure(d); err != nil {
			in.report(err)
		}
		return in.handler.DeviceAdded(d)
	case evDeviceRemoved:
		d, ok := in.devices[h]
		if !ok {
			return nil
		}
		delete(in.devices, h)
		f.deviceUnref(d.h)
		return in.handler.DeviceRemoved(d)
	}
	d, ok := in.devices[h]
	if !ok {
		return nil
	}
	in.out = in.translate(in.out[:0], ev)
	for _, e := range in.out {
		if err := in.handler.Input(d, e); err != nil {
			return err
		}
	}
	return nil
}

func pointerFrame(out []backend.Event, at time.Duration) []backend.Event {
	return append(out, backend.PointerFrame{Time: at})
}

func (in *Libinput) translate(out []backend.Event, ev uintptr) []backend.Event {
	f := &in.fn
	switch typ := f.eventType(ev); typ {
	case evKeyboardKey:
		k := f.keyboardEvent(ev)
		return append(out, backend.Key{Time: usec(f.keyTime(k)), Code: f.key(k), Pressed: f.keyState(k) == 1})
	case evPointerMotion:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		return pointerFrame(append(out, backend.PointerMotion{Time: at, DX: f.dx(p), DY: f.dy(p), UnaccelDX: f.dxRaw(p), UnaccelDY: f.dyRaw(p)}), at)
	case evPointerAbsolute:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		return pointerFrame(append(out, backend.PointerMotionAbsolute{Time: at, X: f.absX(p, 1), Y: f.absY(p, 1)}), at)
	case evPointerButton:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		return pointerFrame(append(out, backend.PointerButton{Time: at, Button: f.button(p), Pressed: f.buttonState(p) == 1}), at)
	case evScrollWheel, evScrollFinger, evScrollContinuous:
		p := f.pointerEvent(ev)
		at := usec(f.pointerTime(p))
		source := map[int32]backend.AxisSource{evScrollWheel: backend.Wheel, evScrollFinger: backend.Finger, evScrollContinuous: backend.Continuous}[typ]
		for _, a := range []struct {
			axis        int32
			orientation backend.Orientation
		}{{axisVertical, backend.Vertical}, {axisHorizontal, backend.Horizontal}} {
			if f.hasAxis(p, a.axis) == 0 {
				continue
			}
			ax := backend.PointerAxis{Time: at, Orientation: a.orientation, Source: source, Value: f.scrollValue(p, a.axis)}
			if typ == evScrollWheel {
				ax.Value120 = int32(f.scrollV120(p, a.axis))
			}
			out = append(out, ax)
		}
		return pointerFrame(out, at)
	case evTouchDown, evTouchMotion:
		t := f.touchEvent(ev)
		at, id, x, y := usec(f.touchTime(t)), f.touchSlot(t), f.touchX(t, 1), f.touchY(t, 1)
		if typ == evTouchDown {
			return append(out, backend.TouchDown{Time: at, ID: id, X: x, Y: y})
		}
		return append(out, backend.TouchMotion{Time: at, ID: id, X: x, Y: y})
	case evTouchUp:
		t := f.touchEvent(ev)
		return append(out, backend.TouchUp{Time: usec(f.touchTime(t)), ID: f.touchSlot(t)})
	case evTouchCancel:
		t := f.touchEvent(ev)
		return append(out, backend.TouchCancel{Time: usec(f.touchTime(t)), ID: f.touchSlot(t)})
	case evTouchFrame:
		return append(out, backend.TouchFrame{Time: usec(f.touchTime(f.touchEvent(ev)))})
	case evSwitchToggle:
		s := f.switchEvent(ev)
		kind := backend.SwitchLid
		if f.switchKind(s) == switchTabletMode {
			kind = backend.SwitchTabletMode
		}
		return append(out, backend.Switch{Time: usec(f.switchTime(s)), Kind: kind, On: f.switchState(s) == 1})
	}
	return out
}

func boolInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func (in *Libinput) configure(d *InputDevice) error {
	if in.config == nil {
		return nil
	}
	c := in.config(d.name)
	f := &in.fn
	var errs []error
	set := func(name string, status int32) {
		if status != configStatusSuccess {
			errs = append(errs, fmt.Errorf("session: %s does not take %s (status %d)", d.name, name, status))
		}
	}
	if c.Tap != nil {
		set("tap", f.tap(d.h, boolInt(*c.Tap)))
	}
	if c.DragLock != nil {
		set("drag_lock", f.dragLock(d.h, boolInt(*c.DragLock)))
	}
	if c.NaturalScroll != nil {
		set("natural_scroll", f.naturalScroll(d.h, boolInt(*c.NaturalScroll)))
	}
	if c.DisableWhileTyping != nil {
		set("disable_while_typing", f.dwt(d.h, boolInt(*c.DisableWhileTyping)))
	}
	if c.MiddleEmulation != nil {
		set("middle_emulation", f.middle(d.h, boolInt(*c.MiddleEmulation)))
	}
	if c.ClickMethod != nil {
		set("click_method", f.clickMethod(d.h, uint32(*c.ClickMethod)))
	}
	if c.ScrollMethod != nil {
		set("scroll_method", f.scrollMethod(d.h, uint32(*c.ScrollMethod)))
	}
	if c.AccelProfile != nil {
		set("accel_profile", f.accelProfile(d.h, uint32(*c.AccelProfile)))
	}
	if c.AccelSpeed != nil {
		set("accel_speed", f.accelSpeed(d.h, *c.AccelSpeed))
	}
	return errors.Join(errs...)
}

func (in *Libinput) Close() error {
	var errs []error
	if in.src != nil {
		errs = append(errs, in.src.Remove())
	}
	for path := range in.paths {
		errs = append(errs, in.Remove(path))
	}
	for h, d := range in.devices {
		in.fn.deviceUnref(d.h)
		delete(in.devices, h)
	}
	if in.li != 0 {
		in.fn.unref(in.li)
		in.li = 0
	}
	return errors.Join(append(errs, in.forget(), purego.Dlclose(in.lib))...)
}
