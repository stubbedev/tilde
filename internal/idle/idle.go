// Package idle implements ext_idle_notifier_v1 and zwp_idle_inhibit_manager_v1:
// idle timers that input resets, and inhibitors that hold them off while
// their surface is on screen.
package idle

import (
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/loop"
	sei "github.com/stubbedev/lys-wm/internal/protocol/server/extidlenotify"
	sii "github.com/stubbedev/lys-wm/internal/protocol/server/idleinhibit"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const (
	notifierVersion = 2
	inhibitVersion  = 1
)

type Manager struct {
	loop          *loop.Loop
	now           func() time.Duration
	notifications []*notification
	inhibitors    []*Inhibitor
	inhibited     bool
	last          time.Duration
	changed       func() error
}

type notification struct {
	m           *Manager
	client      *server.Client
	resource    *sei.ExtIdleNotificationV1
	timeout     time.Duration
	obeyInhibit bool
	timer       *loop.Timer
	idle        bool
	since       time.Duration
}

type Inhibitor struct {
	m       *Manager
	surface *surface.Surface
	hooks   *surface.Hooks
}

func New(d *server.Display, l *loop.Loop, now func() time.Duration, inhibitorsChanged func() error) (*Manager, error) {
	m := &Manager{loop: l, now: now, last: now(), changed: inhibitorsChanged}
	if _, err := d.AddGlobal(sei.ExtIdleNotifierV1Interface, notifierVersion, m.bindNotifier); err != nil {
		return nil, fmt.Errorf("idle: %w", err)
	}
	if _, err := d.AddGlobal(sii.ZwpIdleInhibitManagerV1Interface, inhibitVersion, m.bindInhibit); err != nil {
		return nil, fmt.Errorf("idle: %w", err)
	}
	return m, nil
}

func (m *Manager) Activity() error {
	m.last = m.now()
	var errs []error
	for _, n := range m.notifications {
		errs = append(errs, n.reset(m.last))
	}
	return errors.Join(errs...)
}

func (m *Manager) Inhibitors() iter.Seq[*Inhibitor] {
	return slices.Values(m.inhibitors)
}

func (i *Inhibitor) Surface() *surface.Surface {
	return i.surface
}

func (m *Manager) SetInhibited(on bool) error {
	if on == m.inhibited {
		return nil
	}
	m.inhibited = on
	now := m.now()
	var errs []error
	for _, n := range m.notifications {
		if !n.obeyInhibit {
			continue
		}
		errs = append(errs, n.reset(now))
		if on {
			errs = append(errs, n.timer.Disarm())
		} else {
			errs = append(errs, n.arm())
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) bindNotifier(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sei.NewExtIdleNotifierV1(c, id, version, notifier{m, c})
	return err
}

type notifier struct {
	m *Manager
	c *server.Client
}

func (notifier) Destroy(*sei.ExtIdleNotifierV1) error {
	return nil
}

func (r notifier) GetIdleNotification(_ *sei.ExtIdleNotifierV1, res *sei.ExtIdleNotificationV1, timeout uint32, _ *sw.WlSeat) (sei.ExtIdleNotificationV1Handler, error) {
	return r.m.add(r.c, res, timeout, true)
}

func (r notifier) GetInputIdleNotification(_ *sei.ExtIdleNotifierV1, res *sei.ExtIdleNotificationV1, timeout uint32, _ *sw.WlSeat) (sei.ExtIdleNotificationV1Handler, error) {
	return r.m.add(r.c, res, timeout, false)
}

func (m *Manager) add(c *server.Client, res *sei.ExtIdleNotificationV1, timeout uint32, obeyInhibit bool) (*notification, error) {
	n := &notification{m: m, client: c, resource: res, timeout: time.Duration(timeout) * time.Millisecond, obeyInhibit: obeyInhibit}
	var err error
	if n.timer, err = m.loop.AddTimer(n.fire); err != nil {
		return nil, fmt.Errorf("idle: timer for a %v notification: %w", n.timeout, err)
	}
	m.notifications = append(m.notifications, n)
	server.OnDestroy(res, n.destroy)
	n.since = m.last
	if n.suspended() {
		return n, nil
	}
	return n, n.arm()
}

func (n *notification) suspended() bool {
	return n.obeyInhibit && n.m.inhibited
}

func (n *notification) reset(at time.Duration) error {
	n.since = at
	if !n.idle {
		return nil
	}
	n.idle = false
	err := n.resource.SendResumed()
	if n.suspended() {
		return err
	}
	return errors.Join(err, n.arm())
}

func (n *notification) arm() error {
	return n.timer.Arm(max(n.timeout-(n.m.now()-n.since), time.Nanosecond))
}

func (n *notification) fire() error {
	if n.idle || n.suspended() {
		return nil
	}
	if n.m.now()-n.since < n.timeout {
		return n.arm()
	}
	n.idle = true
	return n.resource.SendIdled()
}

func (*notification) Destroy(*sei.ExtIdleNotificationV1) error {
	return nil
}

func (n *notification) destroy() error {
	m := n.m
	m.notifications = slices.DeleteFunc(m.notifications, func(x *notification) bool { return x == n })
	return n.timer.Remove()
}

func (m *Manager) bindInhibit(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sii.NewZwpIdleInhibitManagerV1(c, id, version, inhibitManager{m, c})
	return err
}

type inhibitManager struct {
	m *Manager
	c *server.Client
}

func (inhibitManager) Destroy(*sii.ZwpIdleInhibitManagerV1) error {
	return nil
}

func (r inhibitManager) CreateInhibitor(_ *sii.ZwpIdleInhibitManagerV1, res *sii.ZwpIdleInhibitorV1, wl *sw.WlSurface) (sii.ZwpIdleInhibitorV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	i := &Inhibitor{m: r.m, surface: s}
	i.hooks = &surface.Hooks{Destroyed: i.drop}
	s.AddHooks(i.hooks)
	r.m.inhibitors = append(r.m.inhibitors, i)
	server.OnDestroy(res, i.drop)
	return i, r.m.changed()
}

func (i *Inhibitor) drop() error {
	m := i.m
	if !slices.Contains(m.inhibitors, i) {
		return nil
	}
	i.surface.RemoveHooks(i.hooks)
	m.inhibitors = slices.DeleteFunc(m.inhibitors, func(x *Inhibitor) bool { return x == i })
	return m.changed()
}

func (*Inhibitor) Destroy(*sii.ZwpIdleInhibitorV1) error {
	return nil
}
