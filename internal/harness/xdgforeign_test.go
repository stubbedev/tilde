package harness

import (
	"errors"
	"image"
	"testing"

	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cxf1 "github.com/stubbedev/tilde/internal/protocol/client/xdgforeignv1"
	cxf2 "github.com/stubbedev/tilde/internal/protocol/client/xdgforeignv2"
	"github.com/stubbedev/tilde/internal/testclient"
)

type exportedHandle struct{ handle string }

func (e *exportedHandle) Handle(_ *cxf2.ZxdgExportedV2, h string) error {
	e.handle = h
	return nil
}

type exportedHandleV1 struct{ handle string }

func (e *exportedHandleV1) Handle(_ *cxf1.ZxdgExportedV1, h string) error {
	e.handle = h
	return nil
}

type importedState struct{ destroyed bool }

func (i *importedState) Destroyed(*cxf2.ZxdgImportedV2) error {
	i.destroyed = true
	return nil
}

type importedStateV1 struct{ destroyed bool }

func (i *importedStateV1) Destroyed(*cxf1.ZxdgImportedV1) error {
	i.destroyed = true
	return nil
}

type foreignV2 struct {
	exporter *cxf2.ZxdgExporterV2
	importer *cxf2.ZxdgImporterV2
}

func bindForeignV2(t *testing.T, c *testclient.Client) foreignV2 {
	t.Helper()
	exporter, err1 := cxf2.NewZxdgExporterV2(c.Display, 1)
	importer, err2 := cxf2.NewZxdgImporterV2(c.Display, 1)
	if err := errors.Join(err1, err2); err != nil {
		t.Fatal(err)
	}
	c.Bind("zxdg_exporter_v2", exporter)
	c.Bind("zxdg_importer_v2", importer)
	return foreignV2{exporter, importer}
}

func foreignApp(t *testing.T, h *Harness) (*testclient.Client, *testclient.Window) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	left := c.Window("left", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the left window to map", left.Mapped)
	app := c.Window("app", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the app window to map", app.Mapped)
	return c, app
}

func TestAPortalDialogOpensOverTheAppItBelongsTo(t *testing.T) {
	h := Start(t, small)
	app, window := foreignApp(t, h)
	appForeign := bindForeignV2(t, app)
	exported := &exportedHandle{}
	export, err := appForeign.exporter.ExportToplevel(exported, window.Surface)
	if err != nil {
		t.Fatal(err)
	}
	app.DispatchUntil("the handle", func() bool { return exported.handle != "" })

	portal := testclient.Connect(t, h.Socket())
	portalForeign := bindForeignV2(t, portal)
	parent := &importedState{}
	imported, err := portalForeign.importer.ImportToplevel(parent, exported.handle)
	if err != nil {
		t.Fatal(err)
	}
	dialog := portal.WindowWith("picker", testclient.Fill(0, 0xff, 0), func(w *testclient.Window) error {
		return imported.SetParentOf(w.Surface)
	})
	portal.DispatchUntil("the dialog to map", dialog.Mapped)
	if dialog.Logical != image.Pt(64, 64) {
		t.Fatalf("the dialog was sized %v; a dialog with a parent floats at its own size", dialog.Logical)
	}
	h.Eventually("the dialog centred over the app on the right", showing(120, 50, [3]byte{0, 0xff, 0}))
	h.Eventually("the app around the dialog", showing(84, 50, [3]byte{0, 0, 0xff}))
	h.Eventually("the left window left alone", showing(40, 50, [3]byte{0, 0, 0xff}))

	if err := errors.Join(export.Destroy(), app.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	portal.DispatchUntil("the import destroyed with its export", func() bool { return parent.destroyed })

	stale := &importedState{}
	if _, err := portalForeign.importer.ImportToplevel(stale, exported.handle); err != nil {
		t.Fatal(err)
	}
	portal.DispatchUntil("an unknown handle destroyed at once", func() bool { return stale.destroyed })
}

func TestGtk3StyleForeignV1Parents(t *testing.T) {
	h := Start(t, small)
	app, window := foreignApp(t, h)
	exporter, err := cxf1.NewZxdgExporterV1(app.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	app.Bind("zxdg_exporter_v1", exporter)
	exported := &exportedHandleV1{}
	if _, err := exporter.Export(exported, window.Surface); err != nil {
		t.Fatal(err)
	}
	app.DispatchUntil("the handle", func() bool { return exported.handle != "" })

	portal := testclient.Connect(t, h.Socket())
	importer, err := cxf1.NewZxdgImporterV1(portal.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	portal.Bind("zxdg_importer_v1", importer)
	parent := &importedStateV1{}
	imported, err := importer.Import(parent, exported.handle)
	if err != nil {
		t.Fatal(err)
	}
	dialog := portal.WindowWith("picker", testclient.Fill(0, 0xff, 0), func(w *testclient.Window) error {
		return imported.SetParentOf(w.Surface)
	})
	portal.DispatchUntil("the dialog to map", dialog.Mapped)
	if dialog.Logical != image.Pt(64, 64) {
		t.Fatalf("the dialog was sized %v; a dialog with a parent floats at its own size", dialog.Logical)
	}
	h.Eventually("the dialog centred over the app on the right", showing(120, 50, [3]byte{0, 0xff, 0}))
	if parent.destroyed {
		t.Fatal("a live import was destroyed")
	}
}

func TestExportingASurfaceWithoutAToplevelIsAnError(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	foreign := bindForeignV2(t, c)
	surface, err := c.Compositor.CreateSurface(struct{ cw.WlSurfaceHandler }{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.exporter.ExportToplevel(&exportedHandle{}, surface); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(cxf2.ZxdgExporterV2ErrorInvalidSurface))
}
