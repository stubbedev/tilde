package backend

import "time"

type Latch struct {
	Deadline func(now time.Duration) time.Duration
	Wake     func(after time.Duration) error
}

type FrameClock struct {
	idle      func(func() error)
	ready     func() bool
	frame     func() error
	latch     *Latch
	requested bool
	queued    bool
	waiting   bool
}

func NewFrameClock(idle func(func() error), ready func() bool, frame func() error, latch *Latch) *FrameClock {
	return &FrameClock{idle: idle, ready: ready, frame: frame, latch: latch}
}

func (f *FrameClock) Request() error {
	f.requested = true
	return f.Kick()
}

func (f *FrameClock) Cancel() {
	f.requested = false
}

func (f *FrameClock) Requested() bool {
	return f.requested
}

func (f *FrameClock) Kick() error {
	if !f.requested || f.queued || f.waiting || !f.ready() {
		return nil
	}
	if f.latch != nil {
		now := Now()
		if at := f.latch.Deadline(now); at > now {
			f.waiting = true
			return f.latch.Wake(at - now)
		}
	}
	f.queued = true
	f.idle(f.fire)
	return nil
}

func (f *FrameClock) Woke() error {
	f.waiting = false
	return f.Kick()
}

func (f *FrameClock) fire() error {
	f.queued = false
	if !f.requested || !f.ready() {
		return nil
	}
	f.requested = false
	return f.frame()
}

func LatchDeadline(last, now, interval time.Duration) time.Duration {
	if last == 0 || last > now {
		return now
	}
	next, _ := NextVblank(last, now, interval)
	return next - interval/2
}
