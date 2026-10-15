package backend

type FrameClock struct {
	idle      func(func() error)
	ready     func() bool
	frame     func() error
	requested bool
	queued    bool
}

func NewFrameClock(idle func(func() error), ready func() bool, frame func() error) *FrameClock {
	return &FrameClock{idle: idle, ready: ready, frame: frame}
}

func (f *FrameClock) Request() {
	f.requested = true
	f.Kick()
}

func (f *FrameClock) Cancel() {
	f.requested = false
}

func (f *FrameClock) Requested() bool {
	return f.requested
}

func (f *FrameClock) Kick() {
	if !f.requested || f.queued || !f.ready() {
		return
	}
	f.queued = true
	f.idle(f.fire)
}

func (f *FrameClock) fire() error {
	f.queued = false
	if !f.requested || !f.ready() {
		return nil
	}
	f.requested = false
	return f.frame()
}
