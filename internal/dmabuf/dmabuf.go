// Package dmabuf implements zwp_linux_dmabuf_v1: clients share GPU buffers as
// dmabuf planes, checked against what the renderer can import, and learn the
// formats, modifiers and device through feedback, which for a surface that
// could reach a display plane leads with what that plane scans out.
package dmabuf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/fourcc"
	"github.com/stubbedev/tilde/internal/gpu"
	sld "github.com/stubbedev/tilde/internal/protocol/server/linuxdmabuf"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	feedbackVersion = 5
	legacyVersion   = 3
	maxPlanes       = 4
	tableEntry      = 16
)

type Importer interface {
	DMABUFFormats() ([]gpu.FormatModifier, error)
	MainDevice() (uint64, error)
	CheckDMABUF(b gpu.DMABUF) error
}

type Listener interface {
	BufferDestroyed(b *Buffer) error
	SurfaceScanout(s *surface.Surface) gpu.ScanoutTarget
}

type Manager struct {
	importer Importer
	listener Listener
	formats  []gpu.FormatModifier
	index    map[gpu.FormatModifier]uint16
	all      []byte
	device   []byte
	table    int
	version  uint32
	surfaces map[*surface.Surface][]surfaceFeedback
}

type surfaceFeedback struct {
	c   *server.Client
	res *sld.ZwpLinuxDmabufFeedbackV1
}

func New(d *server.Display, imp Importer, l Listener) (*Manager, error) {
	formats, err := imp.DMABUFFormats()
	if err != nil {
		return nil, fmt.Errorf("dmabuf: %w", err)
	}
	if len(formats) == 0 || len(formats) > math.MaxUint16 {
		return nil, fmt.Errorf("dmabuf: the renderer imports %d format and modifier pairs", len(formats))
	}
	m := &Manager{importer: imp, listener: l, formats: formats, table: -1, version: legacyVersion, surfaces: map[*surface.Surface][]surfaceFeedback{}}
	m.index = make(map[gpu.FormatModifier]uint16, len(formats))
	for i, f := range formats {
		m.index[f] = uint16(i)
	}
	m.all = m.indices(formats)
	dev, err := imp.MainDevice()
	switch {
	case err == nil:
		m.device = binary.NativeEndian.AppendUint64(nil, dev)
		m.version = feedbackVersion
		if m.table, err = formatTable(formats); err != nil {
			return nil, err
		}
	case !errors.Is(err, gpu.ErrUnsupported):
		return nil, fmt.Errorf("dmabuf: %w", err)
	}
	if _, err := d.AddGlobal(sld.ZwpLinuxDmabufV1Interface, m.version, m.bind); err != nil {
		return nil, errors.Join(err, m.Close())
	}
	return m, nil
}

func formatTable(formats []gpu.FormatModifier) (int, error) {
	table := make([]byte, 0, len(formats)*tableEntry)
	for _, f := range formats {
		table = binary.NativeEndian.AppendUint32(table, uint32(f.Format))
		table = binary.NativeEndian.AppendUint32(table, 0)
		table = binary.NativeEndian.AppendUint64(table, uint64(f.Modifier))
	}
	fd, err := unix.MemfdCreate("tilde-dmabuf-formats", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return -1, fmt.Errorf("dmabuf: memfd_create: %w", err)
	}
	if _, err := unix.Write(fd, table); err != nil {
		return -1, errors.Join(fmt.Errorf("dmabuf: write the format table: %w", err), unix.Close(fd))
	}
	seals := unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE | unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, seals); err != nil {
		return -1, errors.Join(fmt.Errorf("dmabuf: seal the format table: %w", err), unix.Close(fd))
	}
	return fd, nil
}

func (m *Manager) Version() uint32 {
	return m.version
}

func (m *Manager) Close() error {
	if m.table < 0 {
		return nil
	}
	err := unix.Close(m.table)
	m.table = -1
	return err
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, version uint32) error {
	res, err := sld.NewZwpLinuxDmabufV1(c, id, version, requests{m, c})
	if err != nil {
		return err
	}
	if version >= 4 {
		return nil
	}
	var errs []error
	for i, f := range m.formats {
		if version < 3 {
			if i == 0 || m.formats[i-1].Format != f.Format {
				errs = append(errs, res.SendFormat(uint32(f.Format)))
			}
			continue
		}
		hi, lo := wire.Halves(uint64(f.Modifier))
		errs = append(errs, res.SendModifier(uint32(f.Format), hi, lo))
	}
	return errors.Join(errs...)
}

type requests struct {
	m *Manager
	c *server.Client
}

func (requests) Destroy(*sld.ZwpLinuxDmabufV1) error {
	return nil
}

func (r requests) CreateParams(_ *sld.ZwpLinuxDmabufV1, res *sld.ZwpLinuxBufferParamsV1) (sld.ZwpLinuxBufferParamsV1Handler, error) {
	p := &params{m: r.m, c: r.c, res: res}
	server.OnDestroy(res, p.closePlanes)
	return p, nil
}

func (m *Manager) indices(formats []gpu.FormatModifier) []byte {
	var indices []byte
	for _, f := range formats {
		if i, ok := m.index[f]; ok {
			indices = binary.NativeEndian.AppendUint16(indices, i)
		}
	}
	return indices
}

func tranche(res *sld.ZwpLinuxDmabufFeedbackV1, device, indices []byte, flags sld.ZwpLinuxDmabufFeedbackV1TrancheFlags) error {
	return errors.Join(
		res.SendTrancheTargetDevice(device),
		res.SendTrancheFormats(indices),
		res.SendTrancheFlags(flags),
		res.SendTrancheDone(),
	)
}

func (m *Manager) feedback(res *sld.ZwpLinuxDmabufFeedbackV1, scanout gpu.ScanoutTarget) error {
	err := errors.Join(
		res.SendFormatTable(m.table, uint32(len(m.formats)*tableEntry)), //nolint:gosec // capped in New
		res.SendMainDevice(m.device),
	)
	if indices := m.indices(scanout.Formats); len(indices) > 0 {
		err = errors.Join(err, tranche(res, binary.NativeEndian.AppendUint64(nil, scanout.Device), indices, sld.ZwpLinuxDmabufFeedbackV1TrancheFlagsScanout))
	}
	return errors.Join(err, tranche(res, m.device, m.all, 0), res.SendDone())
}

func (r requests) GetDefaultFeedback(_ *sld.ZwpLinuxDmabufV1, res *sld.ZwpLinuxDmabufFeedbackV1) (sld.ZwpLinuxDmabufFeedbackV1Handler, error) {
	return feedback{}, r.c.Sent(r.m.feedback(res, gpu.ScanoutTarget{}))
}

func (r requests) GetSurfaceFeedback(_ *sld.ZwpLinuxDmabufV1, res *sld.ZwpLinuxDmabufFeedbackV1, wl *sw.WlSurface) (sld.ZwpLinuxDmabufFeedbackV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	r.m.surfaces[s] = append(r.m.surfaces[s], surfaceFeedback{c: r.c, res: res})
	server.OnDestroy(res, func() error {
		r.m.forget(s, res)
		return nil
	})
	return feedback{}, r.c.Sent(r.m.feedback(res, r.m.listener.SurfaceScanout(s)))
}

func (m *Manager) forget(s *surface.Surface, res *sld.ZwpLinuxDmabufFeedbackV1) {
	left := slices.DeleteFunc(m.surfaces[s], func(f surfaceFeedback) bool { return f.res == res })
	if len(left) == 0 {
		delete(m.surfaces, s)
		return
	}
	m.surfaces[s] = left
}

func (m *Manager) ScanoutChanged(s *surface.Surface) error {
	feedbacks := m.surfaces[s]
	if len(feedbacks) == 0 {
		return nil
	}
	t := m.listener.SurfaceScanout(s)
	var err error
	for _, f := range feedbacks {
		err = errors.Join(err, f.c.Sent(m.feedback(f.res, t)))
	}
	return err
}

type feedback struct{}

func (feedback) Destroy(*sld.ZwpLinuxDmabufFeedbackV1) error {
	return nil
}

type plane struct {
	fd       int
	offset   uint32
	stride   uint32
	modifier fourcc.Modifier
}

type params struct {
	m      *Manager
	c      *server.Client
	res    *sld.ZwpLinuxBufferParamsV1
	planes [maxPlanes]*plane
	used   bool
}

func fail(res *sld.ZwpLinuxBufferParamsV1, code sld.ZwpLinuxBufferParamsV1Error, format string, args ...any) error {
	return res.ProtocolError(code, format, args...)
}

func (p *params) closePlanes() error {
	var errs []error
	for i, pl := range p.planes {
		if pl != nil {
			errs = append(errs, unix.Close(pl.fd))
			p.planes[i] = nil
		}
	}
	return errors.Join(errs...)
}

func (p *params) Destroy(*sld.ZwpLinuxBufferParamsV1) error {
	return nil
}

func (p *params) Add(obj *sld.ZwpLinuxBufferParamsV1, fd int, idx, offset, stride, hi, lo uint32) error {
	modifier := fourcc.Modifier(wire.Join(hi, lo))
	var err error
	switch {
	case p.used:
		err = fail(obj, sld.ZwpLinuxBufferParamsV1ErrorAlreadyUsed, "params already created a buffer")
	case idx >= maxPlanes:
		err = fail(obj, sld.ZwpLinuxBufferParamsV1ErrorPlaneIdx, "plane %d is beyond the %d dmabuf planes", idx, maxPlanes)
	case p.planes[idx] != nil:
		err = fail(obj, sld.ZwpLinuxBufferParamsV1ErrorPlaneSet, "plane %d is already set", idx)
	}
	if err == nil {
		for i, pl := range p.planes {
			if pl != nil && pl.modifier != modifier {
				err = fail(obj, sld.ZwpLinuxBufferParamsV1ErrorInvalidFormat, "plane %d has modifier %v, but plane %d has %v", idx, modifier, i, pl.modifier)
				break
			}
		}
	}
	if err != nil {
		return errors.Join(err, unix.Close(fd))
	}
	p.planes[idx] = &plane{fd: fd, offset: offset, stride: stride, modifier: modifier}
	return nil
}

var verticalSubsampling = map[fourcc.Format][]int{
	fourcc.ARGB8888: {1}, fourcc.XRGB8888: {1}, fourcc.ABGR8888: {1}, fourcc.XBGR8888: {1},
	fourcc.NV12: {1, 2}, fourcc.NV21: {1, 2}, fourcc.NV16: {1, 1}, fourcc.P010: {1, 2},
	fourcc.YUV420: {1, 2, 2}, fourcc.YVU420: {1, 2, 2},
}

func planeRows(format fourcc.Format, plane int, height int32) uint64 {
	sub := 1
	if subs, ok := verticalSubsampling[format]; ok {
		sub = subs[plane]
	}
	return uint64((int(height) + sub - 1) / sub) //nolint:gosec // height was checked to be positive
}

func (p *params) build(width, height int32, format fourcc.Format, flags sld.ZwpLinuxBufferParamsV1Flags) (gpu.DMABUF, error) {
	obj := p.res
	if p.used {
		return gpu.DMABUF{}, fail(obj, sld.ZwpLinuxBufferParamsV1ErrorAlreadyUsed, "params already created a buffer")
	}
	p.used = true
	n := 0
	for n < maxPlanes && p.planes[n] != nil {
		n++
	}
	for i := n; i < maxPlanes; i++ {
		if p.planes[i] != nil {
			return gpu.DMABUF{}, fail(obj, sld.ZwpLinuxBufferParamsV1ErrorIncomplete, "plane %d is set but plane %d is missing", i, n)
		}
	}
	if n == 0 {
		return gpu.DMABUF{}, fail(obj, sld.ZwpLinuxBufferParamsV1ErrorIncomplete, "no planes")
	}
	modifier := p.planes[0].modifier
	if !slices.Contains(p.m.formats, gpu.FormatModifier{Format: format, Modifier: modifier}) {
		return gpu.DMABUF{}, fail(obj, sld.ZwpLinuxBufferParamsV1ErrorInvalidFormat, "format %v with modifier %v is not supported", format, modifier)
	}
	if subs, ok := verticalSubsampling[format]; ok && n != len(subs) {
		return gpu.DMABUF{}, fail(obj, sld.ZwpLinuxBufferParamsV1ErrorIncomplete, "format %v has %d planes, not %d", format, len(subs), n)
	}
	if width <= 0 || height <= 0 {
		return gpu.DMABUF{}, fail(obj, sld.ZwpLinuxBufferParamsV1ErrorInvalidDimensions, "invalid size %dx%d", width, height)
	}
	if flags != 0 {
		return gpu.DMABUF{}, errUnsupportedFlags
	}
	b := gpu.DMABUF{Size: image.Pt(int(width), int(height)), Format: format, Modifier: modifier}
	for i, pl := range p.planes[:n] {
		end := uint64(pl.offset) + uint64(pl.stride)*planeRows(format, i, height)
		if end > math.MaxUint32 {
			return gpu.DMABUF{}, fail(obj, sld.ZwpLinuxBufferParamsV1ErrorOutOfBounds, "plane %d ends past 4 GiB", i)
		}
		if size, err := unix.Seek(pl.fd, 0, unix.SEEK_END); err == nil {
			if pl.offset >= uint32(size) || (modifier == fourcc.Linear && end > uint64(size)) { //nolint:gosec // a dmabuf of 4 GiB or more fails the check above
				return gpu.DMABUF{}, fail(obj, sld.ZwpLinuxBufferParamsV1ErrorOutOfBounds, "plane %d at offset %d with stride %d does not fit %d bytes", i, pl.offset, pl.stride, size)
			}
		}
		b.Planes = append(b.Planes, gpu.Plane{FD: pl.fd, Offset: pl.offset, Stride: pl.stride})
	}
	for i := range p.planes {
		p.planes[i] = nil
	}
	return b, nil
}

var errUnsupportedFlags = errors.New("dmabuf: y-inverted and interlaced buffers are not supported")

func closeAll(b gpu.DMABUF) error {
	var errs []error
	for _, pl := range b.Planes {
		errs = append(errs, unix.Close(pl.FD))
	}
	return errors.Join(errs...)
}

func (p *params) check(width, height int32, format fourcc.Format, flags sld.ZwpLinuxBufferParamsV1Flags) (gpu.DMABUF, error) {
	b, err := p.build(width, height, format, flags)
	if err != nil {
		return gpu.DMABUF{}, err
	}
	if err := p.m.importer.CheckDMABUF(b); err != nil {
		return gpu.DMABUF{}, errors.Join(fmt.Errorf("dmabuf: import %dx%d format %v with modifier %v: %w", width, height, format, b.Modifier, err), closeAll(b))
	}
	return b, nil
}

func (p *params) Create(obj *sld.ZwpLinuxBufferParamsV1, width, height int32, format uint32, flags sld.ZwpLinuxBufferParamsV1Flags) error {
	b, err := p.check(width, height, fourcc.Format(format), flags)
	var pe *wire.ProtocolError
	switch {
	case errors.As(err, &pe):
		return err
	case err != nil:
		return p.c.Sent(obj.SendFailed())
	}
	buf := &Buffer{m: p.m, dmabuf: b}
	if buf.resource, err = obj.SendCreated(buf); err != nil {
		return errors.Join(err, closeAll(b))
	}
	server.OnDestroy(buf.resource, buf.destroyed)
	return nil
}

func (p *params) CreateImmed(obj *sld.ZwpLinuxBufferParamsV1, res *sw.WlBuffer, width, height int32, format uint32, flags sld.ZwpLinuxBufferParamsV1Flags) (sw.WlBufferHandler, error) {
	b, err := p.check(width, height, fourcc.Format(format), flags)
	var pe *wire.ProtocolError
	switch {
	case errors.As(err, &pe):
		return nil, err
	case err != nil:
		return nil, fail(obj, sld.ZwpLinuxBufferParamsV1ErrorInvalidWlBuffer, "%v", err)
	}
	buf := &Buffer{m: p.m, resource: res, dmabuf: b}
	server.OnDestroy(res, buf.destroyed)
	return buf, nil
}

func (p *params) SetSamplingDevice(obj *sld.ZwpLinuxBufferParamsV1, _ []byte) error {
	return fail(obj, sld.ZwpLinuxBufferParamsV1ErrorInvalidDevTSize, "set_sampling_device needs version 6; tilde offers %d", p.m.version)
}

type Buffer struct {
	dmabuf   gpu.DMABUF
	m        *Manager
	resource *sw.WlBuffer
}

func (b *Buffer) Size() image.Point {
	return b.dmabuf.Size
}

func (b *Buffer) DMABUF() gpu.DMABUF {
	return b.dmabuf
}

func (b *Buffer) Release() error {
	return b.resource.SendRelease()
}

func (b *Buffer) Destroy(*sw.WlBuffer) error {
	return nil
}

func (b *Buffer) destroyed() error {
	return errors.Join(b.m.listener.BufferDestroyed(b), closeAll(b.dmabuf))
}
