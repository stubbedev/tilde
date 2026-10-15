package compositor

import (
	"errors"
	"math"
	"slices"

	"github.com/stubbedev/tilde/internal/backend"
	spc "github.com/stubbedev/tilde/internal/protocol/server/pointerconstraints"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/region"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const pointerConstraintsVersion = 1

const subpixel = 1.0 / 256

type constraintField uint8

const (
	constraintRegion constraintField = 1 << iota
	constraintHint
)

type constraintState struct {
	set      constraintField
	region   region.Region
	infinite bool
	hint     point
}

func (st *constraintState) merge(o *constraintState) {
	if o.set&constraintRegion != 0 {
		st.region, st.infinite = o.region, o.infinite
	}
	if o.set&constraintHint != 0 {
		st.hint = o.hint
	}
	st.set |= o.set
}

func (st *constraintState) setRegion(g *sw.WlRegion) error {
	st.set |= constraintRegion
	st.region, st.infinite = region.Region{}, g == nil
	if g == nil {
		return nil
	}
	v, err := surface.RegionValue(g)
	st.region = v
	return err
}

type constraint struct {
	c        *Compositor
	surface  *surface.Surface
	kind     constraintKind
	lifetime spc.ZwpPointerConstraintsV1Lifetime
	phase    constraintPhase
	hooks    *surface.Hooks
	pending  constraintState
	current  constraintState
}

type constraintKind interface {
	send(cl *server.Client, active bool) error
}

type lock struct{ res *spc.ZwpLockedPointerV1 }

type confine struct{ res *spc.ZwpConfinedPointerV1 }

func (l lock) send(cl *server.Client, active bool) error {
	if active {
		return cl.Sent(l.res.SendLocked())
	}
	return cl.Sent(l.res.SendUnlocked())
}

func (f confine) send(cl *server.Client, active bool) error {
	if active {
		return cl.Sent(f.res.SendConfined())
	}
	return cl.Sent(f.res.SendUnconfined())
}

type constraintPhase uint8

const (
	constraintInactive constraintPhase = iota
	constraintActive
	constraintSpent
)

func (c *Compositor) addPointerConstraints(d *server.Display) error {
	_, err := d.AddGlobal(spc.ZwpPointerConstraintsV1Interface, pointerConstraintsVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := spc.NewZwpPointerConstraintsV1(cl, id, version, pointerConstraints{c, cl})
		return err
	})
	return err
}

type pointerConstraints struct {
	c  *Compositor
	cl *server.Client
}

func (pointerConstraints) Destroy(*spc.ZwpPointerConstraintsV1) error { return nil }

func (m pointerConstraints) LockPointer(obj *spc.ZwpPointerConstraintsV1, res *spc.ZwpLockedPointerV1, wl *sw.WlSurface, _ *sw.WlPointer, g *sw.WlRegion, lifetime spc.ZwpPointerConstraintsV1Lifetime) (spc.ZwpLockedPointerV1Handler, error) {
	k, err := m.constrain(obj, res, lock{res}, wl, g, lifetime)
	if err != nil {
		return nil, err
	}
	return lockedPointer{k}, k.c.refreshConstraint()
}

func (m pointerConstraints) ConfinePointer(obj *spc.ZwpPointerConstraintsV1, res *spc.ZwpConfinedPointerV1, wl *sw.WlSurface, _ *sw.WlPointer, g *sw.WlRegion, lifetime spc.ZwpPointerConstraintsV1Lifetime) (spc.ZwpConfinedPointerV1Handler, error) {
	k, err := m.constrain(obj, res, confine{res}, wl, g, lifetime)
	if err != nil {
		return nil, err
	}
	return confinedPointer{k}, k.c.refreshConstraint()
}

func (m pointerConstraints) constrain(obj *spc.ZwpPointerConstraintsV1, res wire.Resource, kind constraintKind, wl *sw.WlSurface, g *sw.WlRegion, lifetime spc.ZwpPointerConstraintsV1Lifetime) (*constraint, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	if _, ok := m.c.constraints[s]; ok {
		return nil, obj.ProtocolError(spc.ZwpPointerConstraintsV1ErrorAlreadyConstrained, "wl_surface@%d already has a pointer constraint", wl.Base().ID())
	}
	k := &constraint{c: m.c, surface: s, kind: kind, lifetime: lifetime}
	if err := k.current.setRegion(g); err != nil {
		return nil, err
	}
	k.hooks = &surface.Hooks{Commit: k.commit, Applied: k.applied, Destroyed: k.surfaceGone}
	s.AddHooks(k.hooks)
	m.c.constraints[s] = k
	server.OnDestroy(res, k.drop)
	return k, nil
}

type lockedPointer struct{ *constraint }

func (lockedPointer) Destroy(*spc.ZwpLockedPointerV1) error { return nil }

func (k lockedPointer) SetCursorPositionHint(_ *spc.ZwpLockedPointerV1, x, y wire.Fixed) error {
	k.pending.set |= constraintHint
	k.pending.hint = point{x.Float(), y.Float()}
	return nil
}

func (k lockedPointer) SetRegion(_ *spc.ZwpLockedPointerV1, g *sw.WlRegion) error {
	return k.pending.setRegion(g)
}

type confinedPointer struct{ *constraint }

func (confinedPointer) Destroy(*spc.ZwpConfinedPointerV1) error { return nil }

func (k confinedPointer) SetRegion(_ *spc.ZwpConfinedPointerV1, g *sw.WlRegion) error {
	return k.pending.setRegion(g)
}

func (k *constraint) commit(u *surface.Update) error {
	st := k.pending
	k.pending = constraintState{}
	u.OnApply(func() error {
		k.current.merge(&st)
		return nil
	})
	return nil
}

func (k *constraint) applied() error {
	if k.phase != constraintActive {
		return k.c.refreshConstraint()
	}
	return k.c.confineCursor()
}

func (k *constraint) drop() error {
	if k.c.constraints[k.surface] != k {
		return nil
	}
	var err error
	if k.phase == constraintActive {
		err = k.c.release(k)
	}
	delete(k.c.constraints, k.surface)
	k.surface.RemoveHooks(k.hooks)
	return errors.Join(err, k.c.refreshConstraint())
}

func (k *constraint) surfaceGone() error {
	if k.c.constraints[k.surface] != k {
		return nil
	}
	var err error
	if k.phase == constraintActive {
		err = k.c.deactivate(k)
	}
	return errors.Join(err, k.drop())
}

func (k *constraint) area() region.Region {
	input := k.surface.Input()
	if k.current.infinite {
		return input
	}
	var out region.Region
	for _, r := range k.current.region.Rects() {
		out = out.Union(input.Intersect(r))
	}
	return out
}

func (k *constraint) contains(local point) bool {
	return k.area().Contains(local.pixel())
}

func (k *constraint) clamp(local point) point {
	if k.contains(local) {
		return local
	}
	best, dist := local, math.Inf(1)
	for _, r := range k.area().Rects() {
		q := point{
			X: math.Min(math.Max(local.X, float64(r.Min.X)), float64(r.Max.X)-subpixel),
			Y: math.Min(math.Max(local.Y, float64(r.Min.Y)), float64(r.Max.Y)-subpixel),
		}
		if d := math.Hypot(q.X-local.X, q.Y-local.Y); d < dist {
			best, dist = q, d
		}
	}
	return best
}

func (k *constraint) send(active bool) error {
	return k.kind.send(k.surface.Client(), active)
}

func (k *constraint) locks() bool {
	_, ok := k.kind.(lock)
	return ok
}

func (c *Compositor) ownedByFocus(s *surface.Surface) bool {
	focus := c.seat.Focus()
	if focus == nil {
		return false
	}
	for s.Subsurface() != nil && s.Subsurface().Parent() != nil {
		s = s.Subsurface().Parent()
	}
	if s == focus {
		return true
	}
	w := c.windowWithSurface(focus)
	if w == nil {
		return false
	}
	return slices.ContainsFunc(c.popups(w), func(pp placedPopup) bool { return pp.popup.Surface() == s })
}

func (c *Compositor) eligibleConstraint() *constraint {
	focus := c.seat.PointerFocus()
	if focus == nil || c.interaction != nil || c.drag != nil {
		return nil
	}
	k, ok := c.constraints[focus]
	if !ok || k.phase == constraintSpent || !c.ownedByFocus(focus) {
		return nil
	}
	return k
}

func (c *Compositor) refreshConstraint() error {
	want := c.eligibleConstraint()
	var errs []error
	if old := c.constraint; old != nil && old != want {
		errs = append(errs, c.deactivate(old))
	}
	if want == nil || want.phase == constraintActive {
		return errors.Join(errs...)
	}
	org, ok := c.originOf(want.surface)
	if !ok || !want.contains(org.local(c.cursor)) {
		return errors.Join(errs...)
	}
	want.phase, c.constraint = constraintActive, want
	return errors.Join(append(errs, want.send(true))...)
}

func (c *Compositor) deactivate(k *constraint) error {
	return errors.Join(c.release(k), k.send(false))
}

func (c *Compositor) release(k *constraint) error {
	k.phase = constraintInactive
	if k.lifetime == spc.ZwpPointerConstraintsV1LifetimeOneshot {
		k.phase = constraintSpent
	}
	c.constraint = nil
	org, ok := c.originOf(k.surface)
	if !ok || !k.locks() || k.current.set&constraintHint == 0 || !k.contains(k.current.hint) {
		return nil
	}
	c.cursor = org.global(k.current.hint)
	return c.cursorMoved()
}

func (c *Compositor) constrainMotion(next point) (point, bool) {
	k := c.constraint
	if k == nil {
		return next, true
	}
	if k.locks() {
		return c.cursor, false
	}
	org, ok := c.originOf(k.surface)
	if !ok {
		return next, true
	}
	return org.global(k.clamp(org.local(next))), true
}

func (c *Compositor) confineCursor() error {
	next, moved := c.constrainMotion(c.cursor)
	if !moved || next == c.cursor {
		return nil
	}
	c.cursor = next
	return c.pointerMoved(backend.Now())
}
