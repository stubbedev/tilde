package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cxdd "github.com/stubbedev/lys-wm/internal/protocol/client/xdgdialog"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type modalScene struct {
	h      *Harness
	c      *testclient.Client
	app    *testclient.Window
	dialog *testclient.Window
	hint   *cxdd.XdgDialogV1
}

func dialogManager(t *testing.T, c *testclient.Client) *cxdd.XdgWmDialogV1 {
	t.Helper()
	m, err := cxdd.NewXdgWmDialogV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	return m
}

func modal(t *testing.T, setModal bool) *modalScene {
	t.Helper()
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m := dialogManager(t, c)
	app := c.Window("app", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the app to map", app.Mapped)
	s := &modalScene{h: h, c: c, app: app}
	s.dialog = c.WindowWith("dialog", testclient.Fill(0, 0xff, 0), func(w *testclient.Window) error {
		if err := w.Toplevel.SetParent(app.Toplevel); err != nil {
			return err
		}
		hint, err := m.GetXdgDialog(w.Toplevel)
		if err != nil {
			return err
		}
		s.hint = hint
		if setModal {
			return hint.SetModal()
		}
		return nil
	})
	c.DispatchUntil("the dialog to map", s.dialog.Mapped)
	return s
}

func (s *modalScene) clickApp() {
	s.h.click(0, 5, 5, btnLeft)
}

func (s *modalScene) expectFocus(what string, w *testclient.Window) {
	s.h.t.Helper()
	s.h.Eventually(what, func(_ *headless.Backend, c *compositor.Compositor) bool {
		f := c.Seat().Focus()
		return f != nil && f.Resource().Base().ID() == w.Surface.Base().ID()
	})
}

func TestFocusingTheParentOfAModalDialogFocusesTheDialog(t *testing.T) {
	s := modal(t, true)
	s.clickApp()
	s.expectFocus("the modal dialog keeps the keyboard", s.dialog)

	if err := errors.Join(s.hint.UnsetModal(), s.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	s.clickApp()
	s.expectFocus("the app takes the keyboard once the dialog is no longer modal", s.app)

	if err := errors.Join(s.hint.SetModal(), s.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	s.expectFocus("modal again, the dialog takes the keyboard back", s.dialog)
	if err := errors.Join(s.hint.Destroy(), s.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	s.clickApp()
	s.expectFocus("destroying the hint drops the modality", s.app)
}

func TestAPlainDialogDoesNotHoldTheFocus(t *testing.T) {
	s := modal(t, false)
	s.clickApp()
	s.expectFocus("the app takes the keyboard from a non-modal dialog", s.app)
}

func TestADialogObjectCanBeMadeAgainAfterTheFirstIsGone(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m := dialogManager(t, c)
	w := c.Window("app", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	first, err := m.GetXdgDialog(w.Toplevel)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Destroy(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetXdgDialog(w.Toplevel); err != nil {
		t.Fatal(err)
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatalf("an xdg_dialog_v1 after the first was destroyed: %v", err)
	}
}

func TestASecondDialogObjectIsAnError(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m := dialogManager(t, c)
	w := c.Window("app", testclient.Fill(0xff, 0, 0))
	c.DispatchUntil("the window to map", w.Mapped)
	_, err1 := m.GetXdgDialog(w.Toplevel)
	_, err2 := m.GetXdgDialog(w.Toplevel)
	if err := errors.Join(err1, err2); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(cxdd.XdgWmDialogV1ErrorAlreadyUsed))
}
