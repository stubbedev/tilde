// Package drm issues DRM ioctls on a device node: capabilities and timeline
// syncobjs for explicit synchronization.
package drm

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	CapSyncobj         = 0x13
	CapSyncobjTimeline = 0x14

	ioctlGetCap        = 0x0c
	ioctlCreate        = 0xbf
	ioctlDestroy       = 0xc0
	ioctlHandleToFD    = 0xc1
	ioctlFDToHandle    = 0xc2
	ioctlTransfer      = 0xcc
	ioctlTimelineQuery = 0xcb
	ioctlSignal        = 0xcd
	ioctlEventFD       = 0xcf

	createSignaled = 1 << 0
	importSyncFile = 1 << 0
	exportSyncFile = 1 << 0
	waitAvailable  = 1 << 2
	ioctlReadWrite = 3
	ioctlTypeDRM   = 'd'
	ioctlDirShift  = 30
	ioctlSizeShift = 16
	ioctlTypeShift = 8
)

func iowr(nr, size uintptr) uintptr {
	return ioctlReadWrite<<ioctlDirShift | size<<ioctlSizeShift | ioctlTypeDRM<<ioctlTypeShift | nr
}

type Device struct {
	fd   int
	path string
}

func Open(path string) (*Device, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("drm: open %s: %w", path, err)
	}
	return &Device{fd: fd, path: path}, nil
}

func FromFD(fd int, path string) *Device {
	return &Device{fd: fd, path: path}
}

func (d *Device) Close() error {
	if err := unix.Close(d.fd); err != nil {
		return fmt.Errorf("drm: close %s: %w", d.path, err)
	}
	return nil
}

func (d *Device) Path() string {
	return d.path
}

func (d *Device) ioctl(name string, nr uintptr, arg unsafe.Pointer, size uintptr) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(d.fd), iowr(nr, size), uintptr(arg))
		switch errno {
		case 0:
			return nil
		case unix.EINTR, unix.EAGAIN:
			continue
		}
		return fmt.Errorf("drm: %s on %s: %w", name, d.path, errno)
	}
}

func (d *Device) Cap(capability uint64) (uint64, error) {
	arg := struct{ capability, value uint64 }{capability: capability}
	if err := d.ioctl("DRM_IOCTL_GET_CAP", ioctlGetCap, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return 0, err
	}
	return arg.value, nil
}

type Syncobj uint32

type handleArgs struct {
	handle uint32
	flags  uint32
	fd     int32
	pad    uint32
}

func (d *Device) CreateSyncobj(signaled bool) (Syncobj, error) {
	arg := struct{ handle, flags uint32 }{}
	if signaled {
		arg.flags = createSignaled
	}
	if err := d.ioctl("DRM_IOCTL_SYNCOBJ_CREATE", ioctlCreate, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return 0, err
	}
	return Syncobj(arg.handle), nil
}

func (d *Device) DestroySyncobj(s Syncobj) error {
	arg := struct{ handle, pad uint32 }{handle: uint32(s)}
	return d.ioctl("DRM_IOCTL_SYNCOBJ_DESTROY", ioctlDestroy, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel reads this struct during the call
}

func (d *Device) ImportSyncobj(fd int) (Syncobj, error) {
	arg := handleArgs{fd: int32(fd)}                                                                                             //nolint:gosec // file descriptors fit an int32
	if err := d.ioctl("DRM_IOCTL_SYNCOBJ_FD_TO_HANDLE", ioctlFDToHandle, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return 0, err
	}
	return Syncobj(arg.handle), nil
}

func (d *Device) ExportSyncobj(s Syncobj) (int, error) {
	arg := handleArgs{handle: uint32(s), fd: -1}
	if err := d.ioctl("DRM_IOCTL_SYNCOBJ_HANDLE_TO_FD", ioctlHandleToFD, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return -1, err
	}
	return int(arg.fd), nil
}

type transferArgs struct {
	src, dst           uint32
	srcPoint, dstPoint uint64
	flags, pad         uint32
}

func (d *Device) transfer(src Syncobj, srcPoint uint64, dst Syncobj, dstPoint uint64) error {
	arg := transferArgs{src: uint32(src), dst: uint32(dst), srcPoint: srcPoint, dstPoint: dstPoint}
	return d.ioctl("DRM_IOCTL_SYNCOBJ_TRANSFER", ioctlTransfer, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel reads this struct during the call
}

type timelineArgs struct {
	handles, points uint64
	count, flags    uint32
}

func (d *Device) timeline(name string, nr uintptr, s Syncobj, point uint64) (uint64, error) {
	handle, points := new(uint32), new(uint64)
	*handle, *points = uint32(s), point
	arg := timelineArgs{handles: uint64(uintptr(unsafe.Pointer(handle))), points: uint64(uintptr(unsafe.Pointer(points))), count: 1} //nolint:gosec // heap memory that the kernel uses only during the call
	err := d.ioctl(name, nr, unsafe.Pointer(&arg), unsafe.Sizeof(arg))                                                               //nolint:gosec // the kernel reads this struct during the call
	runtime.KeepAlive(handle)
	runtime.KeepAlive(points)
	return *points, err
}

func (d *Device) Signal(s Syncobj, point uint64) error {
	_, err := d.timeline("DRM_IOCTL_SYNCOBJ_TIMELINE_SIGNAL", ioctlSignal, s, point)
	return err
}

func (d *Device) Query(s Syncobj) (uint64, error) {
	return d.timeline("DRM_IOCTL_SYNCOBJ_QUERY", ioctlTimelineQuery, s, 0)
}

func (d *Device) EventFD(s Syncobj, point uint64, eventfd int, available bool) error {
	arg := struct {
		handle, flags uint32
		point         uint64
		fd            int32
		pad           uint32
	}{handle: uint32(s), point: point, fd: int32(eventfd)} //nolint:gosec // file descriptors fit an int32
	if available {
		arg.flags = waitAvailable
	}
	return d.ioctl("DRM_IOCTL_SYNCOBJ_EVENTFD", ioctlEventFD, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel reads this struct during the call
}

func (d *Device) ImportSyncFile(s Syncobj, point uint64, syncFile int) error {
	tmp, err := d.CreateSyncobj(false)
	if err != nil {
		return err
	}
	arg := handleArgs{handle: uint32(tmp), flags: importSyncFile, fd: int32(syncFile)}                                    //nolint:gosec // file descriptors fit an int32
	err = d.ioctl("DRM_IOCTL_SYNCOBJ_FD_TO_HANDLE(sync file)", ioctlFDToHandle, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel reads this struct during the call
	if err == nil {
		err = d.transfer(tmp, 0, s, point)
	}
	return errors.Join(err, d.DestroySyncobj(tmp))
}

func (d *Device) ExportSyncFile(s Syncobj, point uint64) (int, error) {
	tmp, err := d.CreateSyncobj(false)
	if err != nil {
		return -1, err
	}
	fd := -1
	err = d.transfer(s, point, tmp, 0)
	if err == nil {
		arg := handleArgs{handle: uint32(tmp), flags: exportSyncFile, fd: -1}
		if err = d.ioctl("DRM_IOCTL_SYNCOBJ_HANDLE_TO_FD(sync file)", ioctlHandleToFD, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err == nil { //nolint:gosec // the kernel fills this struct during the call
			fd = int(arg.fd)
		}
	}
	return fd, errors.Join(err, d.DestroySyncobj(tmp))
}
