package compositor

import (
	"errors"
	"fmt"
	"image"
	"math"

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
	locked   *spc.ZwpLockedPointerV1
	confined *spc.ZwpConfinedPointerV1
	oneshot  bool
	active   bool
	spent    bool
	hooks    *surface.Hooks
	pending  constraintState
	current  constraintState
}

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
	k, err := m.constrain(obj, res, wl, g, lifetime)
	if err != nil {
		return nil, err
	}
	k.locked = res
	return lockedPointer{k}, k.c.refreshConstraint()
}

func (m pointerConstraints) ConfinePointer(obj *spc.ZwpPointerConstraintsV1, res *spc.ZwpConfinedPointerV1, wl *sw.WlSurface, _ *sw.WlPointer, g *sw.WlRegion, lifetime spc.ZwpPointerConstraintsV1Lifetime) (spc.ZwpConfinedPointerV1Handler, error) {
	k, err := m.constrain(obj, res, wl, g, lifetime)
	if err != nil {
		return nil, err
	}
	k.confined = res
	return confinedPointer{k}, k.c.refreshConstraint()
}

func (m pointerConstraints) constrain(obj *spc.ZwpPointerConstraintsV1, res wire.Resource, wl *sw.WlSurface, g *sw.WlRegion, lifetime spc.ZwpPointerConstraintsV1Lifetime) (*constraint, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	if _, ok := m.c.constraints[s]; ok {
		return nil, obj.ProtocolError(spc.ZwpPointerConstraintsV1ErrorAlreadyConstrained, fmt.Sprintf("wl_surface@%d already has a pointer constraint", wl.Base().ID()))
	}
	k := &constraint{c: m.c, surface: s, oneshot: lifetime == spc.ZwpPointerConstraintsV1LifetimeOneshot}
	if err := k.current.setRegion(g); err != nil {
		return nil, err
	}
	k.hooks = &surface.Hooks{Commit: k.commit, Applied: k.applied, Destroyed: k.surfaceGone}
	s.AddHooks(k.hooks)
	m.c.constraints[s] = k
	m.cl.OnDestroy(res, k.drop)
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
	if !k.active {
		return k.c.refreshConstraint()
	}
	return k.c.confineCursor()
}

func (k *constraint) drop() error {
	if k.c.constraints[k.surface] != k {
		return nil
	}
	var err error
	if k.active {
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
	if k.active {
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
	return k.area().Contains(image.Pt(int(math.Floor(local.X)), int(math.Floor(local.Y))))
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
	cl := k.surface.Client()
	switch {
	case k.locked != nil && active:
		return cl.Sent(k.locked.SendLocked())
	case k.locked != nil:
		return cl.Sent(k.locked.SendUnlocked())
	case active:
		return cl.Sent(k.confined.SendConfined())
	}
	return cl.Sent(k.confined.SendUnconfined())
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
	for _, w := range c.windows {
		if w.top.Surface() != focus {
			continue
		}
		for _, pp := range c.popups(w) {
			if pp.popup.Surface() == s {
				return true
			}
		}
	}
	return false
}

func (c *Compositor) eligibleConstraint() *constraint {
	focus := c.seat.PointerFocus()
	if focus == nil || c.interaction != nil || c.drag != nil {
		return nil
	}
	k, ok := c.constraints[focus]
	if !ok || k.spent || !c.ownedByFocus(focus) {
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
	if want == nil || want.active {
		return errors.Join(errs...)
	}
	org, ok := c.originOf(want.surface)
	if !ok || !want.contains(org.local(c.cursor)) {
		return errors.Join(errs...)
	}
	want.active, c.constraint = true, want
	return errors.Join(append(errs, want.send(true))...)
}

func (c *Compositor) deactivate(k *constraint) error {
	return errors.Join(c.release(k), k.send(false))
}

func (c *Compositor) release(k *constraint) error {
	k.active, k.spent = false, k.oneshot
	c.constraint = nil
	org, ok := c.originOf(k.surface)
	if !ok || k.locked == nil || k.current.set&constraintHint == 0 || !k.contains(k.current.hint) {
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
	if k.locked != nil {
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
