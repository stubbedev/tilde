package compositor

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/shm"
)

func (c *Compositor) offLoop(work func() error, then func(error) error) {
	c.offLoopWork.Go(func() {
		err := work()
		if posted := c.loop.Post(func() error { return then(err) }); posted != nil && !errors.Is(posted, loop.ErrClosed) {
			panic(fmt.Sprintf("compositor: hand off-loop work back to the loop: %v", posted))
		}
	})
}

func (c *Compositor) asyncReader() (render.AsyncReader, bool) {
	r, ok := c.renderer.(render.AsyncReader)
	return r, ok && r.AsyncReads()
}

type captureScratch struct {
	target render.AsyncReadTarget
	read   *asyncRead
}

func (s *captureScratch) take(r render.AsyncReader, size image.Point) (render.AsyncReadTarget, error) {
	if s.read != nil {
		s.read.owner, s.read, s.target = nil, nil, nil
	}
	if s.target != nil && s.target.Size() == size {
		return s.target, nil
	}
	if err := s.drop(); err != nil {
		return nil, err
	}
	t, err := r.NewAsyncTarget(size)
	if err != nil {
		return nil, err
	}
	s.target = t
	return t, nil
}

func (s *captureScratch) drop() error {
	t := s.target
	s.target = nil
	if s.read != nil {
		s.read.owner, s.read = nil, nil
		return nil
	}
	if t == nil {
		return nil
	}
	return t.Destroy()
}

type asyncRead struct {
	c         *Compositor
	k         *capture
	target    render.AsyncReadTarget
	owner     *captureScratch
	pending   render.PendingRead
	dst       *shm.Mapping
	finish    func(error) error
	cancelled bool
	ended     bool
}

func (c *Compositor) readAsync(r render.AsyncReader, b *shm.Buffer, scratch *captureScratch, k *capture, finish func(error) error) error {
	oneShot := scratch == nil
	if oneShot {
		scratch = &captureScratch{}
	}
	target, err := scratch.take(r, k.size)
	if err != nil {
		return finish(err)
	}
	read := &asyncRead{c: c, k: k, target: target, finish: finish}
	if !oneShot {
		read.owner, scratch.read = scratch, read
	}
	if err := paint(c.renderer, target, k); err != nil {
		return read.end(err)
	}
	if read.pending, err = target.ReadAsync(); err != nil {
		return read.end(err)
	}
	if read.dst, err = b.MapForWriting(); err != nil {
		return read.end(err)
	}
	k.read = read
	c.reads[read] = struct{}{}
	c.offLoop(read.pending.Wait, read.waited)
	return nil
}

func (r *asyncRead) waited(err error) error {
	if err != nil || r.cancelled {
		return r.end(err)
	}
	pix, err := r.pending.Map()
	if err != nil {
		return r.end(err)
	}
	size, dst := r.k.size, r.dst
	r.dst = nil
	r.c.offLoop(func() error {
		err := dst.Write(func(data []byte, stride int) error {
			row := size.X * 4
			for y := range size.Y {
				copy(data[y*stride:y*stride+row], pix[y*row:])
			}
			return nil
		})
		return errors.Join(err, dst.Close())
	}, r.end)
	return nil
}

var errCaptureAbandoned = fmt.Errorf("compositor: the frame was destroyed while it was read back: %w", errCaptureGone)

func (r *asyncRead) end(err error) error {
	if r.ended {
		return nil
	}
	r.ended = true
	delete(r.c.reads, r)
	if r.pending != nil {
		err = errors.Join(err, r.pending.Release())
	}
	if r.dst != nil {
		err = errors.Join(err, r.dst.Close())
	}
	if r.owner == nil {
		err = errors.Join(err, r.target.Destroy())
	} else {
		r.owner.read = nil
	}
	if r.cancelled {
		if err != nil {
			r.c.report(fmt.Errorf("compositor: an abandoned capture: %w", err))
		}
		err = errCaptureAbandoned
	}
	return r.finish(err)
}

func (c *Compositor) endReads() error {
	c.offLoopWork.Wait()
	var errs []error
	for r := range c.reads {
		r.cancelled = true
		errs = append(errs, r.end(nil))
	}
	return errors.Join(errs...)
}
