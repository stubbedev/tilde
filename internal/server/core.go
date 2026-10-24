package server

import (
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/wire"
)

type displayRequests struct{ c *Client }

func (r displayRequests) Sync(_ *sw.WlDisplay, callback *sw.WlCallback) error {
	return callback.SendDone(r.c.display.NextSerial())
}

func (r displayRequests) GetRegistry(_ *sw.WlDisplay, registry *sw.WlRegistry) (sw.WlRegistryHandler, error) {
	c := r.c
	c.registries = append(c.registries, registry)
	for _, g := range c.display.globals {
		if g.removed || !g.visible(c) {
			continue
		}
		if err := registry.SendGlobal(g.name, g.iface.Name, g.version); err != nil {
			return nil, err
		}
	}
	return registryRequests{c}, nil
}

type registryRequests struct{ c *Client }

func (r registryRequests) Bind(registry *sw.WlRegistry, name uint32, iface string, version uint32, id wire.ObjectID) error {
	g := r.c.display.global(name)
	switch {
	case g == nil || !g.visible(r.c):
		return invalidObject(registry, "invalid global %s (%d)", iface, name)
	case g.iface.Name != iface:
		return invalidObject(registry, "invalid interface for global %d: have %s, wanted %s", name, iface, g.iface.Name)
	case version == 0 || version > g.version:
		return invalidObject(registry, "invalid version for global %s (%d): have %d, wanted %d", iface, name, version, g.version)
	}
	return g.bind(r.c, id, version)
}
