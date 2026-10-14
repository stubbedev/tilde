package compositor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/tilde/internal/dmabuf"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/output"
	sefl "github.com/stubbedev/tilde/internal/protocol/server/extforeigntoplevellist"
	seis "github.com/stubbedev/tilde/internal/protocol/server/extimagecapturesource"
	seic "github.com/stubbedev/tilde/internal/protocol/server/extimagecopycapture"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/region"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shm"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	captureSourceVersion = 1
	imageCaptureVersion  = 1
)

type captureSource struct {
	output *output.Output
	window *window
}

func (c *Compositor) addImageCapture(d *server.Display) error {
	if _, err := d.AddGlobal(seis.ExtOutputImageCaptureSourceManagerV1Interface, captureSourceVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := seis.NewExtOutputImageCaptureSourceManagerV1(cl, id, version, outputSources{c})
		return err
	}); err != nil {
		return err
	}
	if _, err := d.AddGlobal(seis.ExtForeignToplevelImageCaptureSourceManagerV1Interface, captureSourceVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := seis.NewExtForeignToplevelImageCaptureSourceManagerV1(cl, id, version, toplevelSources{c})
		return err
	}); err != nil {
		return err
	}
	offer, err := captureOffer(c.renderer)
	if err != nil {
		return err
	}
	_, err = d.AddGlobal(seic.ExtImageCopyCaptureManagerV1Interface, imageCaptureVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := seic.NewExtImageCopyCaptureManagerV1(cl, id, version, imageCaptures{c, cl, offer})
		return err
	})
	return err
}

type dmabufOffer struct {
	device  []byte
	formats []dmabufFormat
}

type dmabufFormat struct {
	format    uint32
	modifiers []byte
}

func captureOffer(r render.Renderer) (*dmabufOffer, error) {
	cr, ok := r.(captureRenderer)
	if !ok {
		return &dmabufOffer{}, nil
	}
	dev, err := cr.MainDevice()
	if errors.Is(err, gpu.ErrUnsupported) {
		return &dmabufOffer{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("image capture: %w", err)
	}
	offer := &dmabufOffer{device: binary.NativeEndian.AppendUint64(nil, dev)}
	for _, format := range []uint32{gpu.FormatXRGB8888, gpu.FormatARGB8888} {
		mods, err := cr.RenderableModifiers(format)
		if err != nil {
			return nil, fmt.Errorf("image capture: modifiers of format %#x: %w", format, err)
		}
		if len(mods) == 0 {
			continue
		}
		list := make([]byte, 0, 8*len(mods))
		for _, m := range mods {
			list = binary.NativeEndian.AppendUint64(list, m)
		}
		offer.formats = append(offer.formats, dmabufFormat{format, list})
	}
	return offer, nil
}

type outputSources struct{ c *Compositor }

func (outputSources) Destroy(*seis.ExtOutputImageCaptureSourceManagerV1) error { return nil }

func (m outputSources) CreateSource(_ *seis.ExtOutputImageCaptureSourceManagerV1, _ *seis.ExtImageCaptureSourceV1, wl *sw.WlOutput) (seis.ExtImageCaptureSourceV1Handler, error) {
	return &captureSource{output: m.c.layout.Owner(wl)}, nil
}

type toplevelSources struct{ c *Compositor }

func (toplevelSources) Destroy(*seis.ExtForeignToplevelImageCaptureSourceManagerV1) error {
	return nil
}

func (m toplevelSources) CreateSource(_ *seis.ExtForeignToplevelImageCaptureSourceManagerV1, _ *seis.ExtImageCaptureSourceV1, handle *sefl.ExtForeignToplevelHandleV1) (seis.ExtImageCaptureSourceV1Handler, error) {
	for _, l := range m.c.extLists {
		for w, h := range l.handles {
			if h.resource == handle {
				return &captureSource{window: w}, nil
			}
		}
	}
	return &captureSource{}, nil
}

func (*captureSource) Destroy(*seis.ExtImageCaptureSourceV1) error {
	return nil
}

type imageCaptures struct {
	c     *Compositor
	cl    *server.Client
	offer *dmabufOffer
}

func (imageCaptures) Destroy(*seic.ExtImageCopyCaptureManagerV1) error { return nil }

func (m imageCaptures) CreateSession(obj *seic.ExtImageCopyCaptureManagerV1, res *seic.ExtImageCopyCaptureSessionV1, source *seis.ExtImageCaptureSourceV1, options seic.ExtImageCopyCaptureManagerV1Options) (seic.ExtImageCopyCaptureSessionV1Handler, error) {
	if options&^seic.ExtImageCopyCaptureManagerV1OptionsPaintCursors != 0 {
		return nil, obj.ProtocolError(seic.ExtImageCopyCaptureManagerV1ErrorInvalidOption, fmt.Sprintf("options %#x", options))
	}
	src, ok := source.Handler().(*captureSource)
	if !ok {
		return nil, fmt.Errorf("compositor: capture source@%d has handler %T", source.Base().ID(), source.Handler())
	}
	s := &captureSession{c: m.c, cl: m.cl, res: res, offer: m.offer, source: *src, cursor: options&seic.ExtImageCopyCaptureManagerV1OptionsPaintCursors != 0}
	m.c.captureSessions = append(m.c.captureSessions, s)
	m.cl.OnDestroy(res, func() error {
		m.c.captureSessions = slices.DeleteFunc(m.c.captureSessions, func(x *captureSession) bool { return x == s })
		return nil
	})
	return s, s.sync()
}

func (m imageCaptures) CreatePointerCursorSession(_ *seic.ExtImageCopyCaptureManagerV1, _ *seic.ExtImageCopyCaptureCursorSessionV1, _ *seis.ExtImageCaptureSourceV1, _ *sw.WlPointer) (seic.ExtImageCopyCaptureCursorSessionV1Handler, error) {
	return cursorSessions{m.cl}, nil
}

type cursorSessions struct{ cl *server.Client }

func (cursorSessions) Destroy(*seic.ExtImageCopyCaptureCursorSessionV1) error { return nil }

func (r cursorSessions) GetCaptureSession(_ *seic.ExtImageCopyCaptureCursorSessionV1, res *seic.ExtImageCopyCaptureSessionV1) (seic.ExtImageCopyCaptureSessionV1Handler, error) {
	s := &captureSession{cl: r.cl, res: res, stopped: true}
	return s, r.cl.Sent(res.SendStopped())
}

type captureSession struct {
	c        *Compositor
	cl       *server.Client
	res      *seic.ExtImageCopyCaptureSessionV1
	offer    *dmabufOffer
	source   captureSource
	cursor   bool
	size     image.Point
	frame    *captureFrame
	damage   region.Set
	captured bool
	stopped  bool
}

func (s *captureSession) target() (*output.Output, image.Rectangle, bool) {
	switch {
	case s.source.output != nil:
		o := s.source.output
		if s.c.output(o.Backend()) != o {
			return nil, image.Rectangle{}, false
		}
		return o, s.c.captureRegion(o, image.Rectangle{}), true
	case s.source.window != nil:
		w := s.source.window
		if !slices.Contains(s.c.windows, w) || w.output == nil {
			return nil, image.Rectangle{}, false
		}
		return w.output, s.c.windowRegion(w), true
	}
	return nil, image.Rectangle{}, false
}

func (s *captureSession) sync() error {
	if s.stopped {
		return nil
	}
	_, area, ok := s.target()
	if !ok {
		s.stopped = true
		var err error
		if f := s.frame; f != nil && f.capturing {
			err = f.fail(seic.ExtImageCopyCaptureFrameV1FailureReasonStopped)
		}
		return errors.Join(err, s.cl.Sent(s.res.SendStopped()))
	}
	if area.Size() == s.size {
		return nil
	}
	s.size, s.captured = area.Size(), false
	s.damage.Reset()
	return s.cl.Sent(s.constraints())
}

func (s *captureSession) noteDamage(o *output.Output, frame *region.Set) {
	if s.stopped {
		return
	}
	if to, area, ok := s.target(); ok && to == o {
		addDamage(&s.damage, frame, area)
	}
}

func (s *captureSession) constraints() error {
	res := s.res
	errs := []error{res.SendBufferSize(uint32(s.size.X), uint32(s.size.Y))} //nolint:gosec // capture sizes are positive
	for _, f := range []sw.WlShmFormat{sw.WlShmFormatXrgb8888, sw.WlShmFormatArgb8888} {
		errs = append(errs, res.SendShmFormat(f))
	}
	if s.offer.device != nil {
		errs = append(errs, res.SendDmabufDevice(s.offer.device))
		for _, f := range s.offer.formats {
			errs = append(errs, res.SendDmabufFormat(f.format, f.modifiers))
		}
	}
	return errors.Join(append(errs, res.SendDone())...)
}

func (s *captureSession) CreateFrame(obj *seic.ExtImageCopyCaptureSessionV1, res *seic.ExtImageCopyCaptureFrameV1) (seic.ExtImageCopyCaptureFrameV1Handler, error) {
	if s.frame != nil {
		return nil, obj.ProtocolError(seic.ExtImageCopyCaptureSessionV1ErrorDuplicateFrame, "the session already has a frame")
	}
	f := &captureFrame{s: s, res: res}
	s.frame = f
	s.cl.OnDestroy(res, func() error {
		f.dead = true
		if s.frame == f {
			s.frame = nil
		}
		return nil
	})
	return f, nil
}

func (*captureSession) Destroy(*seic.ExtImageCopyCaptureSessionV1) error {
	return nil
}

type captureFrame struct {
	s         *captureSession
	res       *seic.ExtImageCopyCaptureFrameV1
	buffer    *sw.WlBuffer
	capturing bool
	done      bool
	dead      bool
}

func (f *captureFrame) AttachBuffer(_ *seic.ExtImageCopyCaptureFrameV1, b *sw.WlBuffer) error {
	f.buffer = b
	return nil
}

func (f *captureFrame) DamageBuffer(obj *seic.ExtImageCopyCaptureFrameV1, x, y, w, h int32) error {
	if x < 0 || y < 0 || w <= 0 || h <= 0 {
		return obj.ProtocolError(seic.ExtImageCopyCaptureFrameV1ErrorInvalidBufferDamage, fmt.Sprintf("damage %d,%d %dx%d", x, y, w, h))
	}
	return nil
}

func (f *captureFrame) fail(reason seic.ExtImageCopyCaptureFrameV1FailureReason) error {
	f.capturing, f.done = false, true
	if f.dead {
		return nil
	}
	return f.s.cl.Sent(f.res.SendFailed(reason))
}

func (f *captureFrame) Capture(obj *seic.ExtImageCopyCaptureFrameV1) error {
	s := f.s
	switch {
	case f.capturing || f.done:
		return obj.ProtocolError(seic.ExtImageCopyCaptureFrameV1ErrorAlreadyCaptured, "the frame was already captured")
	case f.buffer == nil:
		return obj.ProtocolError(seic.ExtImageCopyCaptureFrameV1ErrorNoBuffer, "capture without a buffer")
	case s.stopped:
		return f.fail(seic.ExtImageCopyCaptureFrameV1FailureReasonStopped)
	}
	o, area, ok := s.target()
	if !ok {
		return f.fail(seic.ExtImageCopyCaptureFrameV1FailureReasonStopped)
	}
	target, err := f.target(area.Size())
	if err != nil {
		return f.fail(seic.ExtImageCopyCaptureFrameV1FailureReasonBufferConstraints)
	}
	draw := s.c.outputDraw(o, area, s.cursor)
	if w := s.source.window; w != nil {
		draw = s.c.windowDraw(w, s.cursor)
	}
	f.capturing = true
	waits := func(*region.Set) bool { return s.captured && s.damage.Empty() }
	return s.c.queueCapture(capture{output: o, size: area.Size(), draw: draw, target: target, waits: waits, done: func(at time.Duration, err error) error {
		if err != nil {
			s.c.report(fmt.Errorf("compositor: image capture: %w", err))
			return f.fail(seic.ExtImageCopyCaptureFrameV1FailureReasonUnknown)
		}
		f.capturing, f.done = false, true
		err = f.ready(at, area.Size())
		s.captured = true
		s.damage.Reset()
		return err
	}}, waits(nil))
}

func (f *captureFrame) ready(at time.Duration, size image.Point) error {
	if f.dead {
		return nil
	}
	damage := []image.Rectangle{{Max: size}}
	if f.s.captured {
		damage = f.s.damage.Rects()
	}
	errs := []error{f.res.SendTransform(sw.WlOutputTransformNormal)}
	for _, r := range damage {
		errs = append(errs, f.res.SendDamage(int32(r.Min.X), int32(r.Min.Y), int32(r.Dx()), int32(r.Dy()))) //nolint:gosec // damage lies inside the capture
	}
	sec := uint64(max(at, 0) / time.Second)
	errs = append(errs,
		f.res.SendPresentationTime(uint32(sec>>32), uint32(sec), uint32(at%time.Second)), //nolint:gosec // split as the protocol asks
		f.res.SendReady(),
	)
	return f.s.cl.Sent(errors.Join(errs...))
}

func (f *captureFrame) target(size image.Point) (captureTarget, error) {
	switch b := f.buffer.Handler().(type) {
	case *shm.Buffer:
		return shmTarget(b, size, captureFormats)
	case *dmabuf.Buffer:
		return f.s.c.dmabufTarget(b, size)
	}
	return nil, errors.New("neither a shared-memory nor a dmabuf buffer")
}

func (*captureFrame) Destroy(*seic.ExtImageCopyCaptureFrameV1) error {
	return nil
}

func (c *Compositor) syncCaptureSessions() error {
	var errs []error
	for _, s := range c.captureSessions {
		errs = append(errs, s.sync())
	}
	return errors.Join(errs...)
}
