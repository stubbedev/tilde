package shell

import (
	"errors"
	"fmt"
	"image"
	"slices"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/surface"
)

type PopupRoot interface {
	Popups() []*Popup
	Surface() *surface.Surface
}

type popupParent interface {
	children() *[]*Popup
	root() PopupRoot
	offset() image.Point
}

type Popup struct {
	xs         *XdgSurface
	resource   *sxdg.XdgPopup
	parent     popupParent
	rules      Positioner
	token      *uint32
	box        image.Rectangle
	requested  image.Rectangle
	configures configures[image.Rectangle]
	dismissed  bool
}

func (xs *XdgSurface) GetPopup(obj *sxdg.XdgSurface, res *sxdg.XdgPopup, parent *sxdg.XdgSurface, pos *sxdg.XdgPositioner) (sxdg.XdgPopupHandler, error) {
	if err := xs.setSubRole(obj, "xdg_popup"); err != nil {
		return nil, err
	}
	rules, err := xs.base.rules(pos)
	if err != nil {
		return nil, err
	}
	p := &Popup{xs: xs, resource: res, rules: rules}
	if parent != nil {
		px, ok := parent.Handler().(*XdgSurface)
		if !ok {
			return nil, fmt.Errorf("shell: xdg_surface@%d has handler %T", parent.Base().ID(), parent.Handler())
		}
		if px.role == nil || px == xs {
			return nil, xs.base.resource.ProtocolError(sxdg.XdgWmBaseErrorInvalidPopupParent, "the parent xdg_surface has no role")
		}
		p.adopt(px)
	}
	xs.role = p
	xs.base.client.OnDestroy(res, p.destroy)
	return p, nil
}

func (p *Popup) Surface() *surface.Surface {
	return p.xs.surface
}

func (p *Popup) Geometry() image.Rectangle {
	return p.xs.geometry
}

func (p *Popup) Box() image.Rectangle {
	return p.box
}

func (p *Popup) Mapped() bool {
	return p.xs.mapped
}

func (p *Popup) Popups() []*Popup {
	return p.xs.Popups()
}

func (p *Popup) adopt(parent popupParent) {
	p.parent = parent
	list := parent.children()
	*list = append(*list, p)
}

func (p *Popup) parentPopup() *Popup {
	xs, ok := p.parent.(*XdgSurface)
	if !ok {
		return nil
	}
	parent, _ := xs.role.(*Popup)
	return parent
}

func (p *Popup) Root() PopupRoot {
	if p.parent == nil {
		return nil
	}
	return p.parent.root()
}

func (p *Popup) parentOffset() image.Point {
	if p.parent == nil {
		return image.Point{}
	}
	return p.parent.offset()
}

func (xs *XdgSurface) children() *[]*Popup {
	return &xs.popups
}

func (xs *XdgSurface) root() PopupRoot {
	switch r := xs.role.(type) {
	case *Toplevel:
		return r
	case *Popup:
		return r.Root()
	}
	return nil
}

func (xs *XdgSurface) offset() image.Point {
	if p, ok := xs.role.(*Popup); ok {
		return p.parentOffset().Add(p.box.Min)
	}
	return image.Point{}
}

func (p *Popup) ack(serial uint32) bool {
	return p.configures.ack(serial)
}

func (p *Popup) applyPending() error {
	if box, ok := p.configures.take(); ok {
		p.box = box
	}
	return nil
}

func (p *Popup) initialCommit() error {
	root := p.Root()
	if root == nil {
		return p.xs.base.resource.ProtocolError(sxdg.XdgWmBaseErrorInvalidPopupParent, "the popup was committed without a parent")
	}
	if parent := p.parentPopup(); parent != nil && parent.dismissed {
		return p.dismiss()
	}
	return p.place()
}

func (p *Popup) place() error {
	root := p.Root()
	if root == nil {
		return nil
	}
	bounds := p.xs.base.shell.listener.PopupConstraint(root).Sub(p.parentOffset())
	box := p.rules.Place(bounds)
	x, y, err := wirePoint(box.Min)
	if err != nil {
		return err
	}
	w, h, err := wireSize(box.Size())
	if err != nil {
		return err
	}
	c := p.xs.base.client
	if p.token != nil {
		if err := c.Sent(p.resource.SendRepositioned(*p.token)); err != nil {
			return err
		}
		p.token = nil
	}
	if err := c.Sent(p.resource.SendConfigure(x, y, w, h)); err != nil {
		return err
	}
	serial, err := p.xs.sendConfigure()
	if err != nil {
		return err
	}
	p.requested = box
	p.configures.push(serial, box)
	return nil
}

func (p *Popup) Reconstrain() error {
	if !p.rules.Reactive || !p.xs.initialSent || p.dismissed {
		return nil
	}
	return p.place()
}

func (p *Popup) mapped() error {
	sh := p.xs.base.shell
	if err := sh.listener.PopupMapped(p); err != nil {
		return err
	}
	if slices.Contains(sh.grab, p) {
		return sh.listener.GrabChanged()
	}
	return nil
}

func (p *Popup) unmapped() error {
	p.configures.reset()
	return p.xs.base.shell.listener.PopupUnmapped(p)
}

func (p *Popup) inert() bool {
	return p.dismissed
}

func (p *Popup) dismiss() error {
	if p.dismissed {
		return nil
	}
	errs := []error{p.xs.dismissPopups()}
	p.dismissed = true
	if p.xs.mapped {
		errs = append(errs, p.xs.unmap())
	}
	sh := p.xs.base.shell
	if i := slices.Index(sh.grab, p); i >= 0 {
		sh.grab = sh.grab[:i]
		errs = append(errs, sh.listener.GrabChanged())
	}
	errs = append(errs, p.xs.base.client.Sent(p.resource.SendPopupDone()))
	return errors.Join(errs...)
}

func (p *Popup) Destroy(*sxdg.XdgPopup) error {
	sh := p.xs.base.shell
	if i := slices.Index(sh.grab, p); i >= 0 && i != len(sh.grab)-1 {
		return p.xs.base.resource.ProtocolError(sxdg.XdgWmBaseErrorNotTheTopmostPopup, "a grabbing popup was destroyed before the popups above it")
	}
	return nil
}

func (p *Popup) destroy() error {
	xs := p.xs
	sh := xs.base.shell
	var errs []error
	if xs.mapped {
		errs = append(errs, xs.unmap())
	}
	errs = append(errs, xs.dismissPopups())
	if i := slices.Index(sh.grab, p); i >= 0 {
		sh.grab = sh.grab[:i]
		errs = append(errs, sh.listener.GrabChanged())
	}
	if p.parent != nil {
		list := p.parent.children()
		*list = slices.DeleteFunc(*list, func(x *Popup) bool { return x == p })
	}
	p.dismissed = true
	xs.role, xs.configured, xs.initialSent = nil, false, false
	return errors.Join(errs...)
}

func (p *Popup) Grab(obj *sxdg.XdgPopup, _ *sw.WlSeat, serial uint32) error {
	sh := p.xs.base.shell
	if p.xs.initialSent {
		return obj.ProtocolError(sxdg.XdgPopupErrorInvalidGrab, "the popup took a grab after its initial commit")
	}
	parent := p.parentPopup()
	if parent != nil && !parent.dismissed && !slices.Contains(sh.grab, parent) {
		return obj.ProtocolError(sxdg.XdgPopupErrorInvalidGrab, "the parent popup has no grab")
	}
	c := p.xs.base.client
	if (parent != nil && parent.dismissed) || !sh.listener.UserActive(c, serial) {
		return p.dismiss()
	}
	keep := 0
	if parent != nil {
		keep = slices.Index(sh.grab, parent) + 1
	}
	if err := sh.dismissAbove(keep); err != nil {
		return err
	}
	sh.grab = append(sh.grab, p)
	return sh.listener.GrabChanged()
}

func (p *Popup) Reposition(_ *sxdg.XdgPopup, pos *sxdg.XdgPositioner, token uint32) error {
	rules, err := p.xs.base.rules(pos)
	if err != nil {
		return err
	}
	p.rules, p.token = rules, &token
	if !p.xs.initialSent || p.dismissed {
		return nil
	}
	return p.place()
}

func (sh *Shell) dismissAbove(keep int) error {
	if len(sh.grab) <= keep {
		return nil
	}
	dropped := slices.Clone(sh.grab[keep:])
	sh.grab = sh.grab[:keep]
	var errs []error
	for _, p := range slices.Backward(dropped) {
		errs = append(errs, p.dismiss())
	}
	return errors.Join(errs...)
}

func (sh *Shell) DismissGrab() error {
	if len(sh.grab) == 0 {
		return nil
	}
	return errors.Join(sh.dismissAbove(0), sh.listener.GrabChanged())
}

func (sh *Shell) Grab() []*Popup {
	return slices.Clone(sh.grab)
}
