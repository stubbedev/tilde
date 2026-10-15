package testclient

import (
	"errors"
	"fmt"
	"image"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/fourcc"
	"github.com/stubbedev/tilde/internal/gpu"
	cld "github.com/stubbedev/tilde/internal/protocol/client/linuxdmabuf"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/gles"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wlclient/feedback"
)

const (
	udmabufCreate   = 0x40187542
	udmabufCloexec  = 0x01
	dmabufSync      = 0x40086200
	syncRead        = 1 << 0
	syncWrite       = 1 << 1
	syncEnd         = 1 << 2
	feedbackVersion = 4
	dmabufFormat    = fourcc.XRGB8888
)

type udmabufCreateArgs struct {
	memfd  uint32
	flags  uint32
	offset uint64
	size   uint64
}

type legacyModifiers struct{ linear, implicit bool }

func (m *legacyModifiers) Format(*cld.ZwpLinuxDmabufV1, uint32) error { return nil }

func (m *legacyModifiers) Modifier(_ *cld.ZwpLinuxDmabufV1, format, hi, lo uint32) error {
	if fourcc.Format(format) != dmabufFormat {
		return nil
	}
	switch fourcc.Modifier(wire.Join(hi, lo)) {
	case fourcc.Linear:
		m.linear = true
	case fourcc.Invalid:
		m.implicit = true
	}
	return nil
}

type DMABUF struct {
	WL       *cw.WlBuffer
	Released bool
	c        *Client
	pixels   func() ([]byte, error)
}

func (b *DMABUF) Release(*cw.WlBuffer) error {
	b.Released = true
	return nil
}

func (b *DMABUF) Pixels() []byte {
	b.c.t.Helper()
	pix, err := b.pixels()
	b.c.must(err)
	return pix
}

func cpuAccess(fd int, flags uint64) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), dmabufSync, uintptr(unsafe.Pointer(&flags))) //nolint:gosec // DMA_BUF_IOCTL_SYNC reads the flags during the call
	if errno != 0 {
		return fmt.Errorf("testclient: DMA_BUF_IOCTL_SYNC %#x on fd %d: %w", flags, fd, errno)
	}
	return nil
}

type mapping struct {
	fd     int
	stride int
	size   image.Point
	bytes  []byte
}

func mapDMABUF(fd, stride int, size image.Point) (*mapping, error) {
	b, err := unix.Mmap(fd, 0, stride*size.Y, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("testclient: map %d bytes of a dmabuf: %w", stride*size.Y, err)
	}
	return &mapping{fd: fd, stride: stride, size: size, bytes: b}, nil
}

func (m *mapping) paint(p Painter) error {
	if err := cpuAccess(m.fd, syncWrite); err != nil {
		return err
	}
	p(m.bytes, m.stride, m.size, 0)
	return cpuAccess(m.fd, syncWrite|syncEnd)
}

func (m *mapping) pixels() ([]byte, error) {
	if err := cpuAccess(m.fd, syncRead); err != nil {
		return nil, err
	}
	row := m.size.X * 4
	pix := make([]byte, 0, row*m.size.Y)
	for y := range m.size.Y {
		pix = append(pix, m.bytes[y*m.stride:y*m.stride+row]...)
	}
	return pix, cpuAccess(m.fd, syncRead|syncEnd)
}

func (m *mapping) unmap() error {
	return unix.Munmap(m.bytes)
}

type memory struct {
	fd       int
	stride   int
	modifier fourcc.Modifier
	mapped   *mapping
	pixels   func() ([]byte, error)
	close    func() error
}

func udmabuf(size image.Point, modifier fourcc.Modifier) (m memory, err error) {
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		return memory{}, err
	}
	defer dev.Close()
	stride := size.X * 4
	length := (stride*size.Y + os.Getpagesize() - 1) &^ (os.Getpagesize() - 1)
	memfd, err := unix.MemfdCreate("testclient-dmabuf", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return memory{}, fmt.Errorf("testclient: memfd for a udmabuf: %w", err)
	}
	defer func() { err = errors.Join(err, unix.Close(memfd)) }()
	if err := unix.Ftruncate(memfd, int64(length)); err != nil {
		return memory{}, fmt.Errorf("testclient: size a udmabuf memfd to %d bytes: %w", length, err)
	}
	if _, err := unix.FcntlInt(uintptr(memfd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
		return memory{}, fmt.Errorf("testclient: seal a udmabuf memfd: %w", err)
	}
	args := udmabufCreateArgs{memfd: uint32(memfd), flags: udmabufCloexec, size: uint64(length)}         //nolint:gosec // file descriptors and page-rounded sizes fit
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), udmabufCreate, uintptr(unsafe.Pointer(&args))) //nolint:gosec // UDMABUF_CREATE reads this struct during the call
	if errno != 0 {
		return memory{}, fmt.Errorf("testclient: UDMABUF_CREATE of %d bytes: %w", length, errno)
	}
	fd := int(r)
	mapped, err := mapDMABUF(fd, stride, size)
	if err != nil {
		return memory{}, errors.Join(err, unix.Close(fd))
	}
	return memory{
		fd: fd, stride: stride, modifier: modifier, mapped: mapped, pixels: mapped.pixels,
		close: func() error { return errors.Join(mapped.unmap(), unix.Close(fd)) },
	}, nil
}

func (c *Client) gpuStack(fb feedback.Feedback) *gles.Stack {
	c.t.Helper()
	if c.stack != nil {
		return c.stack
	}
	node, err := feedback.RenderNode(fb.Main)
	c.must(err)
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		c.t.Fatalf("testclient: open %s: %v", node, err)
	}
	s, err := gles.OnGBM(fd, gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		c.must(errors.Join(fmt.Errorf("testclient: GLES on %s: %w", node, err), unix.Close(fd)))
	}
	c.t.Cleanup(func() {
		if err := errors.Join(s.Close(), unix.Close(fd)); err != nil {
			c.t.Errorf("testclient: close GLES on %s: %v", node, err)
		}
	})
	c.stack = s
	return s
}

func readBack(r *gles.Renderer, b gpu.DMABUF) (pix []byte, err error) {
	tex, err := r.ImportTexture(b)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, tex.Destroy()) }()
	target, err := r.NewTarget(b.Size)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, target.Destroy()) }()
	all := image.Rectangle{Max: b.Size}
	p, err := r.Begin(target, render.Region{all})
	if err != nil {
		return nil, err
	}
	p.DrawTexture(tex, render.FRect{W: float64(b.Size.X), H: float64(b.Size.Y)}, all, render.Normal, 1)
	if err := p.End(); err != nil {
		return nil, err
	}
	pix = make([]byte, b.Size.X*4*b.Size.Y)
	return pix, target.Read(pix, b.Size.X*4)
}

func (c *Client) gbmMemory(fb feedback.Feedback, size image.Point, modifiers []fourcc.Modifier, usage gpu.Usage) memory {
	c.t.Helper()
	s := c.gpuStack(fb)
	b, err := s.GBM.Allocate(size, dmabufFormat, modifiers, usage)
	c.must(err)
	if len(b.Planes) != 1 {
		c.must(errors.Join(fmt.Errorf("testclient: a %v buffer with modifier %v has %d planes", dmabufFormat, b.Modifier, len(b.Planes)), b.Close()))
	}
	m := memory{fd: b.Planes[0].FD, stride: int(b.Planes[0].Stride), modifier: b.Modifier, close: b.Close}
	m.pixels = func() ([]byte, error) { return readBack(s.Renderer, b.DMABUF) }
	if b.Modifier != fourcc.Linear {
		return m
	}
	if m.mapped, err = mapDMABUF(m.fd, m.stride, size); err != nil {
		c.must(errors.Join(err, b.Close()))
	}
	m.close = func() error { return errors.Join(m.mapped.unmap(), b.Close()) }
	return m
}

type dmabufGlobal struct {
	linux    *cld.ZwpLinuxDmabufV1
	feedback *feedback.Feedback
	legacy   *legacyModifiers
}

func (c *Client) bindDMABUF() dmabufGlobal {
	c.t.Helper()
	g, ok := c.globals.byName["zwp_linux_dmabuf_v1"]
	if !ok {
		c.t.Fatal("testclient: the compositor offers no zwp_linux_dmabuf_v1")
	}
	d := dmabufGlobal{legacy: &legacyModifiers{}}
	var err error
	d.linux, err = cld.NewZwpLinuxDmabufV1(c.Display, min(g.version, feedbackVersion), d.legacy)
	c.must(err)
	c.must(c.registry.Bind(g.name, d.linux))
	c.must(c.Display.Roundtrip())
	if g.version >= feedbackVersion {
		fb, err := feedback.Default(c.Display, d.linux)
		c.must(err)
		d.feedback = &fb
	}
	return d
}

func (c *Client) legacyMemory(mods *legacyModifiers, size image.Point) memory {
	c.t.Helper()
	modifier := fourcc.Linear
	switch {
	case mods.linear:
	case mods.implicit:
		modifier = fourcc.Invalid
	default:
		c.t.Fatalf("testclient: the compositor takes %v dmabufs neither linear nor with an implicit modifier", dmabufFormat)
	}
	m, err := udmabuf(size, modifier)
	if err != nil {
		c.t.Skipf("testclient: no udmabuf to share memory as a dmabuf: %v", err)
	}
	return m
}

func (c *Client) DMABUF(size image.Point, paint Painter) *DMABUF {
	c.t.Helper()
	g := c.bindDMABUF()
	var mem memory
	if g.feedback == nil {
		mem = c.legacyMemory(g.legacy, size)
	} else {
		linear := gpu.FormatModifier{Format: dmabufFormat, Modifier: fourcc.Linear}
		if !g.feedback.Imports(linear) {
			c.t.Fatalf("testclient: the compositor's main device takes no %v dmabufs for the client to paint", linear)
		}
		mem = c.gbmMemory(*g.feedback, size, []fourcc.Modifier{fourcc.Linear}, 0)
	}
	b := c.share(g, mem, size)
	if mem.mapped == nil {
		c.t.Fatalf("testclient: GBM allocated a %v buffer with modifier %v when asked for linear", dmabufFormat, mem.modifier)
	}
	c.must(mem.mapped.paint(paint))
	return b
}

func (c *Client) RenderTarget(size image.Point, modifiers []fourcc.Modifier) *DMABUF {
	c.t.Helper()
	g := c.bindDMABUF()
	if g.feedback == nil {
		return c.share(g, c.legacyMemory(g.legacy, size), size)
	}
	if len(modifiers) == 0 {
		c.t.Fatalf("testclient: no modifiers to render a %v target with", dmabufFormat)
	}
	return c.share(g, c.gbmMemory(*g.feedback, size, modifiers, gpu.UseRendering), size)
}

func (c *Client) share(g dmabufGlobal, mem memory, size image.Point) *DMABUF {
	c.t.Helper()
	c.t.Cleanup(func() {
		if err := mem.close(); err != nil {
			c.t.Errorf("testclient: free a dmabuf: %v", err)
		}
	})
	params, err := g.linux.CreateParams(paramsEvents{})
	c.must(err)
	hi, lo := wire.Halves(uint64(mem.modifier))
	c.must(params.Add(mem.fd, 0, 0, uint32(mem.stride), hi, lo)) //nolint:gosec // test buffers are small
	b := &DMABUF{c: c, pixels: mem.pixels}
	b.WL, err = params.CreateImmed(b, int32(size.X), int32(size.Y), uint32(dmabufFormat), 0) //nolint:gosec // test buffers are small
	c.must(err)
	c.must(errors.Join(params.Destroy(), g.linux.Destroy()))
	return b
}

type paramsEvents struct{}

func (paramsEvents) Created(*cld.ZwpLinuxBufferParamsV1, *cw.WlBuffer) (cw.WlBufferHandler, error) {
	return nil, errors.New("testclient: created after create_immed")
}

func (paramsEvents) Failed(*cld.ZwpLinuxBufferParamsV1) error {
	return errors.New("testclient: the compositor failed a dmabuf")
}
