package drm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	CapCursorWidth         = 0x8
	CapCursorHeight        = 0x9
	CapAddFB2Modifiers     = 0x10
	CapCRTCInVblankEvent   = 0x12
	CapTimestampMonotonic  = 0x6
	ClientCapUniversal     = 2
	ClientCapAtomic        = 3
	ObjectCRTC             = 0xcccccccc
	ObjectConnector        = 0xc0c0c0c0
	ObjectPlane            = 0xeeeeeeee
	ModeTypePreferred      = 1 << 3
	ModeFlagInterlace      = 1 << 4
	ModeFlagDoubleScan     = 1 << 5
	AtomicPageFlipEvent    = 0x01
	AtomicTestOnly         = 0x0100
	AtomicNonblock         = 0x0200
	AtomicAllowModeset     = 0x0400
	FBModifiers            = 1 << 1
	EventFlipComplete      = 0x02
	ioctlWrite             = 1
	ioctlSetClientCap      = 0x0d
	ioctlGEMClose          = 0x09
	ioctlPrimeFDToHandle   = 0x2e
	ioctlGetResources      = 0xa0
	ioctlGetEncoder        = 0xa6
	ioctlGetConnector      = 0xa7
	ioctlGetProperty       = 0xaa
	ioctlGetPropBlob       = 0xac
	ioctlRmFB              = 0xaf
	ioctlGetPlaneResources = 0xb5
	ioctlGetPlane          = 0xb6
	ioctlAddFB2            = 0xb8
	ioctlObjGetProperties  = 0xb9
	ioctlAtomic            = 0xbc
	ioctlCreateDumb        = 0xb2
	ioctlMapDumb           = 0xb3
	ioctlDestroyDumb       = 0xb4
	ioctlCreatePropBlob    = 0xbd
	ioctlDestroyPropBlob   = 0xbe
	modeInfoSize           = 68
	propertyEnumSize       = 40
)

func iow(nr, size uintptr) uintptr {
	return ioctlWrite<<ioctlDirShift | size<<ioctlSizeShift | ioctlTypeDRM<<ioctlTypeShift | nr
}

func (d *Device) ioctlW(name string, nr uintptr, arg unsafe.Pointer, size uintptr) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(d.fd), iow(nr, size), uintptr(arg))
		switch errno {
		case 0:
			return nil
		case unix.EINTR, unix.EAGAIN:
			continue
		}
		return fmt.Errorf("drm: %s on %s: %w", name, d.path, errno)
	}
}

func addr[T any](p *runtime.Pinner, s []T) uint64 {
	if len(s) == 0 {
		return 0
	}
	p.Pin(&s[0])
	return uint64(uintptr(unsafe.Pointer(&s[0]))) //nolint:gosec // the pinned element stays put until the caller unpins it
}

func (d *Device) FD() int {
	return d.fd
}

func (d *Device) SetClientCap(capability, value uint64) error {
	arg := struct{ capability, value uint64 }{capability, value}
	return d.ioctlW("DRM_IOCTL_SET_CLIENT_CAP", ioctlSetClientCap, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel reads this struct during the call
}

type Resources struct {
	CRTCs, Connectors, Encoders []uint32
	MinSize, MaxSize            [2]uint32
}

type cardRes struct {
	fbs, crtcs, connectors, encoders                uint64
	countFBs, countCRTCs, countConns, countEncoders uint32
	minW, maxW, minH, maxH                          uint32
}

func (d *Device) Resources() (Resources, error) {
	for {
		var res cardRes
		if err := d.ioctl("DRM_IOCTL_MODE_GETRESOURCES", ioctlGetResources, unsafe.Pointer(&res), unsafe.Sizeof(res)); err != nil { //nolint:gosec // the kernel fills this struct during the call
			return Resources{}, err
		}
		counts := res
		out := Resources{
			CRTCs: make([]uint32, res.countCRTCs), Connectors: make([]uint32, res.countConns), Encoders: make([]uint32, res.countEncoders),
		}
		fbs := make([]uint32, res.countFBs)
		var pin runtime.Pinner
		res.fbs, res.crtcs, res.connectors, res.encoders = addr(&pin, fbs), addr(&pin, out.CRTCs), addr(&pin, out.Connectors), addr(&pin, out.Encoders)
		err := d.ioctl("DRM_IOCTL_MODE_GETRESOURCES", ioctlGetResources, unsafe.Pointer(&res), unsafe.Sizeof(res)) //nolint:gosec // the kernel fills the pinned arrays during the call
		pin.Unpin()
		if err != nil {
			return Resources{}, err
		}
		if res.countFBs != counts.countFBs || res.countCRTCs != counts.countCRTCs || res.countConns != counts.countConns || res.countEncoders != counts.countEncoders {
			continue
		}
		out.MinSize, out.MaxSize = [2]uint32{res.minW, res.minH}, [2]uint32{res.maxW, res.maxH}
		return out, nil
	}
}

type Mode struct {
	Clock                                         uint32
	HDisplay, HSyncStart, HSyncEnd, HTotal, HSkew uint16
	VDisplay, VSyncStart, VSyncEnd, VTotal, VScan uint16
	VRefresh, Flags, Type                         uint32
	Name                                          [32]byte
}

func (m Mode) RefreshMHz() int32 {
	if m.HTotal == 0 || m.VTotal == 0 {
		return 0
	}
	refresh := (uint64(m.Clock)*1_000_000/uint64(m.HTotal) + uint64(m.VTotal)/2) / uint64(m.VTotal)
	if m.Flags&ModeFlagInterlace != 0 {
		refresh *= 2
	}
	if m.Flags&ModeFlagDoubleScan != 0 {
		refresh /= 2
	}
	if m.VScan > 1 {
		refresh /= uint64(m.VScan)
	}
	return int32(refresh) //nolint:gosec // display refresh rates fit easily
}

func (m Mode) Preferred() bool {
	return m.Type&ModeTypePreferred != 0
}

func (m Mode) Bytes() []byte {
	b, err := binary.Append(nil, binary.NativeEndian, m)
	if err != nil {
		panic(fmt.Sprintf("drm: encoding a fixed-size mode cannot fail: %v", err))
	}
	return b
}

type Connection uint32

const (
	Connected    Connection = 1
	Disconnected Connection = 2
	Unknown      Connection = 3
)

type Connector struct {
	ID, Type, TypeID, Encoder uint32
	Connection                Connection
	PhysicalMM                [2]uint32
	Modes                     []Mode
	Encoders                  []uint32
}

var connectorTypes = map[uint32]string{
	0: "Unknown", 1: "VGA", 2: "DVI-I", 3: "DVI-D", 4: "DVI-A", 5: "Composite", 6: "SVIDEO", 7: "LVDS",
	8: "Component", 9: "DIN", 10: "DP", 11: "HDMI-A", 12: "HDMI-B", 13: "TV", 14: "eDP", 15: "Virtual",
	16: "DSI", 17: "DPI", 18: "Writeback", 19: "SPI", 20: "USB",
}

func (c Connector) Name() string {
	t, ok := connectorTypes[c.Type]
	if !ok {
		t = fmt.Sprintf("Type%d", c.Type)
	}
	return fmt.Sprintf("%s-%d", t, c.TypeID)
}

type getConnector struct {
	encoders, modes, props, propValues                     uint64
	countModes, countProps, countEncoders                  uint32
	encoderID, connectorID, connectorType, connectorTypeID uint32
	connection, mmWidth, mmHeight, subpixel, pad           uint32
}

func (d *Device) Connector(id uint32) (Connector, error) {
	for {
		arg := getConnector{connectorID: id}
		if err := d.ioctl("DRM_IOCTL_MODE_GETCONNECTOR", ioctlGetConnector, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
			return Connector{}, err
		}
		counts := arg
		modes := make([]byte, int(arg.countModes)*modeInfoSize)
		encoders := make([]uint32, arg.countEncoders)
		props := make([]uint32, arg.countProps)
		values := make([]uint64, arg.countProps)
		var pin runtime.Pinner
		arg.modes, arg.encoders, arg.props, arg.propValues = addr(&pin, modes), addr(&pin, encoders), addr(&pin, props), addr(&pin, values)
		err := d.ioctl("DRM_IOCTL_MODE_GETCONNECTOR", ioctlGetConnector, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel fills the pinned arrays during the call
		pin.Unpin()
		if err != nil {
			return Connector{}, err
		}
		if arg.countModes != counts.countModes || arg.countEncoders != counts.countEncoders || arg.countProps != counts.countProps {
			continue
		}
		c := Connector{
			ID: id, Type: arg.connectorType, TypeID: arg.connectorTypeID, Encoder: arg.encoderID,
			Connection: Connection(arg.connection), PhysicalMM: [2]uint32{arg.mmWidth, arg.mmHeight}, Encoders: encoders,
		}
		c.Modes = make([]Mode, arg.countModes)
		if _, err := binary.Decode(modes, binary.NativeEndian, c.Modes); err != nil {
			return Connector{}, fmt.Errorf("drm: decode the modes of connector %d: %w", id, err)
		}
		return c, nil
	}
}

type Encoder struct {
	ID, Type, CRTC, PossibleCRTCs, PossibleClones uint32
}

func (d *Device) Encoder(id uint32) (Encoder, error) {
	arg := Encoder{ID: id}
	if err := d.ioctl("DRM_IOCTL_MODE_GETENCODER", ioctlGetEncoder, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return Encoder{}, err
	}
	return arg, nil
}

func (d *Device) Planes() ([]uint32, error) {
	for {
		arg := struct {
			ids   uint64
			count uint32
			pad   uint32
		}{}
		if err := d.ioctl("DRM_IOCTL_MODE_GETPLANERESOURCES", ioctlGetPlaneResources, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
			return nil, err
		}
		ids := make([]uint32, arg.count)
		want := arg.count
		var pin runtime.Pinner
		arg.ids = addr(&pin, ids)
		err := d.ioctl("DRM_IOCTL_MODE_GETPLANERESOURCES", ioctlGetPlaneResources, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel fills the pinned array during the call
		pin.Unpin()
		if err != nil {
			return nil, err
		}
		if arg.count == want {
			return ids, nil
		}
	}
}

type Plane struct {
	ID, CRTC, FB, PossibleCRTCs uint32
	Formats                     []uint32
}

func (d *Device) Plane(id uint32) (Plane, error) {
	for {
		arg := struct {
			id, crtc, fb, possible, gamma, countFormats uint32
			formats                                     uint64
		}{id: id}
		if err := d.ioctl("DRM_IOCTL_MODE_GETPLANE", ioctlGetPlane, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
			return Plane{}, err
		}
		formats := make([]uint32, arg.countFormats)
		want := arg.countFormats
		var pin runtime.Pinner
		arg.formats = addr(&pin, formats)
		err := d.ioctl("DRM_IOCTL_MODE_GETPLANE", ioctlGetPlane, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel fills the pinned array during the call
		pin.Unpin()
		if err != nil {
			return Plane{}, err
		}
		if arg.countFormats == want {
			return Plane{ID: id, CRTC: arg.crtc, FB: arg.fb, PossibleCRTCs: arg.possible, Formats: formats}, nil
		}
	}
}

type Property struct {
	ID    uint32
	Value uint64
}

func (d *Device) Properties(objectID, objectType uint32) (map[string]Property, error) {
	for {
		arg := struct {
			props, values         uint64
			count, objID, objType uint32
			pad                   uint32
		}{objID: objectID, objType: objectType}
		if err := d.ioctl("DRM_IOCTL_MODE_OBJ_GETPROPERTIES", ioctlObjGetProperties, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
			return nil, err
		}
		ids := make([]uint32, arg.count)
		values := make([]uint64, arg.count)
		want := arg.count
		var pin runtime.Pinner
		arg.props, arg.values = addr(&pin, ids), addr(&pin, values)
		err := d.ioctl("DRM_IOCTL_MODE_OBJ_GETPROPERTIES", ioctlObjGetProperties, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel fills the pinned arrays during the call
		pin.Unpin()
		if err != nil {
			return nil, err
		}
		if arg.count != want {
			continue
		}
		out := make(map[string]Property, len(ids))
		for i, id := range ids {
			info, err := d.PropertyInfo(id)
			if err != nil {
				return nil, err
			}
			out[info.Name] = Property{ID: id, Value: values[i]}
		}
		return out, nil
	}
}

type PropertyInfo struct {
	Name  string
	Flags uint32
	Enums map[string]uint64
}

func (d *Device) PropertyInfo(id uint32) (PropertyInfo, error) {
	for {
		arg := struct {
			values, enums       uint64
			id, flags           uint32
			name                [32]byte
			countValues, countE uint32
		}{id: id}
		if err := d.ioctl("DRM_IOCTL_MODE_GETPROPERTY", ioctlGetProperty, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
			return PropertyInfo{}, err
		}
		values := make([]uint64, arg.countValues)
		enums := make([]byte, int(arg.countE)*propertyEnumSize)
		wantV, wantE := arg.countValues, arg.countE
		var pin runtime.Pinner
		arg.values, arg.enums = addr(&pin, values), addr(&pin, enums)
		err := d.ioctl("DRM_IOCTL_MODE_GETPROPERTY", ioctlGetProperty, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel fills the pinned arrays during the call
		pin.Unpin()
		if err != nil {
			return PropertyInfo{}, err
		}
		if arg.countValues != wantV || arg.countE != wantE {
			continue
		}
		info := PropertyInfo{Name: cstring(arg.name[:]), Flags: arg.flags, Enums: map[string]uint64{}}
		for i := range int(arg.countE) {
			e := enums[i*propertyEnumSize:]
			info.Enums[cstring(e[8:propertyEnumSize])] = binary.NativeEndian.Uint64(e)
		}
		return info, nil
	}
}

func cstring(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func (d *Device) Blob(id uint32) ([]byte, error) {
	arg := struct {
		id, length uint32
		data       uint64
	}{id: id}
	if err := d.ioctl("DRM_IOCTL_MODE_GETPROPBLOB", ioctlGetPropBlob, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return nil, err
	}
	data := make([]byte, arg.length)
	var pin runtime.Pinner
	arg.data = addr(&pin, data)
	err := d.ioctl("DRM_IOCTL_MODE_GETPROPBLOB", ioctlGetPropBlob, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel fills the pinned buffer during the call
	pin.Unpin()
	if err != nil {
		return nil, err
	}
	return data[:arg.length], nil
}

func (d *Device) CreateBlob(data []byte) (uint32, error) {
	arg := struct {
		data       uint64
		length, id uint32
	}{length: uint32(len(data))} //nolint:gosec // property blobs are small
	var pin runtime.Pinner
	arg.data = addr(&pin, data)
	err := d.ioctl("DRM_IOCTL_MODE_CREATEPROPBLOB", ioctlCreatePropBlob, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel reads the pinned buffer during the call
	pin.Unpin()
	return arg.id, err
}

func (d *Device) DestroyBlob(id uint32) error {
	arg := id
	return d.ioctl("DRM_IOCTL_MODE_DESTROYPROPBLOB", ioctlDestroyPropBlob, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel reads this value during the call
}

func (d *Device) PrimeFDToHandle(fd int) (uint32, error) {
	arg := struct {
		handle, flags uint32
		fd            int32
	}{fd: int32(fd)} //nolint:gosec // file descriptors fit an int32
	if err := d.ioctl("DRM_IOCTL_PRIME_FD_TO_HANDLE", ioctlPrimeFDToHandle, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return 0, err
	}
	return arg.handle, nil
}

func (d *Device) CloseHandle(handle uint32) error {
	arg := struct{ handle, pad uint32 }{handle: handle}
	return d.ioctlW("DRM_IOCTL_GEM_CLOSE", ioctlGEMClose, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel reads this struct during the call
}

type FB struct {
	Width, Height, Format uint32
	Handles, Pitches      [4]uint32
	Offsets               [4]uint32
	Modifier              uint64
	WithModifier          bool
}

func (d *Device) AddFB2(fb FB) (uint32, error) {
	arg := struct {
		id, width, height, format, flags uint32
		handles, pitches, offsets        [4]uint32
		pad                              uint32
		modifiers                        [4]uint64
	}{width: fb.Width, height: fb.Height, format: fb.Format, handles: fb.Handles, pitches: fb.Pitches, offsets: fb.Offsets}
	if fb.WithModifier {
		arg.flags = FBModifiers
		for i, h := range fb.Handles {
			if h != 0 {
				arg.modifiers[i] = fb.Modifier
			}
		}
	}
	if err := d.ioctl("DRM_IOCTL_MODE_ADDFB2", ioctlAddFB2, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return 0, err
	}
	return arg.id, nil
}

func (d *Device) RemoveFB(id uint32) error {
	arg := id
	return d.ioctl("DRM_IOCTL_MODE_RMFB", ioctlRmFB, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel reads this value during the call
}

type Dumb struct {
	Handle, Pitch uint32
	Pix           []byte
}

func (d *Device) CreateDumb(size image.Point) (Dumb, error) {
	if size.X <= 0 || size.Y <= 0 || size.X > math.MaxUint16 || size.Y > math.MaxUint16 {
		return Dumb{}, fmt.Errorf("drm: cannot make a %v dumb buffer", size)
	}
	arg := struct {
		height, width, bpp, flags, handle, pitch uint32
		size                                     uint64
	}{height: uint32(size.Y), width: uint32(size.X), bpp: 32}
	if err := d.ioctl("DRM_IOCTL_MODE_CREATE_DUMB", ioctlCreateDumb, unsafe.Pointer(&arg), unsafe.Sizeof(arg)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return Dumb{}, err
	}
	b := Dumb{Handle: arg.handle, Pitch: arg.pitch}
	m := struct {
		handle, pad uint32
		offset      uint64
	}{handle: arg.handle}
	if err := d.ioctl("DRM_IOCTL_MODE_MAP_DUMB", ioctlMapDumb, unsafe.Pointer(&m), unsafe.Sizeof(m)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return Dumb{}, errors.Join(err, d.DestroyDumb(b))
	}
	pix, err := unix.Mmap(d.fd, int64(m.offset), int(arg.size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED) //nolint:gosec // mmap offsets and dumb buffer sizes fit
	if err != nil {
		return Dumb{}, errors.Join(fmt.Errorf("drm: map a %v dumb buffer on %s: %w", size, d.path, err), d.DestroyDumb(b))
	}
	b.Pix = pix
	return b, nil
}

func (d *Device) DestroyDumb(b Dumb) error {
	var err error
	if b.Pix != nil {
		err = unix.Munmap(b.Pix)
	}
	arg := b.Handle
	return errors.Join(err, d.ioctl("DRM_IOCTL_MODE_DESTROY_DUMB", ioctlDestroyDumb, unsafe.Pointer(&arg), unsafe.Sizeof(arg))) //nolint:gosec // the kernel reads this value during the call
}

type FlipEvent struct {
	UserData uint64
	Sec      uint32
	Usec     uint32
	Sequence uint32
	CRTC     uint32
}

func ParseEvents(buf []byte, flip func(FlipEvent) error) error {
	for len(buf) >= 8 {
		typ, length := binary.NativeEndian.Uint32(buf), binary.NativeEndian.Uint32(buf[4:])
		if length < 8 || int(length) > len(buf) {
			return fmt.Errorf("drm: an event of %d bytes in %d", length, len(buf))
		}
		if typ == EventFlipComplete && length >= 32 {
			e := FlipEvent{
				UserData: binary.NativeEndian.Uint64(buf[8:]), Sec: binary.NativeEndian.Uint32(buf[16:]),
				Usec: binary.NativeEndian.Uint32(buf[20:]), Sequence: binary.NativeEndian.Uint32(buf[24:]), CRTC: binary.NativeEndian.Uint32(buf[28:]),
			}
			if err := flip(e); err != nil {
				return err
			}
		}
		buf = buf[length:]
	}
	if len(buf) != 0 {
		return errors.New("drm: a truncated event")
	}
	return nil
}

func Modifiers(inFormats []byte, format uint32) ([]uint64, error) {
	if len(inFormats) < 24 {
		return nil, fmt.Errorf("drm: an IN_FORMATS blob of %d bytes", len(inFormats))
	}
	h := func(i int) uint32 { return binary.NativeEndian.Uint32(inFormats[i*4:]) }
	countFormats, formatsOff, countMods, modsOff := h(2), h(3), h(4), h(5)
	if uint64(formatsOff)+4*uint64(countFormats) > uint64(len(inFormats)) || uint64(modsOff)+24*uint64(countMods) > uint64(len(inFormats)) {
		return nil, fmt.Errorf("drm: IN_FORMATS tables past its %d bytes", len(inFormats))
	}
	index := -1
	for i := range int(countFormats) {
		if binary.NativeEndian.Uint32(inFormats[int(formatsOff)+4*i:]) == format {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, nil
	}
	var out []uint64
	for i := range int(countMods) {
		m := inFormats[int(modsOff)+24*i:]
		mask, offset, modifier := binary.NativeEndian.Uint64(m), int(binary.NativeEndian.Uint32(m[8:])), binary.NativeEndian.Uint64(m[16:])
		if index >= offset && index < offset+64 && mask&(1<<(index-offset)) != 0 {
			out = append(out, modifier)
		}
	}
	return out, nil
}
