package shell

import (
	"fmt"
	"image"
	"slices"

	sel "github.com/stubbedev/tilde/internal/protocol/server/extsessionlock"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	lockVersion = 1
	lockRole    = "ext_session_lock_surface_v1"
)

type SessionLock struct {
	shell     *Shell
	client    *server.Client
	resource  *sel.ExtSessionLockV1
	surfaces  []*LockSurface
	confirmed bool
	finished  bool
	unlocked  bool
}

type LockSurface struct {
	lock       *SessionLock
	resource   *sel.ExtSessionLockSurfaceV1
	surface    *surface.Surface
	output     *sw.WlOutput
	configures configures[image.Point]
	size       image.Point
	sent       image.Point
	acked      bool
	mapped     bool
}

func (sh *Shell) bindLock(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sel.NewExtSessionLockManagerV1(c, id, version, lockManager{sh, c})
	return err
}

func (sh *Shell) Locked() bool {
	return sh.locked
}

func (sh *Shell) LockAlive() bool {
	return sh.lock != nil
}

func (sh *Shell) LockSurfaces() []*LockSurface {
	if sh.lock == nil {
		return nil
	}
	out := make([]*LockSurface, 0, len(sh.lock.surfaces))
	for _, s := range sh.lock.surfaces {
		if s.mapped {
			out = append(out, s)
		}
	}
	return out
}

func (sh *Shell) ConfirmLock() error {
	l := sh.lock
	if l == nil || l.confirmed {
		return nil
	}
	l.confirmed = true
	return l.client.Sent(l.resource.SendLocked())
}

func (sh *Shell) LockConfirmed() bool {
	return sh.lock == nil || sh.lock.confirmed
}

func (sh *Shell) ReconfigureLock() error {
	if sh.lock == nil {
		return nil
	}
	for _, s := range sh.lock.surfaces {
		if err := s.configure(); err != nil {
			return err
		}
	}
	return nil
}

type lockManager struct {
	sh *Shell
	c  *server.Client
}

func (lockManager) Destroy(*sel.ExtSessionLockManagerV1) error {
	return nil
}

func (m lockManager) Lock(_ *sel.ExtSessionLockManagerV1, res *sel.ExtSessionLockV1) (sel.ExtSessionLockV1Handler, error) {
	sh := m.sh
	l := &SessionLock{shell: sh, client: m.c, resource: res}
	if sh.lock != nil {
		l.finished = true
		return l, m.c.Sent(res.SendFinished())
	}
	sh.lock, sh.locked = l, true
	server.OnDestroy(res, l.abandon)
	return l, sh.listener.LockChanged()
}

func (l *SessionLock) abandon() error {
	sh := l.shell
	if sh.lock != l {
		return nil
	}
	sh.lock = nil
	return sh.listener.LockChanged()
}

func (l *SessionLock) Destroy(obj *sel.ExtSessionLockV1) error {
	if l.confirmed && !l.unlocked && !l.finished {
		return obj.ProtocolError(sel.ExtSessionLockV1ErrorInvalidDestroy, "destroy after locked without unlock_and_destroy")
	}
	return nil
}

func (l *SessionLock) UnlockAndDestroy(obj *sel.ExtSessionLockV1) error {
	sh := l.shell
	switch {
	case l.finished:
		return nil
	case !l.confirmed:
		return obj.ProtocolError(sel.ExtSessionLockV1ErrorInvalidUnlock, "unlock before the locked event")
	}
	l.unlocked = true
	if sh.lock != l {
		return nil
	}
	sh.lock, sh.locked = nil, false
	return sh.listener.LockChanged()
}

func (l *SessionLock) GetLockSurface(obj *sel.ExtSessionLockV1, res *sel.ExtSessionLockSurfaceV1, wl *sw.WlSurface, output *sw.WlOutput) (sel.ExtSessionLockSurfaceV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	ls := &LockSurface{lock: l, resource: res, surface: s, output: output}
	switch {
	case slices.ContainsFunc(l.surfaces, func(o *LockSurface) bool { return o.output == output }):
		return nil, obj.ProtocolError(sel.ExtSessionLockV1ErrorDuplicateOutput, "the output already has a lock surface")
	case s.HasBuffer():
		return nil, obj.ProtocolError(sel.ExtSessionLockV1ErrorAlreadyConstructed, "the surface already has a buffer")
	}
	if err := s.SetRole(ls); err != nil {
		return nil, obj.ProtocolError(sel.ExtSessionLockV1ErrorRole, err.Error())
	}
	l.surfaces = append(l.surfaces, ls)
	server.OnDestroy(res, ls.destroy)
	server.OnDestroy(wl, ls.destroy)
	return ls, ls.configure()
}

func (s *LockSurface) configure() error {
	size, ok := s.lock.shell.listener.LockOutputSize(s.output)
	if !ok || size == s.sent {
		return nil
	}
	s.sent = size
	serial := s.lock.shell.display.NextSerial()
	s.configures.push(serial, size)
	return s.lock.client.Sent(s.resource.SendConfigure(serial, uint32(size.X), uint32(size.Y))) //nolint:gosec // output sizes are positive
}

func (s *LockSurface) destroy() error {
	l := s.lock
	i := slices.Index(l.surfaces, s)
	if i < 0 {
		return nil
	}
	l.surfaces = slices.Delete(l.surfaces, i, i+1)
	s.surface.ClearRole(s)
	if !s.mapped || l.shell.lock != l {
		return nil
	}
	s.mapped = false
	return l.shell.listener.LockChanged()
}

func (*LockSurface) Destroy(*sel.ExtSessionLockSurfaceV1) error {
	return nil
}

func (s *LockSurface) AckConfigure(obj *sel.ExtSessionLockSurfaceV1, serial uint32) error {
	if !s.configures.ack(serial) {
		return obj.ProtocolError(sel.ExtSessionLockSurfaceV1ErrorInvalidSerial, fmt.Sprintf("serial %d was never sent", serial))
	}
	return nil
}

func (*LockSurface) RoleName() string {
	return lockRole
}

func (s *LockSurface) Surface() *surface.Surface {
	return s.surface
}

func (s *LockSurface) Output() *sw.WlOutput {
	return s.output
}

func (s *LockSurface) Committed(sf *surface.Surface) error {
	if size, ok := s.configures.take(); ok {
		s.size, s.acked = size, true
	}
	switch {
	case !s.acked:
		return s.resource.ProtocolError(sel.ExtSessionLockSurfaceV1ErrorCommitBeforeFirstAck, "commit before the first ack_configure")
	case sf.Buffer() == nil:
		return s.resource.ProtocolError(sel.ExtSessionLockSurfaceV1ErrorNullBuffer, "a lock surface cannot unmap")
	case sf.Size() != s.size:
		return s.resource.ProtocolError(sel.ExtSessionLockSurfaceV1ErrorDimensionsMismatch, fmt.Sprintf("the buffer is %v, the configure was %v", sf.Size(), s.size))
	}
	s.mapped = true
	if s.lock.shell.lock != s.lock {
		return nil
	}
	return s.lock.shell.listener.LockChanged()
}
