package harness

import (
	"errors"
	"io"
	"os"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	cedc "github.com/stubbedev/tilde/internal/protocol/client/extdatacontrol"
	cwdc "github.com/stubbedev/tilde/internal/protocol/client/wlrdatacontrol"
	"github.com/stubbedev/tilde/internal/testclient"
)

type controlOffer struct{ mimes []string }

type extManager struct {
	c         *testclient.Client
	mgr       *cedc.ExtDataControlManagerV1
	dev       *cedc.ExtDataControlDeviceV1
	offers    map[*cedc.ExtDataControlOfferV1]*controlOffer
	selection *cedc.ExtDataControlOfferV1
	primary   *cedc.ExtDataControlOfferV1
	events    int
}

func (m *extManager) Offer(o *cedc.ExtDataControlOfferV1, mime string) error {
	m.offers[o].mimes = append(m.offers[o].mimes, mime)
	return nil
}

func (m *extManager) DataOffer(_ *cedc.ExtDataControlDeviceV1, o *cedc.ExtDataControlOfferV1) (cedc.ExtDataControlOfferV1Handler, error) {
	m.offers[o] = &controlOffer{}
	return m, nil
}

func (m *extManager) Selection(_ *cedc.ExtDataControlDeviceV1, o *cedc.ExtDataControlOfferV1) error {
	m.selection = o
	m.events++
	return nil
}

func (m *extManager) PrimarySelection(_ *cedc.ExtDataControlDeviceV1, o *cedc.ExtDataControlOfferV1) error {
	m.primary = o
	m.events++
	return nil
}

func (m *extManager) Finished(*cedc.ExtDataControlDeviceV1) error { return nil }

type extContent struct {
	text string
	sent int
}

func (s *extContent) Send(_ *cedc.ExtDataControlSourceV1, _ string, fd int) error {
	f := os.NewFile(uintptr(fd), "send")
	_, err := io.WriteString(f, s.text)
	s.sent++
	return errors.Join(err, f.Close())
}

func (s *extContent) Cancelled(*cedc.ExtDataControlSourceV1) error { return nil }

func clipboardManager(t *testing.T, h *Harness) *extManager {
	t.Helper()
	m := &extManager{c: testclient.Connect(t, h.Socket()), offers: map[*cedc.ExtDataControlOfferV1]*controlOffer{}}
	seat, _ := m.c.Seat()
	var err error
	if m.mgr, err = cedc.NewExtDataControlManagerV1(m.c.Display, 1); err != nil {
		t.Fatal(err)
	}
	m.c.Bind("ext_data_control_manager_v1", m.mgr)
	if m.dev, err = m.mgr.GetDataDevice(m, seat); err != nil {
		t.Fatal(err)
	}
	if err := m.c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDataControlWatchesAndSetsTheClipboard(t *testing.T) {
	h := Start(t, small)
	m := clipboardManager(t, h)
	if m.events != 2 || m.selection != nil || m.primary != nil {
		t.Fatalf("an empty session announced selection %v primary %v in %d events", m.selection, m.primary, m.events)
	}
	a := join(t, h, "a")
	a.seen(t, "a focused", func() bool { return slices.Contains(a.in.Log, "kb_enter a") })
	copied := a.c.DataSource(map[string]string{"text/plain": "copied", "text/html": "<i>copied</i>"})
	if err := errors.Join(a.cb.Device.SetSelection(copied.Data, a.in.Serial), a.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	m.c.DispatchUntil("the copy seen by the manager", func() bool { return m.selection != nil })
	if got := m.offers[m.selection].mimes; !slices.Equal(got, []string{"text/html", "text/plain"}) {
		t.Fatalf("offered %v", got)
	}
	offer := m.selection
	if got := m.c.Receive(func(fd int) error { return offer.Receive("text/plain", fd) }, a.c); got != "copied" {
		t.Fatalf("the manager read %q", got)
	}

	content := &extContent{text: "managed"}
	src, err := m.mgr.CreateDataSource(content)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(src.Offer("text/plain"), m.dev.SetSelection(src), m.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the managed selection offered to a", func() bool {
		return a.cb.Selection != nil && slices.Equal(a.cb.Selection.Mimes, []string{"text/plain"})
	})
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	r := os.NewFile(uintptr(fds[0]), "paste")
	defer r.Close()
	if err := errors.Join(a.cb.Selection.Data.Receive("text/plain", fds[1]), unix.Close(fds[1]), a.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	m.c.DispatchUntil("the manager asked for its text", func() bool { return content.sent > 0 })
	if text, err := io.ReadAll(r); err != nil || string(text) != "managed" {
		t.Fatalf("a pasted %q, %v", text, err)
	}
}

type wlrManager struct {
	primary *cwdc.ZwlrDataControlOfferV1
	mimes   []string
}

func (w *wlrManager) Offer(_ *cwdc.ZwlrDataControlOfferV1, mime string) error {
	w.mimes = append(w.mimes, mime)
	return nil
}

func (w *wlrManager) DataOffer(*cwdc.ZwlrDataControlDeviceV1, *cwdc.ZwlrDataControlOfferV1) (cwdc.ZwlrDataControlOfferV1Handler, error) {
	return w, nil
}

func (w *wlrManager) Selection(*cwdc.ZwlrDataControlDeviceV1, *cwdc.ZwlrDataControlOfferV1) error {
	return nil
}

func (w *wlrManager) PrimarySelection(_ *cwdc.ZwlrDataControlDeviceV1, o *cwdc.ZwlrDataControlOfferV1) error {
	w.primary = o
	return nil
}

func (w *wlrManager) Finished(*cwdc.ZwlrDataControlDeviceV1) error { return nil }

func TestWlrDataControlSeesThePrimarySelection(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	seat, _ := c.Seat()
	mgr, err := cwdc.NewZwlrDataControlManagerV1(c.Display, 2)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("zwlr_data_control_manager_v1", mgr)
	w := &wlrManager{}
	if _, err := mgr.GetDataDevice(w, seat); err != nil {
		t.Fatal(err)
	}
	a := join(t, h, "a")
	a.seen(t, "a focused", func() bool { return slices.Contains(a.in.Log, "kb_enter a") })
	selected := a.c.PrimarySource(map[string]string{"text/plain;charset=utf-8": "middle"})
	if err := errors.Join(a.cb.Primaries.SetSelection(selected.Primary, a.in.Serial), a.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the primary selection seen", func() bool { return w.primary != nil })
	if !slices.Equal(w.mimes, []string{"text/plain;charset=utf-8"}) {
		t.Fatalf("offered %v", w.mimes)
	}
	offer := w.primary
	if got := c.Receive(func(fd int) error { return offer.Receive("text/plain;charset=utf-8", fd) }, a.c); got != "middle" {
		t.Fatalf("read %q", got)
	}
}

type wlrContent struct{}

func (wlrContent) Send(_ *cwdc.ZwlrDataControlSourceV1, _ string, fd int) error {
	return unix.Close(fd)
}

func (wlrContent) Cancelled(*cwdc.ZwlrDataControlSourceV1) error { return nil }

func TestDataControlSourcesAreUsedOnce(t *testing.T) {
	type rig struct {
		c    *testclient.Client
		code uint32
	}
	ext := func(t *testing.T, h *Harness, reuse bool) rig {
		m := clipboardManager(t, h)
		src, err := m.mgr.CreateDataSource(&extContent{text: "x"})
		if err != nil {
			t.Fatal(err)
		}
		if reuse {
			if err := errors.Join(src.Offer("text/plain"), m.dev.SetSelection(src), m.dev.SetSelection(src)); err != nil {
				t.Fatal(err)
			}
			return rig{m.c, uint32(cedc.ExtDataControlDeviceV1ErrorUsedSource)}
		}
		if err := errors.Join(m.dev.SetSelection(src), src.Offer("text/plain")); err != nil {
			t.Fatal(err)
		}
		return rig{m.c, uint32(cedc.ExtDataControlSourceV1ErrorInvalidOffer)}
	}
	wlr := func(t *testing.T, h *Harness, reuse bool) rig {
		c := testclient.Connect(t, h.Socket())
		seat, _ := c.Seat()
		mgr, err := cwdc.NewZwlrDataControlManagerV1(c.Display, 2)
		if err != nil {
			t.Fatal(err)
		}
		c.Bind("zwlr_data_control_manager_v1", mgr)
		dev, err := mgr.GetDataDevice(&wlrManager{}, seat)
		if err != nil {
			t.Fatal(err)
		}
		src, err := mgr.CreateDataSource(wlrContent{})
		if err != nil {
			t.Fatal(err)
		}
		if reuse {
			if err := errors.Join(src.Offer("text/plain"), dev.SetPrimarySelection(src), dev.SetSelection(src)); err != nil {
				t.Fatal(err)
			}
			return rig{c, uint32(cwdc.ZwlrDataControlDeviceV1ErrorUsedSource)}
		}
		if err := errors.Join(dev.SetSelection(src), src.Offer("text/plain")); err != nil {
			t.Fatal(err)
		}
		return rig{c, uint32(cwdc.ZwlrDataControlSourceV1ErrorInvalidOffer)}
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, h *Harness, reuse bool) rig
		reuse bool
	}{
		{"ext source set twice", ext, true},
		{"ext offer after use", ext, false},
		{"wlr source set twice", wlr, true},
		{"wlr offer after use", wlr, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.setup(t, Start(t, small), tc.reuse)
			r.c.ExpectProtocolError(r.code)
		})
	}
}
