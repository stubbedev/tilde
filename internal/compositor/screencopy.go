package compositor

import (
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/stubbedev/tilde/internal/output"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	swsc "github.com/stubbedev/tilde/internal/protocol/server/wlrscreencopy"
	"github.com/stubbedev/tilde/internal/region"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/shm"
	"github.com/stubbedev/tilde/internal/wire"
)

const screencopyVersion = 3

func (c *Compositor) addScreencopy(d *server.Display) error {
	_, err := d.AddGlobal(swsc.ZwlrScreencopyManagerV1Interface, screencopyVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := swsc.NewZwlrScreencopyManagerV1(cl, id, version, screencopyManager{c, cl})
		return err
	})
	return err
}

type screencopyManager struct {
	c  *Compositor
	cl *server.Client
}

func (screencopyManager) Destroy(*swsc.ZwlrScreencopyManagerV1) error {
	return nil
}

func (m screencopyManager) CaptureOutput(_ *swsc.ZwlrScreencopyManagerV1, res *swsc.ZwlrScreencopyFrameV1, overlayCursor int32, wl *sw.WlOutput) (swsc.ZwlrScreencopyFrameV1Handler, error) {
	return m.frame(res, overlayCursor != 0, wl, image.Rectangle{})
}

func (m screencopyManager) CaptureOutputRegion(_ *swsc.ZwlrScreencopyManagerV1, res *swsc.ZwlrScreencopyFrameV1, overlayCursor int32, wl *sw.WlOutput, x, y, w, h int32) (swsc.ZwlrScreencopyFrameV1Handler, error) {
	r := image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h))
	if r.Empty() {
		r = image.Rect(int(x), int(y), int(x)+1, int(y)+1)
	}
	return m.frame(res, overlayCursor != 0, wl, r)
}

func (m screencopyManager) frame(res *swsc.ZwlrScreencopyFrameV1, cursor bool, wl *sw.WlOutput, logical image.Rectangle) (swsc.ZwlrScreencopyFrameV1Handler, error) {
	f := &screencopyFrame{c: m.c, cl: m.cl, res: res, cursor: cursor}
	server.OnDestroy(res, func() error {
		f.end(m.c)
		return nil
	})
	o := m.c.layout.Owner(wl)
	if !m.c.outputLive(o) {
		f.finish()
		return f, res.SendFailed()
	}
	if logical != (image.Rectangle{}) {
		logical = logical.Sub(o.Position())
	}
	f.output, f.region = o, m.c.captureRegion(o, logical)
	if f.region.Empty() {
		f.finish()
		return f, res.SendFailed()
	}
	size := f.region.Size()
	if err := res.SendBuffer(sw.WlShmFormatXrgb8888, uint32(size.X), uint32(size.Y), uint32(size.X*4)); err != nil { //nolint:gosec // capture sizes are positive output sizes
		return nil, err
	}
	if res.Base().Version() >= 3 {
		if err := res.SendBufferDone(); err != nil {
			return nil, err
		}
	}
	return f, nil
}

type screencopyFrame struct {
	c      *Compositor
	cl     *server.Client
	res    *swsc.ZwlrScreencopyFrameV1
	output *output.Output
	region image.Rectangle
	cursor bool
	frameLife
}

func (f *screencopyFrame) Copy(obj *swsc.ZwlrScreencopyFrameV1, wl *sw.WlBuffer) error {
	return f.copy(obj, wl, false)
}

func (f *screencopyFrame) CopyWithDamage(obj *swsc.ZwlrScreencopyFrameV1, wl *sw.WlBuffer) error {
	return f.copy(obj, wl, true)
}

func (f *screencopyFrame) copy(obj *swsc.ZwlrScreencopyFrameV1, wl *sw.WlBuffer, damage bool) error {
	if f.state != frameIdle {
		return obj.ProtocolError(swsc.ZwlrScreencopyFrameV1ErrorAlreadyUsed, "the frame was already copied")
	}
	b, ok := wl.Handler().(*shm.Buffer)
	if !ok {
		return obj.ProtocolError(swsc.ZwlrScreencopyFrameV1ErrorInvalidBuffer, "not a shared-memory buffer")
	}
	target, err := shmTarget(b, f.region.Size(), []render.Format{render.XRGB8888}, nil)
	if err != nil {
		return obj.ProtocolError(swsc.ZwlrScreencopyFrameV1ErrorInvalidBuffer, "%v", err)
	}
	var damaged region.Set
	k := &capture{output: f.output, size: f.region.Size(), clear: opaque, draw: f.c.outputDraw(f.output, f.region, f.cursor), target: target}
	f.start(k)
	if damage {
		k.waits = func(d *region.Set) bool {
			addDamage(&damaged, d, f.region)
			return damaged.Empty()
		}
	}
	k.done = func(at time.Duration, err error) error {
		if err != nil {
			if !errors.Is(err, errCaptureGone) {
				f.c.report(fmt.Errorf("compositor: screencopy of %s: %w", f.output.Backend().Name(), err))
			}
			if !f.finish() {
				return nil
			}
			return f.res.SendFailed()
		}
		if !f.finish() {
			return nil
		}
		errs := []error{f.res.SendFlags(0)}
		for _, r := range damaged.Rects() {
			errs = append(errs, f.res.SendDamage(uint32(r.Min.X), uint32(r.Min.Y), uint32(r.Dx()), uint32(r.Dy()))) //nolint:gosec // damage lies inside the capture
		}
		ts := wire.TimespecOf(at)
		return errors.Join(append(errs, f.res.SendReady(ts.SecHi, ts.SecLo, ts.Nsec))...)
	}
	return f.c.queueCapture(k, damage)
}

func (*screencopyFrame) Destroy(*swsc.ZwlrScreencopyFrameV1) error {
	return nil
}
