package compositor

import (
	"image"

	"github.com/stubbedev/tilde/internal/output"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/wm"
)

type want struct {
	rect      image.Rectangle
	bounds    image.Point
	layer     wm.PlacementLayer
	placed    bool
	activated bool
	scale     output.Scale
}

type toplevel interface {
	shell.PopupRoot
	Geometry() image.Rectangle
	AppID() string
	Title() string
	pid() int32
	parent() toplevel
	prefersFloating() bool
	scaled() bool
	settled(w want) bool
	apply(w want) error
	close() error
}

type xdgToplevel struct {
	*shell.Toplevel
}

func (t xdgToplevel) pid() int32 {
	return t.Surface().Client().Creds().Pid
}

func (t xdgToplevel) parent() toplevel {
	if p := t.Parent(); p != nil {
		return xdgToplevel{p}
	}
	return nil
}

func (xdgToplevel) prefersFloating() bool {
	return false
}

func (t xdgToplevel) scaled() bool {
	return t.Requested().Bounds != (image.Point{})
}

func xdgState(w want) shell.State {
	st := shell.State{Size: w.rect.Size(), Bounds: w.bounds}
	switch {
	case !w.placed:
	case w.layer == wm.LayerTiled:
		st.States = append(st.States,
			sxdg.XdgToplevelStateTiledLeft, sxdg.XdgToplevelStateTiledRight, sxdg.XdgToplevelStateTiledTop, sxdg.XdgToplevelStateTiledBottom,
			sxdg.XdgToplevelStateConstrainedLeft, sxdg.XdgToplevelStateConstrainedRight, sxdg.XdgToplevelStateConstrainedTop, sxdg.XdgToplevelStateConstrainedBottom)
	case w.layer == wm.LayerFullscreen:
		st.States = append(st.States, sxdg.XdgToplevelStateFullscreen)
	}
	if w.activated {
		st.States = append(st.States, sxdg.XdgToplevelStateActivated)
	}
	return st
}

func (t xdgToplevel) settled(w want) bool {
	return sameState(t.Requested(), xdgState(w))
}

func (t xdgToplevel) apply(w want) error {
	s := t.Surface()
	if w.scale != 0 {
		if err := s.Client().Sent(s.SetPreferredScale(uint32(w.scale))); err != nil {
			return err
		}
	}
	_, err := t.Configure(xdgState(w))
	return s.Client().Sent(err)
}

func (t xdgToplevel) close() error {
	return t.SendClose()
}
