package testclient

import (
	"errors"
	"image"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"

	cld "github.com/stubbedev/tilde/internal/protocol/client/linuxdmabuf"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	udmabufCreate  = 0x40187542
	udmabufCloexec = 0x01
	xrgb8888       = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
	modLinear      = 0
	modInvalid     = 0x00ffffffffffffff
)

type udmabufCreateArgs struct {
	memfd  uint32
	flags  uint32
	offset uint64
	size   uint64
}

type dmabufModifiers struct{ linear, implicit bool }

func (m *dmabufModifiers) Format(*cld.ZwpLinuxDmabufV1, uint32) error { return nil }

func (m *dmabufModifiers) Modifier(_ *cld.ZwpLinuxDmabufV1, format, hi, lo uint32) error {
	if format != xrgb8888 {
		return nil
	}
	switch wire.Join(hi, lo) {
	case modLinear:
		m.linear = true
	case modInvalid:
		m.implicit = true
	}
	return nil
}

type DMABUF struct {
	WL       *cw.WlBuffer
	Pix      []byte
	Released bool
}

func (b *DMABUF) Release(*cw.WlBuffer) error {
	b.Released = true
	return nil
}

func udmabuf(size int, paint func([]byte)) (fd int, mapping []byte, err error) {
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		return -1, nil, err
	}
	defer dev.Close()
	size = (size + os.Getpagesize() - 1) &^ (os.Getpagesize() - 1)
	memfd, err := unix.MemfdCreate("testclient-dmabuf", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return -1, nil, err
	}
	defer func() { err = errors.Join(err, unix.Close(memfd)) }()
	if err := unix.Ftruncate(memfd, int64(size)); err != nil {
		return -1, nil, err
	}
	m, err := unix.Mmap(memfd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return -1, nil, err
	}
	paint(m)
	if _, err := unix.FcntlInt(uintptr(memfd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
		return -1, nil, errors.Join(err, unix.Munmap(m))
	}
	args := udmabufCreateArgs{memfd: uint32(memfd), flags: udmabufCloexec, size: uint64(size)}           //nolint:gosec // file descriptors and page-rounded sizes fit
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), udmabufCreate, uintptr(unsafe.Pointer(&args))) //nolint:gosec // UDMABUF_CREATE reads this struct during the call
	if errno != 0 {
		return -1, nil, errors.Join(errno, unix.Munmap(m))
	}
	return int(r), m, nil
}

func (c *Client) DMABUF(size image.Point, paint Painter) *DMABUF {
	c.t.Helper()
	stride := size.X * 4
	fd, mapping, err := udmabuf(stride*size.Y, func(m []byte) { paint(m[:stride*size.Y], stride, size, 0) })
	if err != nil {
		c.t.Skipf("testclient: no udmabuf to share memory as a dmabuf: %v", err)
	}
	c.t.Cleanup(func() {
		if err := unix.Munmap(mapping); err != nil {
			c.t.Errorf("testclient: unmapping a dmabuf: %v", err)
		}
	})
	defer func() { c.must(unix.Close(fd)) }()
	g, ok := c.globals.byName["zwp_linux_dmabuf_v1"]
	if !ok {
		c.t.Fatal("testclient: the compositor offers no zwp_linux_dmabuf_v1")
	}
	mods := &dmabufModifiers{}
	linux, err := cld.NewZwpLinuxDmabufV1(c.Display, min(g.version, 3), mods)
	c.must(err)
	c.must(c.registry.Bind(g.name, linux))
	c.must(c.Display.Roundtrip())
	modifier := uint64(modLinear)
	switch {
	case mods.linear:
	case mods.implicit:
		modifier = modInvalid
	default:
		c.t.Fatal("testclient: the compositor takes XRGB8888 dmabufs neither linear nor with an implicit modifier")
	}
	params, err := linux.CreateParams(paramsEvents{})
	c.must(err)
	hi, lo := wire.Halves(modifier)
	c.must(params.Add(fd, 0, 0, uint32(stride), hi, lo)) //nolint:gosec // test buffers are small
	b := &DMABUF{Pix: mapping[:stride*size.Y]}
	b.WL, err = params.CreateImmed(b, int32(size.X), int32(size.Y), xrgb8888, 0) //nolint:gosec // test buffers are small
	c.must(err)
	c.must(errors.Join(params.Destroy(), linux.Destroy()))
	return b
}

type paramsEvents struct{}

func (paramsEvents) Created(*cld.ZwpLinuxBufferParamsV1, *cw.WlBuffer) (cw.WlBufferHandler, error) {
	return nil, errors.New("testclient: created after create_immed")
}

func (paramsEvents) Failed(*cld.ZwpLinuxBufferParamsV1) error {
	return errors.New("testclient: the compositor failed a dmabuf")
}
