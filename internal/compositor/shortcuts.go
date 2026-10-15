package compositor

import (
	"errors"

	sksi "github.com/stubbedev/tilde/internal/protocol/server/keyboardshortcutsinhibit"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const shortcutsInhibitVersion = 1

type shortcutsInhibitor struct {
	res     *sksi.ZwpKeyboardShortcutsInhibitorV1
	surface *surface.Surface
	hooks   *surface.Hooks
}

func (*shortcutsInhibitor) Destroy(*sksi.ZwpKeyboardShortcutsInhibitorV1) error { return nil }

func (c *Compositor) addShortcutsInhibit(d *server.Display) error {
	_, err := d.AddGlobal(sksi.ZwpKeyboardShortcutsInhibitManagerV1Interface, shortcutsInhibitVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := sksi.NewZwpKeyboardShortcutsInhibitManagerV1(cl, id, version, shortcutsInhibitors{c, cl})
		return err
	})
	return err
}

type shortcutsInhibitors struct {
	c  *Compositor
	cl *server.Client
}

func (shortcutsInhibitors) Destroy(*sksi.ZwpKeyboardShortcutsInhibitManagerV1) error { return nil }

func (m shortcutsInhibitors) InhibitShortcuts(obj *sksi.ZwpKeyboardShortcutsInhibitManagerV1, res *sksi.ZwpKeyboardShortcutsInhibitorV1, wl *sw.WlSurface, _ *sw.WlSeat) (sksi.ZwpKeyboardShortcutsInhibitorV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	if _, ok := m.c.keyInhibitors[s]; ok {
		return nil, obj.ProtocolError(sksi.ZwpKeyboardShortcutsInhibitManagerV1ErrorAlreadyInhibited, "wl_surface@%d already inhibits shortcuts", wl.Base().ID())
	}
	i := &shortcutsInhibitor{res: res, surface: s}
	drop := func() error { return m.c.dropShortcutsInhibitor(i) }
	i.hooks = &surface.Hooks{Destroyed: drop}
	s.AddHooks(i.hooks)
	m.c.keyInhibitors[s] = i
	server.OnDestroy(res, drop)
	return i, m.c.refreshShortcutsInhibitor()
}

func (c *Compositor) dropShortcutsInhibitor(i *shortcutsInhibitor) error {
	if c.keyInhibitors[i.surface] != i {
		return nil
	}
	delete(c.keyInhibitors, i.surface)
	i.surface.RemoveHooks(i.hooks)
	if c.inhibiting == i {
		c.inhibiting = nil
	}
	return nil
}

func (c *Compositor) refreshShortcutsInhibitor() error {
	var want *shortcutsInhibitor
	if focus := c.seat.Focus(); focus != nil {
		want = c.keyInhibitors[focus]
	}
	if want == c.inhibiting {
		return nil
	}
	var errs []error
	if old := c.inhibiting; old != nil {
		errs = append(errs, old.surface.Client().Sent(old.res.SendInactive()))
	}
	c.inhibiting = want
	if want != nil {
		errs = append(errs, want.surface.Client().Sent(want.res.SendActive()))
	}
	return errors.Join(errs...)
}
