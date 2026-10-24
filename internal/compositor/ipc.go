package compositor

import (
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func (c *Compositor) OnDispatch(f func(wm.Action)) {
	c.dispatched = f
}

func (c *Compositor) Snapshot() ipc.Snapshot {
	s := c.wm
	focusedOutput := s.FocusedOutput()
	snap := ipc.Snapshot{Submap: c.submap, Layout: c.keyboardLayout()}
	for _, o := range c.outputs {
		b := o.Backend()
		mode := b.State().Mode
		active, _ := s.ActiveOn(b.Name())
		snap.Outputs = append(snap.Outputs, ipc.Output{
			Name:        b.Name(),
			Description: b.Description(),
			Rect:        ipc.RectOf(o.Logical()),
			Scale:       float64(o.Scale()) / float64(output.Unit),
			Mode:        ipc.Mode{Width: mode.Size.X, Height: mode.Size.Y, RefreshMHz: mode.RefreshMHz},
			Workspace:   active,
			Focused:     b.Name() == focusedOutput,
			Powered:     b.State().Lit(),
		})
	}
	for _, w := range s.Workspaces() {
		visible := s.Shown(w)
		snap.Workspaces = append(snap.Workspaces, ipc.Workspace{
			ID:      w.ID,
			Output:  w.Output,
			Windows: len(w.Tree.Windows()) + len(w.Floating),
			Visible: visible,
			Focused: visible && w.Output == focusedOutput,
		})
	}
	focused, hasFocus := s.Focused()
	for _, w := range c.windows {
		id, ok := s.WorkspaceOf(w.id)
		if !ok {
			continue
		}
		ws, _ := s.Workspace(id)
		snap.Windows = append(snap.Windows, ipc.Window{
			ID:           w.id,
			AppID:        w.top.AppID(),
			Title:        w.top.Title(),
			PID:          w.top.pid(),
			Workspace:    id,
			Output:       ws.Output,
			Rect:         ipc.RectOf(w.rect),
			Floating:     s.Floats(w.id),
			Fullscreen:   s.Fullscreen(w.id),
			Urgent:       w.urgent,
			Unresponsive: w.hung,
			Focused:      hasFocus && w.id == focused,
		})
	}
	return snap
}

func (c *Compositor) keyboardLayout() ipc.Layout {
	group := c.seat.Layout()
	l := ipc.Layout{Index: group}
	if names := c.seatConfig.Keymap.Layouts(); int(group) < len(names) {
		l.Name = names[group]
	}
	return l
}
