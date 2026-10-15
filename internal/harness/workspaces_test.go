package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cew "github.com/stubbedev/tilde/internal/protocol/client/extworkspace"
	csb "github.com/stubbedev/tilde/internal/protocol/client/systembell"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wm"
)

type wsView struct {
	groups  []*cew.ExtWorkspaceGroupHandleV1
	outputs map[*cew.ExtWorkspaceGroupHandleV1]int
	members map[*cew.ExtWorkspaceHandleV1]*cew.ExtWorkspaceGroupHandleV1
	names   map[*cew.ExtWorkspaceHandleV1]string
	states  map[*cew.ExtWorkspaceHandleV1]cew.ExtWorkspaceHandleV1State
	removed map[*cew.ExtWorkspaceHandleV1]bool
	dones   int
}

type wsGroups struct{ v *wsView }

func (v *wsView) WorkspaceGroup(_ *cew.ExtWorkspaceManagerV1, g *cew.ExtWorkspaceGroupHandleV1) (cew.ExtWorkspaceGroupHandleV1Handler, error) {
	v.groups = append(v.groups, g)
	return wsGroups{v}, nil
}

func (v *wsView) Workspace(*cew.ExtWorkspaceManagerV1, *cew.ExtWorkspaceHandleV1) (cew.ExtWorkspaceHandleV1Handler, error) {
	return v, nil
}

func (v *wsView) Done(*cew.ExtWorkspaceManagerV1) error {
	v.dones++
	return nil
}

func (v *wsView) Finished(*cew.ExtWorkspaceManagerV1) error { return nil }

func (wsGroups) Capabilities(*cew.ExtWorkspaceGroupHandleV1, cew.ExtWorkspaceGroupHandleV1GroupCapabilities) error {
	return nil
}

func (g wsGroups) OutputEnter(h *cew.ExtWorkspaceGroupHandleV1, _ *cw.WlOutput) error {
	g.v.outputs[h]++
	return nil
}

func (g wsGroups) OutputLeave(h *cew.ExtWorkspaceGroupHandleV1, _ *cw.WlOutput) error {
	g.v.outputs[h]--
	return nil
}

func (g wsGroups) WorkspaceEnter(h *cew.ExtWorkspaceGroupHandleV1, w *cew.ExtWorkspaceHandleV1) error {
	g.v.members[w] = h
	return nil
}

func (g wsGroups) WorkspaceLeave(_ *cew.ExtWorkspaceGroupHandleV1, w *cew.ExtWorkspaceHandleV1) error {
	delete(g.v.members, w)
	return nil
}

func (wsGroups) Removed(*cew.ExtWorkspaceGroupHandleV1) error { return nil }

func (v *wsView) ID(*cew.ExtWorkspaceHandleV1, string) error { return nil }

func (v *wsView) Name(w *cew.ExtWorkspaceHandleV1, name string) error {
	v.names[w] = name
	return nil
}

func (v *wsView) Coordinates(*cew.ExtWorkspaceHandleV1, []byte) error { return nil }

func (v *wsView) State(w *cew.ExtWorkspaceHandleV1, s cew.ExtWorkspaceHandleV1State) error {
	v.states[w] = s
	return nil
}

func (v *wsView) Capabilities(*cew.ExtWorkspaceHandleV1, cew.ExtWorkspaceHandleV1WorkspaceCapabilities) error {
	return nil
}

func (v *wsView) Removed(w *cew.ExtWorkspaceHandleV1) error {
	v.removed[w] = true
	return nil
}

func (v *wsView) named(name string) *cew.ExtWorkspaceHandleV1 {
	for w, n := range v.names {
		if n == name && !v.removed[w] {
			return w
		}
	}
	return nil
}

func (v *wsView) active(name string) bool {
	w := v.named(name)
	return w != nil && v.states[w]&cew.ExtWorkspaceHandleV1StateActive != 0
}

func watchWorkspaces(t *testing.T, h *Harness) (*wsView, *testclient.Client, *cew.ExtWorkspaceManagerV1) {
	t.Helper()
	v := &wsView{
		outputs: map[*cew.ExtWorkspaceGroupHandleV1]int{}, members: map[*cew.ExtWorkspaceHandleV1]*cew.ExtWorkspaceGroupHandleV1{},
		names: map[*cew.ExtWorkspaceHandleV1]string{}, states: map[*cew.ExtWorkspaceHandleV1]cew.ExtWorkspaceHandleV1State{}, removed: map[*cew.ExtWorkspaceHandleV1]bool{},
	}
	watcher := testclient.Connect(t, h.Socket())
	mgr, err := cew.NewExtWorkspaceManagerV1(watcher.Display, 1, v)
	if err != nil {
		t.Fatal(err)
	}
	watcher.Bind("ext_workspace_manager_v1", mgr)
	watcher.DispatchUntil("the first batch", func() bool { return v.dones > 0 })
	return v, watcher, mgr
}

func TestAnUrgentWindowMarksItsWorkspaceUrgent(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	first := c.Window("first", quadrants)
	c.DispatchUntil("the first window to map", first.Mapped)
	second := c.Window("second", quadrants)
	c.DispatchUntil("the second window to map", second.Mapped)
	v, watcher, _ := watchWorkspaces(t, h)
	if v.states[v.named("1")]&cew.ExtWorkspaceHandleV1StateUrgent != 0 {
		t.Fatal("workspace 1 started urgent")
	}
	bell, err := csb.NewXdgSystemBellV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("xdg_system_bell_v1", bell)
	if err := errors.Join(bell.Ring(first.Surface), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	watcher.DispatchUntil("workspace 1 urgent", func() bool { return v.states[v.named("1")]&cew.ExtWorkspaceHandleV1StateUrgent != 0 })
}

func TestExtWorkspacesFollowAndDriveTheWindowManager(t *testing.T) {
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("busy", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	v, watcher, mgr := watchWorkspaces(t, h)
	if len(v.groups) != 2 || v.outputs[v.groups[0]] != 1 || v.outputs[v.groups[1]] != 1 {
		t.Fatalf("groups %d with outputs %v", len(v.groups), v.outputs)
	}
	if !v.active("1") || !v.active("2") || v.members[v.named("1")] != v.groups[0] || v.members[v.named("2")] != v.groups[1] {
		t.Fatalf("workspaces %v states %v", v.names, v.states)
	}

	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		a, err := wm.ParseAction("workspace 3")
		if err != nil {
			return err
		}
		return comp.Dispatch(a)
	})
	watcher.DispatchUntil("workspace 3 active and 1 hidden behind it", func() bool {
		return v.active("3") && v.named("1") != nil && !v.active("1") && v.members[v.named("3")] == v.groups[0]
	})

	three := v.named("3")
	if err := errors.Join(v.named("1").Activate(), mgr.Commit(), watcher.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	watcher.DispatchUntil("workspace 1 active again and the empty 3 gone", func() bool { return v.active("1") && v.removed[three] })

	if err := errors.Join(v.named("1").Assign(v.groups[1]), mgr.Commit(), watcher.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	watcher.DispatchUntil("workspace 1 moved to the second output", func() bool { return v.members[v.named("1")] == v.groups[1] })
	h.Eventually("the window manager agrees", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		ws, ok := comp.WM().Workspace(1)
		return ok && ws.Output == "HEADLESS-2"
	})
}
