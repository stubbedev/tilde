package nested

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/gpu"
	ld "github.com/stubbedev/tilde/internal/protocol/client/linuxdmabuf"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/gles"
	"github.com/stubbedev/tilde/internal/wlclient"
)

const (
	dmabufVersion  = 4
	dmabufBuffers  = 2
	formatEntry    = 16
	dmabufFormat   = gpu.FormatXRGB8888
	dmabufHostName = "zwp_linux_dmabuf_v1"
)

type feedback struct {
	table   []byte
	main    uint64
	target  uint64
	formats []uint16
	usable  map[uint64]bool
	done    bool
	err     error
}

func (f *feedback) fail(err error) error {
	if f.err == nil {
		f.err = err
	}
	return nil
}

func (f *feedback) Done(*ld.ZwpLinuxDmabufFeedbackV1) error {
	f.done = true
	return nil
}

func (f *feedback) FormatTable(_ *ld.ZwpLinuxDmabufFeedbackV1, fd int, size uint32) error {
	m, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		return f.fail(errors.Join(fmt.Errorf("nested: map the host's dmabuf format table: %w", err), unix.Close(fd)))
	}
	f.table = slices.Clone(m)
	if err := errors.Join(unix.Munmap(m), unix.Close(fd)); err != nil {
		return f.fail(err)
	}
	return nil
}

func devT(b []byte) (uint64, error) {
	if len(b) != 8 {
		return 0, fmt.Errorf("nested: a dev_t of %d bytes", len(b))
	}
	return binary.NativeEndian.Uint64(b), nil
}

func (f *feedback) MainDevice(_ *ld.ZwpLinuxDmabufFeedbackV1, b []byte) error {
	dev, err := devT(b)
	if err != nil {
		return f.fail(err)
	}
	f.main = dev
	return nil
}

func (f *feedback) TrancheTargetDevice(_ *ld.ZwpLinuxDmabufFeedbackV1, b []byte) error {
	dev, err := devT(b)
	if err != nil {
		return f.fail(err)
	}
	f.target = dev
	return nil
}

func (f *feedback) TrancheFormats(_ *ld.ZwpLinuxDmabufFeedbackV1, indices []byte) error {
	for i := 0; i+1 < len(indices); i += 2 {
		f.formats = append(f.formats, binary.NativeEndian.Uint16(indices[i:]))
	}
	return nil
}

func (f *feedback) TrancheFlags(*ld.ZwpLinuxDmabufFeedbackV1, ld.ZwpLinuxDmabufFeedbackV1TrancheFlags) error {
	return nil
}

func (f *feedback) TrancheDone(*ld.ZwpLinuxDmabufFeedbackV1) error {
	if f.target == f.main {
		for _, i := range f.formats {
			at := int(i) * formatEntry
			if at+formatEntry > len(f.table) {
				return f.fail(fmt.Errorf("nested: dmabuf format index %d is outside the host's table", i))
			}
			if binary.NativeEndian.Uint32(f.table[at:]) == dmabufFormat {
				f.usable[binary.NativeEndian.Uint64(f.table[at+8:])] = true
			}
		}
	}
	f.formats = f.formats[:0]
	return nil
}

func renderNode(dev uint64) (string, error) {
	dir := fmt.Sprintf("/sys/dev/char/%d:%d/device/drm", unix.Major(dev), unix.Minor(dev))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("nested: find the render node of the host's device %d:%d: %w", unix.Major(dev), unix.Minor(dev), err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "renderD") {
			return filepath.Join("/dev/dri", e.Name()), nil
		}
	}
	return "", fmt.Errorf("nested: the host's device %d:%d has no render node", unix.Major(dev), unix.Minor(dev))
}

type dmabufChain struct {
	linux     *ld.ZwpLinuxDmabufV1
	fd        int
	eglLib    *gpu.Library
	gbmLib    *gpu.GBMLibrary
	gbm       *gpu.GBM
	display   *gpu.Display
	gles      *gles.Renderer
	modifiers []uint64
}

func newDMABUFChain(host *wlclient.Display, globals registry, reg *cw.WlRegistry, paths gpu.Paths) (c *dmabufChain, err error) {
	g, ok := globals[dmabufHostName]
	if !ok || g.version < dmabufVersion {
		return nil, fmt.Errorf("nested: the GLES renderer needs the host's %s version %d", dmabufHostName, dmabufVersion)
	}
	c = &dmabufChain{fd: -1}
	defer func() {
		if err != nil {
			err = errors.Join(err, c.close())
		}
	}()
	if c.linux, err = ld.NewZwpLinuxDmabufV1(host, dmabufVersion, dmabufEvents{}); err != nil {
		return nil, err
	}
	if err := reg.Bind(g.name, c.linux); err != nil {
		return nil, err
	}
	fb := &feedback{usable: map[uint64]bool{}}
	feed, err := c.linux.GetDefaultFeedback(fb)
	if err != nil {
		return nil, err
	}
	for !fb.done && fb.err == nil {
		if err := host.Dispatch(); err != nil {
			return nil, fmt.Errorf("nested: read the host's dmabuf feedback: %w", err)
		}
	}
	if err := errors.Join(fb.err, feed.Destroy()); err != nil {
		return nil, err
	}
	node, err := renderNode(fb.main)
	if err != nil {
		return nil, err
	}
	if c.fd, err = unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0); err != nil {
		return nil, fmt.Errorf("nested: open %s: %w", node, err)
	}
	if c.eglLib, err = gpu.Load(paths); err != nil {
		return nil, err
	}
	if c.gbmLib, err = gpu.LoadGBM(paths); err != nil {
		return nil, err
	}
	if c.gbm, err = c.gbmLib.Open(c.fd); err != nil {
		return nil, err
	}
	if c.display, err = c.eglLib.OnGBM(c.gbm); err != nil {
		return nil, fmt.Errorf("nested: EGL on %s: %w", node, err)
	}
	if c.gles, err = gles.New(c.display); err != nil {
		return nil, err
	}
	renderable, err := c.display.DMABUFModifiers(dmabufFormat)
	if err != nil {
		return nil, err
	}
	for _, m := range renderable {
		if !m.ExternalOnly && fb.usable[m.Modifier] {
			c.modifiers = append(c.modifiers, m.Modifier)
		}
	}
	if len(c.modifiers) == 0 && !fb.usable[gpu.ModifierInvalid] {
		return nil, fmt.Errorf("nested: the host and %s share no XRGB8888 modifier", node)
	}
	slices.Sort(c.modifiers)
	return c, nil
}

func (c *dmabufChain) renderer() render.Renderer {
	return c.gles
}

func (c *dmabufChain) finish() error {
	return c.gles.Finish()
}

func (c *dmabufChain) allocate(o *Output, size image.Point) ([]*buffer, func() error, error) {
	var (
		bos     []*gpu.Buffer
		targets []*gles.Framebuffer
	)
	free := func() error {
		var errs []error
		for _, f := range targets {
			errs = append(errs, f.Destroy())
		}
		for _, b := range bos {
			errs = append(errs, b.Close())
		}
		return errors.Join(errs...)
	}
	buffers := make([]*buffer, dmabufBuffers)
	for i := range buffers {
		bo, err := c.gbm.Allocate(size, dmabufFormat, c.modifiers, gpu.UseRendering)
		if err != nil {
			return nil, nil, errors.Join(err, free())
		}
		bos = append(bos, bo)
		target, err := c.gles.ImportFramebuffer(bo.DMABUF)
		if err != nil {
			return nil, nil, errors.Join(err, free())
		}
		targets = append(targets, target)
		buf := &buffer{target: target}
		if buf.wl, err = c.hostBuffer(o, buf, bo.DMABUF); err != nil {
			return nil, nil, errors.Join(err, free())
		}
		buffers[i] = buf
	}
	return buffers, free, nil
}

func (c *dmabufChain) hostBuffer(o *Output, buf *buffer, d gpu.DMABUF) (*cw.WlBuffer, error) {
	params, err := c.linux.CreateParams(paramsEvents{})
	if err != nil {
		return nil, err
	}
	for i, p := range d.Planes {
		if err := params.Add(p.FD, uint32(i), p.Offset, p.Stride, uint32(d.Modifier>>32), uint32(d.Modifier)); err != nil { //nolint:gosec // plane indices are below four; the protocol splits the modifier
			return nil, err
		}
	}
	wl, err := params.CreateImmed(bufferEvents{o, buf}, int32(d.Size.X), int32(d.Size.Y), d.Format, 0) //nolint:gosec // buffer sizes fit the protocol
	if err != nil {
		return nil, err
	}
	return wl, params.Destroy()
}

func (c *dmabufChain) close() error {
	var errs []error
	if c.gles != nil {
		errs = append(errs, c.gles.Close())
	}
	if c.display != nil {
		errs = append(errs, c.display.Close())
	}
	if c.gbm != nil {
		c.gbm.Close()
	}
	if c.gbmLib != nil {
		errs = append(errs, c.gbmLib.Close())
	}
	if c.eglLib != nil {
		errs = append(errs, c.eglLib.Close())
	}
	if c.fd >= 0 {
		errs = append(errs, unix.Close(c.fd))
	}
	if c.linux != nil {
		errs = append(errs, c.linux.Destroy())
	}
	return errors.Join(errs...)
}

type dmabufEvents struct{}

func (dmabufEvents) Format(*ld.ZwpLinuxDmabufV1, uint32) error { return nil }

func (dmabufEvents) Modifier(*ld.ZwpLinuxDmabufV1, uint32, uint32, uint32) error { return nil }

type paramsEvents struct{}

func (paramsEvents) Created(*ld.ZwpLinuxBufferParamsV1, *cw.WlBuffer) (cw.WlBufferHandler, error) {
	return nil, errors.New("nested: the host announced a buffer tilde created with create_immed")
}

func (paramsEvents) Failed(*ld.ZwpLinuxBufferParamsV1) error {
	return errors.New("nested: the host rejected a dmabuf buffer")
}
