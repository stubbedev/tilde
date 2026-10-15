package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cel "github.com/stubbedev/tilde/internal/protocol/client/extforeigntoplevellist"
	cwft "github.com/stubbedev/tilde/internal/protocol/client/wlrforeigntoplevel"
	"github.com/stubbedev/tilde/internal/testclient"
)

func TestStoppedManagersFinishOnceAndFallSilent(t *testing.T) {
	h := Start(t, small)
	watcher := testclient.Connect(t, h.Socket())
	ext := &extWatcher{toplevels: map[*cel.ExtForeignToplevelHandleV1]*extToplevel{}}
	list, err := cel.NewExtForeignToplevelListV1(watcher.Display, 1, ext)
	if err != nil {
		t.Fatal(err)
	}
	wlr := &wlrWatcher{toplevels: map[*cwft.ZwlrForeignToplevelHandleV1]*wlrToplevel{}}
	mgr, err := cwft.NewZwlrForeignToplevelManagerV1(watcher.Display, 3, wlr)
	if err != nil {
		t.Fatal(err)
	}
	watcher.Bind(list)
	watcher.Bind(mgr)
	om := outputManager(t, watcher)
	ws, wsClient, wsMgr := watchWorkspaces(t, h)

	if err := errors.Join(list.Stop(), list.Stop(), mgr.Stop(), om.res.Stop(), wsMgr.Stop(), watcher.Display.Roundtrip(), wsClient.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	omDones, wsDones := om.dones, ws.dones
	c := testclient.Connect(t, h.Socket())
	w := c.Window("late", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	h.dispatch("workspace 2")
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddOutput(small)
		return err
	})
	settle(t, h)
	if err := errors.Join(watcher.Display.Roundtrip(), wsClient.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	if ext.finished != 1 || wlr.finished != 1 || om.finished != 1 || ws.finished != 1 {
		t.Fatalf("finished events: ext list %d, wlr manager %d, output manager %d, workspace manager %d, want one each", ext.finished, wlr.finished, om.finished, ws.finished)
	}
	if len(ext.order) != 0 || len(wlr.order) != 0 || om.dones != omDones || ws.dones != wsDones {
		t.Fatalf("stopped managers kept talking: %d ext and %d wlr toplevels, %d output and %d workspace batches", len(ext.order), len(wlr.order), om.dones-omDones, ws.dones-wsDones)
	}
}
