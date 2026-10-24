package compositor

import (
	"cmp"
	"encoding/binary"
	"errors"
	"maps"
	"slices"
	"strconv"

	sew "github.com/stubbedev/lys-wm/internal/protocol/server/extworkspace"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const extWorkspaceVersion = 1

type wsManager struct {
	client   *server.Client
	resource *sew.ExtWorkspaceManagerV1
	groups   map[string]*wsGroup
	spaces   map[wm.WorkspaceID]*wsSpace
	pending  []func(wm.State) (wm.State, error)
}

type wsGroup struct {
	resource *sew.ExtWorkspaceGroupHandleV1
	output   string
}

type wsSpace struct {
	resource *sew.ExtWorkspaceHandleV1
	group    string
	state    sew.ExtWorkspaceHandleV1State
}

func (c *Compositor) addWorkspaces(d *server.Display) error {
	_, err := d.AddGlobal(sew.ExtWorkspaceManagerV1Interface, extWorkspaceVersion, c.bindWorkspaces)
	return err
}

func (c *Compositor) bindWorkspaces(cl *server.Client, id wire.ObjectID, version uint32) error {
	m := &wsManager{client: cl, groups: map[string]*wsGroup{}, spaces: map[wm.WorkspaceID]*wsSpace{}}
	var err error
	if m.resource, err = sew.NewExtWorkspaceManagerV1(cl, id, version, wsManagerRequests{c, m}); err != nil {
		return err
	}
	server.Track(m.resource, &c.wsManagers, m)
	return c.syncWorkspaceManager(m)
}

type wsManagerRequests struct {
	c *Compositor
	m *wsManager
}

func (r wsManagerRequests) Commit(*sew.ExtWorkspaceManagerV1) error {
	s := r.c.wm
	var errs []error
	for _, f := range r.m.pending {
		next, err := f(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s = next
	}
	r.m.pending = r.m.pending[:0]
	if err := errors.Join(errs...); err != nil {
		r.c.report(err)
	}
	return r.c.update(s, nil)
}

func (r wsManagerRequests) Stop(*sew.ExtWorkspaceManagerV1) error {
	return r.m.resource.SendFinished()
}

type wsGroupRequests struct{}

func (wsGroupRequests) CreateWorkspace(*sew.ExtWorkspaceGroupHandleV1, string) error {
	return nil
}

func (wsGroupRequests) Destroy(*sew.ExtWorkspaceGroupHandleV1) error {
	return nil
}

type wsSpaceRequests struct {
	m  *wsManager
	id wm.WorkspaceID
}

func (r wsSpaceRequests) Activate(*sew.ExtWorkspaceHandleV1) error {
	id := r.id
	r.m.pending = append(r.m.pending, func(s wm.State) (wm.State, error) {
		if _, ok := s.Workspace(id); !ok {
			return s, nil
		}
		return s.SwitchWorkspace(id)
	})
	return nil
}

func (r wsSpaceRequests) Assign(_ *sew.ExtWorkspaceHandleV1, group *sew.ExtWorkspaceGroupHandleV1) error {
	id := r.id
	for _, g := range r.m.groups {
		if g.resource != group {
			continue
		}
		output := g.output
		r.m.pending = append(r.m.pending, func(s wm.State) (wm.State, error) {
			if _, ok := s.Workspace(id); !ok {
				return s, nil
			}
			return s.AssignWorkspace(id, output)
		})
	}
	return nil
}

func (wsSpaceRequests) Deactivate(*sew.ExtWorkspaceHandleV1) error { return nil }
func (wsSpaceRequests) Remove(*sew.ExtWorkspaceHandleV1) error     { return nil }
func (wsSpaceRequests) Destroy(*sew.ExtWorkspaceHandleV1) error    { return nil }

func (c *Compositor) workspaceState(w wm.Workspace) sew.ExtWorkspaceHandleV1State {
	var st sew.ExtWorkspaceHandleV1State
	if c.wm.Shown(w) {
		st |= sew.ExtWorkspaceHandleV1StateActive
	}
	for _, win := range c.windows {
		if id, ok := c.wm.WorkspaceOf(win.id); win.urgent && ok && id == w.ID {
			st |= sew.ExtWorkspaceHandleV1StateUrgent
			break
		}
	}
	return st
}

func (c *Compositor) syncWorkspaceManager(m *wsManager) error {
	cl, changed := m.client, false
	var errs []error
	sent := func(err error) {
		changed = true
		errs = append(errs, err)
	}
	outputs := map[string]bool{}
	for _, o := range c.outputs {
		name := o.Backend().Name()
		outputs[name] = true
		if _, ok := m.groups[name]; ok {
			continue
		}
		res, err := m.resource.SendWorkspaceGroup(wsGroupRequests{})
		if err != nil {
			sent(err)
			continue
		}
		m.groups[name] = &wsGroup{resource: res, output: name}
		sent(res.SendCapabilities(0))
		for _, wl := range o.Resources(cl) {
			sent(res.SendOutputEnter(wl))
		}
	}
	live := map[wm.WorkspaceID]bool{}
	for _, w := range c.wm.Workspaces() {
		live[w.ID] = true
		sp, ok := m.spaces[w.ID]
		if !ok {
			res, err := m.resource.SendWorkspace(wsSpaceRequests{m, w.ID})
			if err != nil {
				sent(err)
				continue
			}
			sp = &wsSpace{resource: res, state: ^sew.ExtWorkspaceHandleV1State(0)}
			m.spaces[w.ID] = sp
			name := strconv.Itoa(int(w.ID))
			sent(errors.Join(res.SendID(name), res.SendName(name),
				res.SendCoordinates(binary.LittleEndian.AppendUint32(nil, uint32(w.ID))), //nolint:gosec // workspace ids are small positive numbers
				res.SendCapabilities(sew.ExtWorkspaceHandleV1WorkspaceCapabilitiesActivate|sew.ExtWorkspaceHandleV1WorkspaceCapabilitiesAssign)))
		}
		if st := c.workspaceState(w); st != sp.state {
			sp.state = st
			sent(sp.resource.SendState(st))
		}
		if sp.group != w.Output {
			if g, ok := m.groups[sp.group]; ok {
				sent(g.resource.SendWorkspaceLeave(sp.resource))
			}
			if g, ok := m.groups[w.Output]; ok {
				sent(g.resource.SendWorkspaceEnter(sp.resource))
			}
			sp.group = w.Output
		}
	}
	for _, id := range slices.Sorted(maps.Keys(m.spaces)) {
		if !live[id] {
			sent(m.spaces[id].resource.SendRemoved())
			delete(m.spaces, id)
		}
	}
	for _, name := range slices.SortedFunc(maps.Keys(m.groups), cmp.Compare[string]) {
		if !outputs[name] {
			sent(m.groups[name].resource.SendRemoved())
			delete(m.groups, name)
		}
	}
	if changed {
		errs = append(errs, m.resource.SendDone())
	}
	return errors.Join(errs...)
}

func (c *Compositor) syncWorkspaces() error {
	var errs []error
	for _, m := range c.wsManagers {
		errs = append(errs, c.syncWorkspaceManager(m))
	}
	return errors.Join(errs...)
}
