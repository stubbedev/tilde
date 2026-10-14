package compositor

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/binds"
	shgs "github.com/stubbedev/tilde/internal/protocol/server/hyprlandglobalshortcuts"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wm"
)

const globalShortcutsVersion = 1

type globalShortcut struct {
	cl    *server.Client
	res   *shgs.HyprlandGlobalShortcutV1
	appID string
	id    string
}

func (*globalShortcut) Destroy(*shgs.HyprlandGlobalShortcutV1) error { return nil }

func (g *globalShortcut) send(pressed bool) error {
	now := max(backend.Now(), 0)
	sec := uint64(now / time.Second)
	hi, lo, nsec := uint32(sec>>32), uint32(sec), uint32(now%time.Second) //nolint:gosec // split as the protocol asks
	if pressed {
		return g.cl.Sent(g.res.SendPressed(hi, lo, nsec))
	}
	return g.cl.Sent(g.res.SendReleased(hi, lo, nsec))
}

type heldShortcut struct {
	trigger  binds.Trigger
	shortcut *globalShortcut
}

func (c *Compositor) addGlobalShortcuts(d *server.Display) error {
	_, err := d.AddGlobal(shgs.HyprlandGlobalShortcutsManagerV1Interface, globalShortcutsVersion, func(cl *server.Client, id wire.ObjectID, version uint32) error {
		_, err := shgs.NewHyprlandGlobalShortcutsManagerV1(cl, id, version, globalShortcuts{c, cl})
		return err
	})
	return err
}

type globalShortcuts struct {
	c  *Compositor
	cl *server.Client
}

func (globalShortcuts) Destroy(*shgs.HyprlandGlobalShortcutsManagerV1) error { return nil }

func (m globalShortcuts) RegisterShortcut(obj *shgs.HyprlandGlobalShortcutsManagerV1, res *shgs.HyprlandGlobalShortcutV1, id, appID, _, _ string) (shgs.HyprlandGlobalShortcutV1Handler, error) {
	if m.c.globalShortcut(appID, id) != nil {
		return nil, obj.ProtocolError(shgs.HyprlandGlobalShortcutsManagerV1ErrorAlreadyTaken, fmt.Sprintf("%s:%s is already registered", appID, id))
	}
	g := &globalShortcut{cl: m.cl, res: res, appID: appID, id: id}
	m.c.globalShortcuts = append(m.c.globalShortcuts, g)
	m.cl.OnDestroy(res, func() error {
		m.c.globalShortcuts = slices.DeleteFunc(m.c.globalShortcuts, func(x *globalShortcut) bool { return x == g })
		m.c.heldShortcuts = slices.DeleteFunc(m.c.heldShortcuts, func(h heldShortcut) bool { return h.shortcut == g })
		return nil
	})
	return g, nil
}

func (c *Compositor) globalShortcut(appID, id string) *globalShortcut {
	for _, g := range c.globalShortcuts {
		if g.appID == appID && g.id == id {
			return g
		}
	}
	return nil
}

func (c *Compositor) pressShortcut(s wm.Shortcut) error {
	g := c.globalShortcut(s.AppID, s.ID)
	if g == nil {
		return fmt.Errorf("no application registered the global shortcut %s:%s", s.AppID, s.ID)
	}
	err := g.send(true)
	switch c.firing.(type) {
	case binds.Key, binds.Button:
		c.heldShortcuts = append(c.heldShortcuts, heldShortcut{trigger: c.firing, shortcut: g})
		return err
	}
	return errors.Join(err, g.send(false))
}

func (c *Compositor) releaseShortcuts(t binds.Trigger) error {
	var errs []error
	c.heldShortcuts = slices.DeleteFunc(c.heldShortcuts, func(h heldShortcut) bool {
		if h.trigger != t {
			return false
		}
		errs = append(errs, h.shortcut.send(false))
		return true
	})
	return errors.Join(errs...)
}
