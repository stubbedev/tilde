package harness

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cel "github.com/stubbedev/tilde/internal/protocol/client/extforeigntoplevellist"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cwft "github.com/stubbedev/tilde/internal/protocol/client/wlrforeigntoplevel"
	"github.com/stubbedev/tilde/internal/testclient"
)

type extToplevel struct {
	id, title, appID string
	done, closed     bool
}

type extWatcher struct {
	toplevels map[*cel.ExtForeignToplevelHandleV1]*extToplevel
	order     []*cel.ExtForeignToplevelHandleV1
	finished  int
}

func (e *extWatcher) Toplevel(_ *cel.ExtForeignToplevelListV1, h *cel.ExtForeignToplevelHandleV1) (cel.ExtForeignToplevelHandleV1Handler, error) {
	e.toplevels[h] = &extToplevel{}
	e.order = append(e.order, h)
	return e, nil
}

func (e *extWatcher) Finished(*cel.ExtForeignToplevelListV1) error {
	e.finished++
	return nil
}

func (e *extWatcher) Closed(h *cel.ExtForeignToplevelHandleV1) error {
	e.toplevels[h].closed = true
	return nil
}

func (e *extWatcher) Done(h *cel.ExtForeignToplevelHandleV1) error {
	e.toplevels[h].done = true
	return nil
}

func (e *extWatcher) Title(h *cel.ExtForeignToplevelHandleV1, title string) error {
	e.toplevels[h].title = title
	return nil
}

func (e *extWatcher) AppID(h *cel.ExtForeignToplevelHandleV1, appID string) error {
	e.toplevels[h].appID = appID
	return nil
}

func (e *extWatcher) Identifier(h *cel.ExtForeignToplevelHandleV1, id string) error {
	e.toplevels[h].id = id
	return nil
}

type wlrToplevel struct {
	title, appID string
	states       []cwft.ZwlrForeignToplevelHandleV1State
	outputs      int
	closed       bool
}

type wlrWatcher struct {
	toplevels map[*cwft.ZwlrForeignToplevelHandleV1]*wlrToplevel
	order     []*cwft.ZwlrForeignToplevelHandleV1
	finished  int
}

func (w *wlrWatcher) Toplevel(_ *cwft.ZwlrForeignToplevelManagerV1, h *cwft.ZwlrForeignToplevelHandleV1) (cwft.ZwlrForeignToplevelHandleV1Handler, error) {
	w.toplevels[h] = &wlrToplevel{}
	w.order = append(w.order, h)
	return w, nil
}

func (w *wlrWatcher) Finished(*cwft.ZwlrForeignToplevelManagerV1) error {
	w.finished++
	return nil
}

func (w *wlrWatcher) Title(h *cwft.ZwlrForeignToplevelHandleV1, title string) error {
	w.toplevels[h].title = title
	return nil
}

func (w *wlrWatcher) AppID(h *cwft.ZwlrForeignToplevelHandleV1, appID string) error {
	w.toplevels[h].appID = appID
	return nil
}

func (w *wlrWatcher) OutputEnter(h *cwft.ZwlrForeignToplevelHandleV1, _ *cw.WlOutput) error {
	w.toplevels[h].outputs++
	return nil
}

func (w *wlrWatcher) OutputLeave(h *cwft.ZwlrForeignToplevelHandleV1, _ *cw.WlOutput) error {
	w.toplevels[h].outputs--
	return nil
}

func (w *wlrWatcher) State(h *cwft.ZwlrForeignToplevelHandleV1, state []byte) error {
	var s []cwft.ZwlrForeignToplevelHandleV1State
	for i := 0; i+4 <= len(state); i += 4 {
		s = append(s, cwft.ZwlrForeignToplevelHandleV1State(binary.LittleEndian.Uint32(state[i:])))
	}
	w.toplevels[h].states = s
	return nil
}

func (w *wlrWatcher) Done(*cwft.ZwlrForeignToplevelHandleV1) error { return nil }

func (w *wlrWatcher) Closed(h *cwft.ZwlrForeignToplevelHandleV1) error {
	w.toplevels[h].closed = true
	return nil
}

func (w *wlrWatcher) Parent(*cwft.ZwlrForeignToplevelHandleV1, *cwft.ZwlrForeignToplevelHandleV1) error {
	return nil
}

func (w *wlrToplevel) activated() bool {
	return slices.Contains(w.states, cwft.ZwlrForeignToplevelHandleV1StateActivated)
}

func TestForeignToplevelsListAndManageWindows(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	a := c.Window("first", quadrants)
	c.DispatchUntil("first to map", a.Mapped)

	watcher := testclient.Connect(t, h.Socket())
	list, err := cel.NewExtForeignToplevelListV1(watcher.Display, 1, &extWatcher{toplevels: map[*cel.ExtForeignToplevelHandleV1]*extToplevel{}})
	if err != nil {
		t.Fatal(err)
	}
	ext := list.Handler().(*extWatcher)
	mgr, err := cwft.NewZwlrForeignToplevelManagerV1(watcher.Display, 3, &wlrWatcher{toplevels: map[*cwft.ZwlrForeignToplevelHandleV1]*wlrToplevel{}})
	if err != nil {
		t.Fatal(err)
	}
	wlr := mgr.Handler().(*wlrWatcher)
	watcher.Bind("ext_foreign_toplevel_list_v1", list)
	watcher.Bind("zwlr_foreign_toplevel_manager_v1", mgr)
	watcher.DispatchUntil("the first window listed", func() bool { return len(ext.order) == 1 && len(wlr.order) == 1 })
	first := ext.toplevels[ext.order[0]]
	if *first != (extToplevel{id: "tilde-1", title: "first", appID: "tilde.testclient", done: true}) {
		t.Fatalf("ext toplevel %+v", *first)
	}
	if w := wlr.toplevels[wlr.order[0]]; w.title != "first" || w.outputs != 1 || !w.activated() {
		t.Fatalf("wlr toplevel %+v", *w)
	}

	b := c.Window("second", quadrants)
	c.DispatchUntil("second to map", b.Mapped)
	watcher.DispatchUntil("the second window listed and active", func() bool {
		return len(wlr.order) == 2 && wlr.toplevels[wlr.order[1]].activated() && !wlr.toplevels[wlr.order[0]].activated()
	})
	if err := errors.Join(a.Toplevel.SetTitle("renamed"), a.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	watcher.DispatchUntil("the rename seen", func() bool {
		return first.title == "renamed" && wlr.toplevels[wlr.order[0]].title == "renamed"
	})

	seat, _ := watcher.Seat()
	if err := errors.Join(wlr.order[0].Activate(seat), watcher.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	watcher.DispatchUntil("the first window activated by request", func() bool { return wlr.toplevels[wlr.order[0]].activated() })
	if err := errors.Join(wlr.order[0].SetFullscreen(nil), watcher.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the first window fullscreen by request", func(_ *headless.Backend, comp *compositor.Compositor) bool {
		ws := windowsOf(comp)
		return len(ws) == 2 && ws[0].Fullscreen
	})
	if err := errors.Join(wlr.order[1].Close(), watcher.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the second window asked to close", func() bool { return b.Closed })
	if err := errors.Join(b.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	watcher.DispatchUntil("the second window closed in both lists", func() bool {
		return ext.toplevels[ext.order[1]].closed && wlr.toplevels[wlr.order[1]].closed
	})
	if got := ext.toplevels[ext.order[1]].id; got != fmt.Sprintf("tilde-%d", 2) {
		t.Fatalf("second identifier %q", got)
	}
}
