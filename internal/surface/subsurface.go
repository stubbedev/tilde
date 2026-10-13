package surface

import (
	"fmt"
	"image"
	"slices"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
)

const subsurfaceRole = "wl_subsurface"

type Subsurface struct {
	resource        *sw.WlSubsurface
	surface         *Surface
	parent          *Surface
	sync            bool
	position        image.Point
	pendingPosition image.Point
}

func (sub *Subsurface) RoleName() string {
	return subsurfaceRole
}

func (sub *Subsurface) Committed(*Surface) error {
	return nil
}

func (sub *Subsurface) Parent() *Surface {
	return sub.parent
}

func (sub *Subsurface) Position() image.Point {
	return sub.position
}

func (sub *Subsurface) Synchronized() bool {
	return sub.surface.synchronized()
}

func (sub *Subsurface) detach() {
	if p := sub.parent; p != nil {
		p.order = slices.DeleteFunc(p.order, func(s *Surface) bool { return s == sub.surface })
		p.pendingOrder = slices.DeleteFunc(p.pendingOrder, func(s *Surface) bool { return s == sub.surface })
	}
	sub.parent = nil
}

func (s *Surface) isAncestorOf(other *Surface) bool {
	for p := other; p != nil; {
		if p == s {
			return true
		}
		if p.sub == nil {
			return false
		}
		p = p.sub.parent
	}
	return false
}

type subcompositorRequests struct{}

func (subcompositorRequests) Destroy(*sw.WlSubcompositor) error {
	return nil
}

func (subcompositorRequests) GetSubsurface(obj *sw.WlSubcompositor, res *sw.WlSubsurface, surface, parent *sw.WlSurface) (sw.WlSubsurfaceHandler, error) {
	s, err := surfaceOf(surface)
	if err != nil {
		return nil, err
	}
	p, err := surfaceOf(parent)
	if err != nil {
		return nil, err
	}
	if s.isAncestorOf(p) {
		return nil, obj.ProtocolError(sw.WlSubcompositorErrorBadParent, fmt.Sprintf("wl_surface@%d cannot be a subsurface of itself or its descendant wl_surface@%d", surface.Base().ID(), parent.Base().ID()))
	}
	sub := &Subsurface{resource: res, surface: s, parent: p, sync: true}
	if err := s.SetRole(sub); err != nil {
		return nil, obj.ProtocolError(sw.WlSubcompositorErrorBadSurface, err.Error())
	}
	s.sub = sub
	p.order = append(p.order, s)
	p.pendingOrder = append(p.pendingOrder, s)
	s.client.OnDestroy(res, func() { sub.destroy() })
	return subsurfaceRequests{sub}, nil
}

func (sub *Subsurface) destroy() {
	s := sub.surface
	sub.detach()
	if s.sub == sub {
		s.sub = nil
	}
	s.ClearRole(sub)
}

type subsurfaceRequests struct{ sub *Subsurface }

func (r subsurfaceRequests) Destroy(*sw.WlSubsurface) error {
	return nil
}

func (r subsurfaceRequests) SetPosition(_ *sw.WlSubsurface, x, y int32) error {
	r.sub.pendingPosition = image.Pt(int(x), int(y))
	return nil
}

func (r subsurfaceRequests) place(obj *sw.WlSubsurface, sibling *sw.WlSurface, above bool) error {
	sub := r.sub
	p := sub.parent
	if p == nil {
		return nil
	}
	ref, err := surfaceOf(sibling)
	if err != nil {
		return err
	}
	if ref == sub.surface || (ref != p && (ref.sub == nil || ref.sub.parent != p)) {
		return obj.ProtocolError(sw.WlSubsurfaceErrorBadSurface, fmt.Sprintf("wl_surface@%d is neither the parent nor a sibling", sibling.Base().ID()))
	}
	order := slices.DeleteFunc(p.pendingOrder, func(s *Surface) bool { return s == sub.surface })
	i := slices.Index(order, ref)
	if above {
		i++
	}
	p.pendingOrder = slices.Insert(order, i, sub.surface)
	return nil
}

func (r subsurfaceRequests) PlaceAbove(obj *sw.WlSubsurface, sibling *sw.WlSurface) error {
	return r.place(obj, sibling, true)
}

func (r subsurfaceRequests) PlaceBelow(obj *sw.WlSubsurface, sibling *sw.WlSurface) error {
	return r.place(obj, sibling, false)
}

func (r subsurfaceRequests) SetSync(*sw.WlSubsurface) error {
	r.sub.sync = true
	return nil
}

func (r subsurfaceRequests) SetDesync(*sw.WlSubsurface) error {
	sub := r.sub
	sub.sync = false
	if sub.surface.synchronized() {
		return nil
	}
	return sub.surface.flushDesynchronized()
}

func (s *Surface) flushDesynchronized() error {
	if err := s.applyCache(); err != nil {
		return err
	}
	for _, child := range slices.Clone(s.order) {
		if child != s && !child.synchronized() {
			if err := child.flushDesynchronized(); err != nil {
				return err
			}
		}
	}
	return nil
}
