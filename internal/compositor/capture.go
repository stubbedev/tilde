package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/dmabuf"
	"github.com/stubbedev/tilde/internal/fourcc"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/region"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/shm"
	"github.com/stubbedev/tilde/internal/surface"
)

var (
	captureFormats       = []render.Format{render.XRGB8888, render.ARGB8888}
	dmabufCaptureFormats = []fourcc.Format{fourcc.XRGB8888, fourcc.ARGB8888}
)

type captureRenderer interface {
	ImportTarget(b gpu.DMABUF) (render.ReadTarget, error)
	RenderableModifiers(format fourcc.Format) ([]fourcc.Modifier, error)
	MainDevice() (uint64, error)
	Finish() error
}

var opaque = render.Color{A: 0xff}

type captureTarget interface {
	fill(c *Compositor, k capture) error
}

type shmCapture struct{ b *shm.Buffer }

func (t shmCapture) fill(c *Compositor, k capture) (err error) {
	target, err := c.renderer.NewTarget(k.size)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, target.Destroy()) }()
	if err := paint(c.renderer, target, k); err != nil {
		return err
	}
	return t.b.Write(func(data []byte, stride int) error { return target.Read(data, stride) })
}

type dmabufCapture struct {
	b *dmabuf.Buffer
	r captureRenderer
}

func (t dmabufCapture) fill(c *Compositor, k capture) (err error) {
	target, err := t.r.ImportTarget(t.b.DMABUF())
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, target.Destroy()) }()
	if err := paint(c.renderer, target, k); err != nil {
		return err
	}
	return t.r.Finish()
}

func paint(r render.Renderer, target render.Target, k capture) error {
	p, err := r.Begin(target, render.Region{image.Rectangle{Max: k.size}})
	if err != nil {
		return err
	}
	p.Clear(k.clear)
	return errors.Join(k.draw(p), p.End())
}

type capture struct {
	output *output.Output
	size   image.Point
	clear  render.Color
	draw   func(render.Pass) error
	target captureTarget
	waits  func(damage *region.Set) bool
	done   func(at time.Duration, err error) error
}

type frameState uint8

const (
	frameIdle frameState = iota
	frameCapturing
	frameDone
	frameGone
)

type frameLife struct {
	state   frameState
	pending *capture
}

func (l *frameLife) start(k *capture) {
	l.state, l.pending = frameCapturing, k
}

func (l *frameLife) finish() (alive bool) {
	l.pending = nil
	if l.state == frameGone {
		return false
	}
	l.state = frameDone
	return true
}

func (l *frameLife) end(c *Compositor) {
	l.state = frameGone
	if l.pending != nil {
		c.cancelCapture(l.pending)
		l.pending = nil
	}
}

func (c *Compositor) queueCapture(k *capture, waitForDamage bool) error {
	if !c.outputLive(k.output) {
		return k.done(0, errOutputGone(k.output))
	}
	if c.sceneOf(k.output).drawn && !waitForDamage {
		return c.runCapture(k)
	}
	c.captures[k.output] = append(c.captures[k.output], k)
	return c.schedule(k.output)
}

func (c *Compositor) runCaptures(o *output.Output) error {
	damage := &c.sceneOf(o).damage
	for _, s := range c.captureSessions {
		s.noteDamage(o, damage)
	}
	return c.flushCaptures(o, damage)
}

func (c *Compositor) wakeCaptures(o *output.Output) error {
	if !c.sceneOf(o).drawn {
		return nil
	}
	return c.flushCaptures(o, &region.Set{})
}

func (c *Compositor) flushCaptures(o *output.Output, damage *region.Set) error {
	pending := c.captures[o]
	if len(pending) == 0 {
		return nil
	}
	delete(c.captures, o)
	errs := make([]error, 0, len(pending))
	for _, k := range pending {
		if k.waits != nil && k.waits(damage) {
			c.captures[o] = append(c.captures[o], k)
			continue
		}
		errs = append(errs, c.runCapture(k))
	}
	return errors.Join(errs...)
}

func addDamage(dst, frame *region.Set, area image.Rectangle) {
	for _, r := range frame.Rects() {
		if r = r.Intersect(area); !r.Empty() {
			dst.Add(r.Sub(area.Min))
		}
	}
}

func (c *Compositor) dropCaptures(o *output.Output) error {
	pending := c.captures[o]
	delete(c.captures, o)
	errs := make([]error, 0, len(pending))
	for _, k := range pending {
		errs = append(errs, k.done(0, errOutputGone(o)))
	}
	return errors.Join(errs...)
}

var errCaptureGone = errors.New("the output went away before the capture")

func errOutputGone(o *output.Output) error {
	return fmt.Errorf("compositor: %s: %w", o.Backend().Name(), errCaptureGone)
}

func (c *Compositor) cancelCapture(k *capture) {
	pending := slices.DeleteFunc(c.captures[k.output], func(x *capture) bool { return x == k })
	if len(pending) == 0 {
		delete(c.captures, k.output)
		return
	}
	c.captures[k.output] = pending
}

func (c *Compositor) runCapture(k *capture) error {
	err := k.target.fill(c, *k)
	return k.done(backend.Now(), err)
}

func (c *Compositor) outputDraw(o *output.Output, region image.Rectangle, cursor bool) func(render.Pass) error {
	return func(p render.Pass) error {
		last := c.sceneOf(o).prev
		(&frame{ops: last.ops[:last.cursorFrom]}).replayFrom(p, region.Min)
		if !cursor {
			return nil
		}
		ops := frame{ops: last.ops[last.cursorFrom:]}
		if len(ops.ops) == 0 {
			if _, err := c.drawCursor(&ops, o, nil); err != nil {
				return err
			}
		}
		ops.replayFrom(p, region.Min)
		return nil
	}
}

func (c *Compositor) windowRegion(w *window) image.Rectangle {
	return w.output.Scale().Rect(w.rect.Sub(w.output.Position()))
}

func (c *Compositor) windowDraw(w *window, cursor bool) func(render.Pass) error {
	return func(p render.Pass) error {
		if w.output == nil {
			return nil
		}
		var f frame
		c.drawTree(&f, w.output, w.top.Surface(), c.windowOrigin(w), nil)
		c.drawPopups(&f, w.output, c.popups(w), nil)
		if cursor {
			if _, err := c.drawCursor(&f, w.output, nil); err != nil {
				return err
			}
		}
		f.replayFrom(p, c.windowRegion(w).Min)
		return nil
	}
}

func (c *Compositor) cursorBounds(o *output.Output) (image.Rectangle, error) {
	var f frame
	if _, err := c.drawCursor(&f, o, nil); err != nil {
		return image.Rectangle{}, err
	}
	return f.bounds(), nil
}

func (c *Compositor) pointerDraw(o *output.Output) func(render.Pass) error {
	return func(p render.Pass) error {
		var f frame
		if _, err := c.drawCursor(&f, o, nil); err != nil {
			return err
		}
		f.replayFrom(p, f.bounds().Min)
		return nil
	}
}

func (c *Compositor) inWindow(w *window, s *surface.Surface) bool {
	if inTree(w.top.Surface(), s) {
		return true
	}
	for _, pp := range c.popups(w) {
		if inTree(pp.popup.Surface(), s) {
			return true
		}
	}
	return false
}

func (c *Compositor) captureRegion(o *output.Output, logical image.Rectangle) image.Rectangle {
	bounds := image.Rectangle{Max: o.Backend().State().Mode.Size}
	if logical == (image.Rectangle{}) {
		return bounds
	}
	return o.Scale().Rect(logical).Intersect(bounds)
}

var errConstraints = errors.New("the buffer breaks the capture's constraints")

func shmTarget(b *shm.Buffer, size image.Point, formats []render.Format) (captureTarget, error) {
	switch {
	case b.Size() != size:
		return nil, fmt.Errorf("%w: the buffer is %v, the capture is %v", errConstraints, b.Size(), size)
	case !slices.Contains(formats, b.Format()):
		return nil, fmt.Errorf("%w: format %d cannot hold a capture", errConstraints, b.Format())
	}
	return shmCapture{b}, nil
}

func (c *Compositor) dmabufTarget(b *dmabuf.Buffer, size image.Point) (captureTarget, error) {
	r, ok := c.renderer.(captureRenderer)
	d := b.DMABUF()
	switch {
	case !ok:
		return nil, fmt.Errorf("%w: this renderer cannot draw into dmabufs", errConstraints)
	case d.Size != size:
		return nil, fmt.Errorf("%w: the buffer is %v, the capture is %v", errConstraints, d.Size, size)
	case !slices.Contains(dmabufCaptureFormats, d.Format):
		return nil, fmt.Errorf("%w: format %v cannot hold a capture", errConstraints, d.Format)
	}
	mods, err := r.RenderableModifiers(d.Format)
	if err != nil {
		return nil, fmt.Errorf("modifiers the renderer draws %v into: %w", d.Format, err)
	}
	if !slices.Contains(mods, d.Modifier) {
		return nil, fmt.Errorf("%w: the renderer cannot draw into format %v with modifier %v", errConstraints, d.Format, d.Modifier)
	}
	return dmabufCapture{b, r}, nil
}
