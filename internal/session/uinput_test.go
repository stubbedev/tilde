package session

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend"
)

const (
	uiSetEvBit   = 0x40045564
	uiSetKeyBit  = 0x40045565
	uiSetRelBit  = 0x40045566
	uiDevCreate  = 0x5501
	uiDevDestroy = 0x5502
	uiDevSetup   = 0x405c5503
	uiGetSysname = 0x8040552c
	evSyn        = 0
	evKey        = 1
	evRel        = 2
	btnLeft      = 0x110
	btnRight     = 0x111
	relX         = 0
	relY         = 1
	relWheel     = 8
	relWheelHi   = 11
	busVirtual   = 6
)

const virtualMouse = "tilde virtual mouse"

type uinputSetup struct {
	bustype, vendor, product, version uint16
	name                              [80]byte
	ffEffectsMax                      uint32
}

type virtualDevice struct {
	f    *os.File
	node string
}

func ioctl(fd uintptr, req uintptr, arg uintptr) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, arg); errno != 0 {
		return errno
	}
	return nil
}

func newVirtualMouse(t *testing.T) *virtualDevice {
	t.Helper()
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("no uinput here: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	fd := f.Fd()
	for _, b := range []struct{ req, v uintptr }{
		{uiSetEvBit, evKey},
		{uiSetEvBit, evRel},
		{uiSetKeyBit, btnLeft},
		{uiSetKeyBit, btnRight},
		{uiSetRelBit, relX},
		{uiSetRelBit, relY},
		{uiSetRelBit, relWheel},
		{uiSetRelBit, relWheelHi},
	} {
		if err := ioctl(fd, b.req, b.v); err != nil {
			t.Fatalf("uinput bit %#x %d: %v", b.req, b.v, err)
		}
	}
	setup := uinputSetup{bustype: busVirtual, vendor: 0x1234, product: 0x5678}
	copy(setup.name[:], virtualMouse)
	if err := ioctl(fd, uiDevSetup, uintptr(unsafe.Pointer(&setup))); err != nil {
		t.Fatalf("uinput setup: %v", err)
	}
	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		t.Fatalf("uinput create: %v", err)
	}
	t.Cleanup(func() { _ = ioctl(fd, uiDevDestroy, 0) })
	var name [64]byte
	if err := ioctl(fd, uiGetSysname, uintptr(unsafe.Pointer(&name))); err != nil {
		t.Fatalf("uinput sysname: %v", err)
	}
	sys := filepath.Join("/sys/devices/virtual/input", strings.TrimRight(string(name[:]), "\x00"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, _ := filepath.Glob(filepath.Join(sys, "event*"))
		if len(events) == 1 {
			node := filepath.Join("/dev/input", filepath.Base(events[0]))
			r, err := os.Open(node)
			if err == nil {
				_ = r.Close()
				return &virtualDevice{f: f, node: node}
			}
			if errors.Is(err, os.ErrPermission) {
				t.Skipf("cannot read %s: %v", node, err)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no readable event node under %s", sys)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (v *virtualDevice) emit(t *testing.T, events ...[3]int32) {
	t.Helper()
	buf := make([]byte, 0, 24*(len(events)+1))
	for _, e := range append(events, [3]int32{evSyn, 0, 0}) {
		buf = append(buf, make([]byte, 16)...)
		buf = binary.NativeEndian.AppendUint16(buf, uint16(e[0]))
		buf = binary.NativeEndian.AppendUint16(buf, uint16(e[1]))
		buf = binary.NativeEndian.AppendUint32(buf, uint32(e[2]))
	}
	if _, err := v.f.Write(buf); err != nil {
		t.Fatal(err)
	}
}

type inputRecorder struct {
	backend.Handler
	devices []backend.Device
	axes    []backend.PointerAxis
}

func (r *inputRecorder) DeviceAdded(d backend.Device) error {
	r.devices = append(r.devices, d)
	return nil
}

func (r *inputRecorder) DeviceRemoved(backend.Device) error { return nil }

func (r *inputRecorder) Input(_ backend.Device, e backend.Event) error {
	if a, ok := e.(backend.PointerAxis); ok {
		r.axes = append(r.axes, a)
	}
	return nil
}

func (in *Libinput) waitAxis(t *testing.T, r *inputRecorder) backend.PointerAxis {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.axes) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no scroll event arrived")
		}
		if err := in.read(0); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	a := r.axes[0]
	r.axes = r.axes[:0]
	return a
}

func TestLibinputAppliesAndReappliesPointerSettings(t *testing.T) {
	mouse := newVirtualMouse(t)
	in := libinput(t, PlainOpener{})
	rec := &inputRecorder{}
	in.Start(rec)
	if err := in.Reconfigure(InputConfig{Pointer: DeviceConfig{NaturalScroll: new(true), ScrollFactor: new(2.0)}}); err != nil {
		t.Fatal(err)
	}
	if err := in.Add(mouse.node); err != nil {
		t.Fatal(err)
	}
	if len(rec.devices) != 1 || rec.devices[0].Name() != virtualMouse {
		t.Fatalf("added %v", rec.devices)
	}
	click := func() backend.PointerAxis {
		mouse.emit(t, [3]int32{evRel, relWheel, 1}, [3]int32{evRel, relWheelHi, 120})
		return in.waitAxis(t, rec)
	}
	if a := click(); a.Value120 != 240 || a.Value <= 0 {
		t.Fatalf("a wheel click up with natural scrolling and factor 2: %+v", a)
	}
	if err := in.Reconfigure(InputConfig{}); err != nil {
		t.Fatal(err)
	}
	if a := click(); a.Value120 != -120 || a.Value >= 0 {
		t.Fatalf("a wheel click up after reloading to the defaults: %+v", a)
	}
	err := in.Reconfigure(InputConfig{Devices: map[string]DeviceConfig{virtualMouse: {Tap: new(true)}}})
	if err == nil || !strings.Contains(err.Error(), `"`+virtualMouse+`" has no tap setting`) {
		t.Fatalf("tapping on a mouse: %v", err)
	}
}
