// Package syncobj implements wp_linux_drm_syncobj_manager_v1: explicit
// synchronization through DRM timeline syncobjs. A commit waits for its
// acquire point before it takes effect, and a buffer's release point signals
// once the GPU has finished with it and it has left the screen.
package syncobj

import (
	"errors"
	"fmt"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/dmabuf"
	"github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/loop"
	sls "github.com/stubbedev/tilde/internal/protocol/server/linuxdrmsyncobj"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const version = 1

type Fencer interface {
	SyncFile() (int, error)
}

type Screen interface {
	OffScreen(b *dmabuf.Buffer, f func() error) error
}

type Manager struct {
	loop     *loop.Loop
	dev      *drm.Device
	fencer   Fencer
	screen   Screen
	surfaces map[*surface.Surface]*syncSurface
}

func New(d *server.Display, l *loop.Loop, dev *drm.Device, f Fencer, screen Screen) (*Manager, error) {
	timelines, err := dev.Cap(drm.CapSyncobjTimeline)
	if err != nil {
		return nil, fmt.Errorf("syncobj: %w", err)
	}
	if timelines == 0 {
		return nil, fmt.Errorf("syncobj: %s has no timeline syncobjs", dev.Path())
	}
	m := &Manager{loop: l, dev: dev, fencer: f, screen: screen, surfaces: map[*surface.Surface]*syncSurface{}}
	if _, err := d.AddGlobal(sls.WpLinuxDrmSyncobjManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := sls.NewWpLinuxDrmSyncobjManagerV1(c, id, v, requests{m, c})
	return err
}

type timeline struct {
	m      *Manager
	handle drm.Syncobj
	refs   int
}

func (t *timeline) ref() *timeline {
	t.refs++
	return t
}

func (t *timeline) unref() error {
	if t.refs--; t.refs > 0 {
		return nil
	}
	return t.m.dev.DestroySyncobj(t.handle)
}

type point struct {
	t     *timeline
	value uint64
}

func newPoint(res *sls.WpLinuxDrmSyncobjTimelineV1, hi, lo uint32) (*point, error) {
	h, ok := res.Handler().(*timelineRequests)
	if !ok {
		return nil, fmt.Errorf("syncobj: timeline@%d has handler %T", res.Base().ID(), res.Handler())
	}
	return &point{t: h.t.ref(), value: wire.Join(hi, lo)}, nil
}

func (p *point) drop() error {
	if p == nil {
		return nil
	}
	return p.t.unref()
}

func (p *point) signal() error {
	if p == nil {
		return nil
	}
	return errors.Join(p.t.m.dev.Signal(p.t.handle, p.value), p.drop())
}

func (p *point) signalAfterGPU() error {
	if p == nil {
		return nil
	}
	fd, err := p.t.m.fencer.SyncFile()
	if err != nil {
		return errors.Join(fmt.Errorf("syncobj: fence a release point: %w", err), p.drop())
	}
	return errors.Join(p.t.m.dev.ImportSyncFile(p.t.handle, p.value, fd), unix.Close(fd), p.drop())
}

type requests struct {
	m *Manager
	c *server.Client
}

func (requests) Destroy(*sls.WpLinuxDrmSyncobjManagerV1) error {
	return nil
}

func (r requests) ImportTimeline(obj *sls.WpLinuxDrmSyncobjManagerV1, res *sls.WpLinuxDrmSyncobjTimelineV1, fd int) (sls.WpLinuxDrmSyncobjTimelineV1Handler, error) {
	handle, err := r.m.dev.ImportSyncobj(fd)
	if closeErr := unix.Close(fd); closeErr != nil && err == nil {
		return nil, errors.Join(closeErr, r.m.dev.DestroySyncobj(handle))
	}
	if err != nil {
		return nil, obj.ProtocolError(sls.WpLinuxDrmSyncobjManagerV1ErrorInvalidTimeline, fmt.Sprintf("not a syncobj: %v", err))
	}
	t := &timeline{m: r.m, handle: handle, refs: 1}
	server.OnDestroy(res, t.unref)
	return &timelineRequests{t: t}, nil
}

type timelineRequests struct{ t *timeline }

func (*timelineRequests) Destroy(*sls.WpLinuxDrmSyncobjTimelineV1) error {
	return nil
}

func (r requests) GetSurface(obj *sls.WpLinuxDrmSyncobjManagerV1, res *sls.WpLinuxDrmSyncobjSurfaceV1, wl *sw.WlSurface) (sls.WpLinuxDrmSyncobjSurfaceV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	if prev, ok := r.m.surfaces[s]; ok && prev.res != nil {
		return nil, obj.ProtocolError(sls.WpLinuxDrmSyncobjManagerV1ErrorSurfaceExists, fmt.Sprintf("wl_surface@%d already has a syncobj surface", wl.Base().ID()))
	}
	ss, ok := r.m.surfaces[s]
	if !ok {
		ss = &syncSurface{m: r.m, s: s}
		ss.hooks = &surface.Hooks{Commit: ss.commit, Destroyed: ss.surfaceGone}
		s.AddHooks(ss.hooks)
		r.m.surfaces[s] = ss
	}
	ss.res = res
	server.OnDestroy(res, ss.objectGone)
	return ss, nil
}

type wait struct {
	src     *loop.Source
	efd     int
	acquire *point
	hold    *surface.Hold
}

func (w *wait) cancel() error {
	return errors.Join(w.src.Remove(), unix.Close(w.efd), w.acquire.drop())
}

type syncSurface struct {
	m       *Manager
	s       *surface.Surface
	res     *sls.WpLinuxDrmSyncobjSurfaceV1
	hooks   *surface.Hooks
	acquire *point
	release *point
	latched []latch
	current latch
	waits   []*wait
}

type latch struct {
	release *point
	buffer  *dmabuf.Buffer
}

func (ss *syncSurface) retire(l latch) error {
	if l.release == nil {
		return nil
	}
	return ss.m.screen.OffScreen(l.buffer, l.release.signalAfterGPU)
}

func (ss *syncSurface) Destroy(*sls.WpLinuxDrmSyncobjSurfaceV1) error {
	return nil
}

func (ss *syncSurface) SetAcquirePoint(obj *sls.WpLinuxDrmSyncobjSurfaceV1, t *sls.WpLinuxDrmSyncobjTimelineV1, hi, lo uint32) error {
	p, err := newPoint(t, hi, lo)
	if err != nil {
		return err
	}
	err = ss.acquire.drop()
	ss.acquire = p
	return err
}

func (ss *syncSurface) SetReleasePoint(obj *sls.WpLinuxDrmSyncobjSurfaceV1, t *sls.WpLinuxDrmSyncobjTimelineV1, hi, lo uint32) error {
	p, err := newPoint(t, hi, lo)
	if err != nil {
		return err
	}
	err = ss.release.drop()
	ss.release = p
	return err
}

func (ss *syncSurface) fail(code sls.WpLinuxDrmSyncobjSurfaceV1Error, format string, args ...any) error {
	return ss.res.ProtocolError(code, fmt.Sprintf(format, args...))
}

func (ss *syncSurface) check(buffer surface.Buffer, attached bool) (*dmabuf.Buffer, error) {
	acquire, release := ss.acquire, ss.release
	switch {
	case (acquire != nil || release != nil) && (!attached || buffer == nil):
		return nil, ss.fail(sls.WpLinuxDrmSyncobjSurfaceV1ErrorNoBuffer, "acquire or release point without a buffer")
	case !attached || buffer == nil:
		return nil, nil
	case acquire == nil:
		return nil, ss.fail(sls.WpLinuxDrmSyncobjSurfaceV1ErrorNoAcquirePoint, "buffer without an acquire point")
	case release == nil:
		return nil, ss.fail(sls.WpLinuxDrmSyncobjSurfaceV1ErrorNoReleasePoint, "buffer without a release point")
	case acquire.t == release.t && acquire.value >= release.value:
		return nil, ss.fail(sls.WpLinuxDrmSyncobjSurfaceV1ErrorConflictingPoints, "acquire point %d is not before release point %d on the same timeline", acquire.value, release.value)
	}
	b, ok := buffer.(*dmabuf.Buffer)
	if !ok {
		return nil, ss.fail(sls.WpLinuxDrmSyncobjSurfaceV1ErrorUnsupportedBuffer, "explicit sync needs a dmabuf, not %T", buffer)
	}
	return b, nil
}

func (ss *syncSurface) commit(u *surface.Update) error {
	buffer, attached := ss.s.PendingBuffer()
	var explicit *dmabuf.Buffer
	if ss.res != nil {
		var err error
		if explicit, err = ss.check(buffer, attached); err != nil {
			return err
		}
	}
	if !attached {
		return nil
	}
	acquire, release := ss.acquire, ss.release
	ss.acquire, ss.release = nil, nil
	ss.latched = append(ss.latched, latch{release: release, buffer: explicit})
	u.OnApply(ss.applied)
	if acquire == nil {
		return nil
	}
	return ss.wait(acquire, u.Hold())
}

func (ss *syncSurface) wait(acquire *point, hold *surface.Hold) error {
	efd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return errors.Join(fmt.Errorf("syncobj: eventfd: %w", err), acquire.drop())
	}
	if err := ss.m.dev.EventFD(acquire.t.handle, acquire.value, efd, false); err != nil {
		return errors.Join(err, unix.Close(efd), acquire.drop())
	}
	w := &wait{efd: efd, acquire: acquire, hold: hold}
	if w.src, err = ss.m.loop.AddFD(efd, loop.Readable, func(loop.Events) error {
		ss.waits = slices.DeleteFunc(ss.waits, func(x *wait) bool { return x == w })
		return errors.Join(w.cancel(), w.hold.Release())
	}); err != nil {
		return errors.Join(err, unix.Close(efd), acquire.drop(), w.hold.Release())
	}
	ss.waits = append(ss.waits, w)
	return nil
}

func (ss *syncSurface) applied() error {
	err := ss.retire(ss.current)
	ss.current = ss.latched[0]
	ss.latched[0] = latch{}
	ss.latched = ss.latched[1:]
	if ss.res == nil && ss.current.release == nil && len(ss.latched) == 0 {
		ss.forget()
	}
	return err
}

func (ss *syncSurface) objectGone() error {
	ss.res = nil
	err := errors.Join(ss.acquire.drop(), ss.release.drop())
	ss.acquire, ss.release = nil, nil
	if ss.current.release == nil && len(ss.latched) == 0 {
		ss.forget()
	}
	return err
}

func (ss *syncSurface) surfaceGone() error {
	errs := []error{ss.retire(ss.current)}
	ss.current = latch{}
	for _, l := range ss.latched {
		errs = append(errs, l.release.signal())
	}
	ss.latched = nil
	for _, w := range ss.waits {
		errs = append(errs, w.cancel())
	}
	ss.waits = nil
	return errors.Join(append(errs, ss.objectGone())...)
}

func (ss *syncSurface) forget() {
	ss.s.RemoveHooks(ss.hooks)
	if ss.m.surfaces[ss.s] == ss {
		delete(ss.m.surfaces, ss.s)
	}
}
