package nested

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/fourcc"
	"github.com/stubbedev/lys-wm/internal/gpu"
	ld "github.com/stubbedev/lys-wm/internal/protocol/client/linuxdmabuf"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/render/gles"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wlclient"
	"github.com/stubbedev/lys-wm/internal/wlclient/feedback"
)

const (
	dmabufVersion  = 4
	dmabufBuffers  = 2
	dmabufFormat   = fourcc.XRGB8888
	dmabufHostName = "zwp_linux_dmabuf_v1"
)

type dmabufChain struct {
	linux     *ld.ZwpLinuxDmabufV1
	fd        int
	stack     *gles.Stack
	modifiers []fourcc.Modifier
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
	fb, err := feedback.Default(host, c.linux)
	if err != nil {
		return nil, fmt.Errorf("nested: %w", err)
	}
	node, err := feedback.RenderNode(fb.Main)
	if err != nil {
		return nil, fmt.Errorf("nested: the host's renderer: %w", err)
	}
	if c.fd, err = unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0); err != nil {
		return nil, fmt.Errorf("nested: open %s: %w", node, err)
	}
	if c.stack, err = gles.OnGBM(c.fd, paths); err != nil {
		return nil, fmt.Errorf("nested: GLES on %s: %w", node, err)
	}
	renderable, err := c.stack.Display.DMABUFModifiers(dmabufFormat)
	if err != nil {
		return nil, err
	}
	for _, m := range renderable {
		if !m.ExternalOnly && fb.Imports(gpu.FormatModifier{Format: dmabufFormat, Modifier: m.Modifier}) {
			c.modifiers = append(c.modifiers, m.Modifier)
		}
	}
	if len(c.modifiers) == 0 && !fb.Imports(gpu.FormatModifier{Format: dmabufFormat, Modifier: fourcc.Invalid}) {
		return nil, fmt.Errorf("nested: the host and %s share no XRGB8888 modifier", node)
	}
	slices.Sort(c.modifiers)
	return c, nil
}

func (c *dmabufChain) renderer() render.Renderer {
	return c.stack.Renderer
}

func (c *dmabufChain) finish() error {
	return c.stack.Renderer.Finish()
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
		bo, err := c.stack.GBM.Allocate(size, dmabufFormat, c.modifiers, gpu.UseRendering)
		if err != nil {
			return nil, nil, errors.Join(err, free())
		}
		bos = append(bos, bo)
		target, err := c.stack.Renderer.ImportFramebuffer(bo.DMABUF)
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
	hi, lo := wire.Halves(uint64(d.Modifier))
	for i, p := range d.Planes {
		if err := params.Add(p.FD, uint32(i), p.Offset, p.Stride, hi, lo); err != nil {
			return nil, err
		}
	}
	wl, err := params.CreateImmed(bufferEvents{o, buf}, int32(d.Size.X), int32(d.Size.Y), uint32(d.Format), 0) //nolint:gosec // buffer sizes fit the protocol
	if err != nil {
		return nil, err
	}
	return wl, params.Destroy()
}

func (c *dmabufChain) close() error {
	var errs []error
	if c.stack != nil {
		errs = append(errs, c.stack.Close())
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
	return nil, errors.New("nested: the host announced a buffer lys-wm created with create_immed")
}

func (paramsEvents) Failed(*ld.ZwpLinuxBufferParamsV1) error {
	return errors.New("nested: the host rejected a dmabuf buffer")
}
