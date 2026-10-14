// Package committiming implements wp_commit_timing_manager_v1: a content
// update can carry a target time, and it waits until the refresh that
// presents it would not show it before that time.
package committiming

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/stubbedev/tilde/internal/loop"
	sct "github.com/stubbedev/tilde/internal/protocol/server/committiming"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const version = 1

type Clock struct {
	Now      func() time.Duration
	NextFlip func(*surface.Surface) (time.Duration, bool)
}

type Manager struct {
	loop   *loop.Loop
	clock  Clock
	timers []*timer
}

func New(d *server.Display, l *loop.Loop, clock Clock) (*Manager, error) {
	m := &Manager{loop: l, clock: clock}
	if _, err := d.AddGlobal(sct.WpCommitTimingManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Presented() error {
	var errs []error
	for _, t := range slices.Clone(m.timers) {
		if len(t.targets) > 0 {
			errs = append(errs, t.s.Retry())
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := sct.NewWpCommitTimingManagerV1(c, id, v, requests{m, c})
	return err
}

type requests struct {
	m *Manager
	c *server.Client
}

func (requests) Destroy(*sct.WpCommitTimingManagerV1) error { return nil }

func (r requests) GetTimer(obj *sct.WpCommitTimingManagerV1, res *sct.WpCommitTimerV1, wl *sw.WlSurface) (sct.WpCommitTimerV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(r.m.timers, func(t *timer) bool { return t.s == s })
	if i >= 0 && r.m.timers[i].live {
		return nil, obj.ProtocolError(sct.WpCommitTimingManagerV1ErrorCommitTimerExists, fmt.Sprintf("wl_surface@%d already has a commit timer", wl.Base().ID()))
	}
	var t *timer
	if i >= 0 {
		t = r.m.timers[i]
	} else {
		t = &timer{m: r.m, s: s}
		if t.clock, err = r.m.loop.AddTimer(t.fire); err != nil {
			return nil, fmt.Errorf("committiming: a timer for wl_surface@%d: %w", wl.Base().ID(), err)
		}
		t.hooks = &surface.Hooks{Commit: t.commit}
		s.AddHooks(t.hooks)
		r.m.timers = append(r.m.timers, t)
		r.c.OnDestroy(wl, t.surfaceGone)
	}
	t.live = true
	r.c.OnDestroy(res, func() error {
		t.live = false
		return nil
	})
	return t, nil
}

type timer struct {
	m       *Manager
	s       *surface.Surface
	hooks   *surface.Hooks
	clock   *loop.Timer
	live    bool
	gone    bool
	pending *time.Duration
	targets []time.Duration
}

func (*timer) Destroy(*sct.WpCommitTimerV1) error { return nil }

func target(secHi, secLo, nsec uint32) time.Duration {
	sec := uint64(secHi)<<32 | uint64(secLo)
	if sec > uint64(math.MaxInt64/int64(time.Second)) {
		return math.MaxInt64
	}
	return time.Duration(sec)*time.Second + time.Duration(nsec)
}

func (t *timer) SetTimestamp(obj *sct.WpCommitTimerV1, secHi, secLo, nsec uint32) error {
	switch {
	case t.gone:
		return obj.ProtocolError(sct.WpCommitTimerV1ErrorSurfaceDestroyed, "set_timestamp after the wl_surface was destroyed")
	case nsec >= uint32(time.Second):
		return obj.ProtocolError(sct.WpCommitTimerV1ErrorInvalidTimestamp, fmt.Sprintf("tv_nsec %d is not below one second", nsec))
	case t.pending != nil:
		return obj.ProtocolError(sct.WpCommitTimerV1ErrorTimestampExists, "the next commit already has a timestamp")
	}
	at := target(secHi, secLo, nsec)
	t.pending = &at
	return nil
}

func (t *timer) due(at time.Duration) bool {
	if t.m.clock.Now() >= at {
		return true
	}
	next, ok := t.m.clock.NextFlip(t.s)
	return ok && next >= at
}

func (t *timer) commit(u *surface.Update) error {
	if t.pending == nil {
		return nil
	}
	at := *t.pending
	t.pending = nil
	t.targets = append(t.targets, at)
	u.Gate(func() bool { return t.due(at) })
	u.OnApply(func() error {
		t.targets = t.targets[1:]
		return t.arm()
	})
	return t.arm()
}

func (t *timer) arm() error {
	now := t.m.clock.Now()
	var next time.Duration
	for _, at := range t.targets {
		if at > now && (next == 0 || at < next) {
			next = at
		}
	}
	if next == 0 {
		return t.clock.Disarm()
	}
	return t.clock.Arm(next - now)
}

func (t *timer) fire() error {
	return errors.Join(t.s.Retry(), t.arm())
}

func (t *timer) surfaceGone() error {
	t.gone = true
	t.targets = nil
	t.s.RemoveHooks(t.hooks)
	t.m.timers = slices.DeleteFunc(t.m.timers, func(x *timer) bool { return x == t })
	return t.clock.Remove()
}
