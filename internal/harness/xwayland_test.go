package harness

import (
	"encoding/binary"
	"errors"
	"image"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/ipc"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/wm"
)

func xConnect(t *testing.T, h *Harness) {
	t.Helper()
	path := filepath.Join(h.RuntimeDir, ".X11-unix", "X"+strings.TrimPrefix(h.xwayland.Display(), ":"))
	conn, err := net.DialTimeout("unix", path, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(clientTimeout)); err != nil {
		t.Fatal(err)
	}
	setup := binary.LittleEndian.AppendUint16([]byte{'l', 0}, 11)
	setup = append(setup, make([]byte, 8)...)
	if _, err := conn.Write(setup); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 8)
	if _, err := conn.Read(reply); err != nil {
		t.Fatalf("no X connection setup reply: %v", err)
	}
	if reply[0] != 1 {
		t.Fatalf("X connection setup failed: %v", reply)
	}
}

func TestXwaylandStartsOnTheFirstXClientAndAfterACrash(t *testing.T) {
	reports := make(chan error, 4)
	h := StartXwayland(t, func(err error) { reports <- err }, small)
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		if h.xwayland.Running() || h.xwayland.Pid() != 0 {
			t.Error("Xwayland runs before any X client connected")
		}
		return nil
	})
	xConnect(t, h)
	h.Eventually("Xwayland ready", func(*headless.Backend, *compositor.Compositor) bool { return h.xwayland.Running() })
	if !strings.Contains(h.Trace(), "xwayland_shell_v1") {
		t.Fatal("Xwayland never bound xwayland_shell_v1")
	}
	var pid int
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		pid = h.xwayland.Pid()
		return syscall.Kill(pid, syscall.SIGKILL)
	})
	select {
	case err := <-reports:
		if !strings.Contains(err.Error(), "signal: killed") {
			t.Fatalf("reported %v", err)
		}
	case <-time.After(clientTimeout):
		t.Fatal("the crash was not reported")
	}
	xConnect(t, h)
	h.Eventually("Xwayland restarted", func(*headless.Backend, *compositor.Compositor) bool {
		return h.xwayland.Running() && h.xwayland.Pid() != pid
	})
}

type xclient struct {
	t     *testing.T
	x     *xgb.Conn
	root  xproto.Window
	atoms map[string]xproto.Atom
}

func xDial(t *testing.T, h *Harness) *xclient {
	t.Helper()
	path := filepath.Join(h.RuntimeDir, ".X11-unix", "X"+strings.TrimPrefix(h.xwayland.Display(), ":"))
	conn, err := net.DialTimeout("unix", path, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	x, err := xgb.NewConnNet(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Close)
	c := &xclient{t: t, x: x, root: xproto.Setup(x).DefaultScreen(x).Root, atoms: map[string]xproto.Atom{}}
	for _, name := range []string{"WM_CLASS", "_NET_WM_NAME", "UTF8_STRING", "_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_DIALOG", "_NET_WM_STATE", "_NET_WM_STATE_FULLSCREEN", "_NET_WM_STATE_MODAL", "WM_TRANSIENT_FOR"} {
		r, err := xproto.InternAtom(x, false, uint16(len(name)), name).Reply()
		if err != nil {
			t.Fatal(err)
		}
		c.atoms[name] = r.Atom
	}
	return c
}

func (c *xclient) window(rect image.Rectangle, pixel uint32, override bool, props map[string][]byte) xproto.Window {
	c.t.Helper()
	win, err := xproto.NewWindowId(c.x)
	if err != nil {
		c.t.Fatal(err)
	}
	var or uint32
	if override {
		or = 1
	}
	screen := xproto.Setup(c.x).DefaultScreen(c.x)
	checks := []interface{ Check() error }{
		xproto.CreateWindowChecked(c.x, screen.RootDepth, win, c.root, int16(rect.Min.X), int16(rect.Min.Y), uint16(rect.Dx()), uint16(rect.Dy()), 0,
			xproto.WindowClassInputOutput, screen.RootVisual, xproto.CwBackPixel|xproto.CwOverrideRedirect, []uint32{pixel, or}),
	}
	for name, value := range props {
		typ, format := xproto.Atom(xproto.AtomString), byte(8)
		switch name {
		case "_NET_WM_NAME":
			typ = c.atoms["UTF8_STRING"]
		case "_NET_WM_WINDOW_TYPE", "_NET_WM_STATE", "WM_PROTOCOLS":
			typ, format = xproto.Atom(xproto.AtomAtom), 32
		case "WM_TRANSIENT_FOR":
			typ, format = xproto.Atom(xproto.AtomWindow), 32
		}
		checks = append(checks, xproto.ChangePropertyChecked(c.x, xproto.PropModeReplace, win, c.atoms[name], typ, format, uint32(len(value))/uint32(format/8), value))
	}
	checks = append(checks, xproto.MapWindowChecked(c.x, win))
	for _, ck := range checks {
		if err := ck.Check(); err != nil {
			c.t.Fatal(err)
		}
	}
	return win
}

func (c *xclient) atom(name string) []byte {
	return binary.LittleEndian.AppendUint32(nil, uint32(c.atoms[name]))
}

func (c *xclient) geometry(win xproto.Window) image.Rectangle {
	c.t.Helper()
	g, err := xproto.GetGeometry(c.x, xproto.Drawable(win)).Reply()
	if err != nil {
		c.t.Fatal(err)
	}
	return rectOfGeometry(g)
}

func rectOfGeometry(g *xproto.GetGeometryReply) image.Rectangle {
	return image.Rect(int(g.X), int(g.Y), int(g.X)+int(g.Width), int(g.Y)+int(g.Height))
}

func windowsOf(c *compositor.Compositor) []ipc.Window {
	return c.Snapshot().Windows
}

func TestXWindowsAreManaged(t *testing.T) {
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	xc := xDial(t, h)
	main := xc.window(image.Rect(10, 10, 90, 70), 0xff0000, false, map[string][]byte{
		"WM_CLASS":     []byte("xtest\x00XTest\x00"),
		"_NET_WM_NAME": []byte("x window"),
	})
	h.Eventually("the X window tiled", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && ws[0].AppID == "XTest" && ws[0].Title == "x window" && ws[0].Focused && !ws[0].Floating
	})
	h.Eventually("the X window filling the output", func(*headless.Backend, *compositor.Compositor) bool {
		return xc.geometry(main) == image.Rect(0, 0, 160, 100)
	})
	h.Eventually("the X window drawn", showing(150, 90, [3]byte{0xff, 0, 0}))
	focus, err := xproto.GetInputFocus(xc.x).Reply()
	if err != nil || focus.Focus != main {
		t.Fatalf("X focus on %#x (%v), want %#x", focus.Focus, err, main)
	}

	xc.window(image.Rect(0, 0, 40, 30), 0x0000ff, false, map[string][]byte{
		"WM_CLASS":            []byte("dialog\x00XTest\x00"),
		"_NET_WM_WINDOW_TYPE": xc.atom("_NET_WM_WINDOW_TYPE_DIALOG"),
	})
	h.Eventually("the dialog floating in the middle", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 2 && ws[1].Floating && ws[1].Rect == ipc.Rect{X: 60, Y: 35, Width: 40, Height: 30} && ws[1].Focused
	})
	h.Eventually("the dialog drawn over the window", showing(80, 50, [3]byte{0, 0, 0xff}))

	xc.window(image.Rect(4, 4, 24, 14), 0x00ff00, true, nil)
	h.Eventually("the override-redirect window where it asked to be", showing(10, 8, [3]byte{0, 0xff, 0}))
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if n := len(windowsOf(c)); n != 2 {
			t.Errorf("%d managed windows after an override-redirect map", n)
		}
		return nil
	})

	full := xproto.ClientMessageEvent{
		Format: 32, Window: main, Type: xc.atoms["_NET_WM_STATE"],
		Data: xproto.ClientMessageDataUnionData32New([]uint32{1, uint32(xc.atoms["_NET_WM_STATE_FULLSCREEN"]), 0, 1, 0}),
	}
	if err := xproto.SendEventChecked(xc.x, false, xc.root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(full.Bytes())).Check(); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the X window fullscreen", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 2 && ws[0].Fullscreen
	})

	if err := xproto.DestroyWindowChecked(xc.x, main).Check(); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the destroyed X window gone", func(_ *headless.Backend, c *compositor.Compositor) bool {
		return len(windowsOf(c)) == 1
	})
}

func (c *xclient) clientMessage(timeout time.Duration) xproto.ClientMessageEvent {
	c.t.Helper()
	for deadline := time.Now().Add(timeout); ; {
		ev, open := c.event(time.Until(deadline))
		if !open {
			c.t.Fatal("the X connection closed before a client message")
		}
		if m, ok := ev.(xproto.ClientMessageEvent); ok {
			return m
		}
	}
}

func (c *xclient) event(timeout time.Duration) (xgb.Event, bool) {
	type result struct {
		ev   xgb.Event
		open bool
	}
	got := make(chan result, 1)
	go func() {
		for {
			ev, err := c.x.WaitForEvent()
			if ev == nil && err == nil {
				got <- result{nil, false}
				return
			}
			if ev != nil {
				got <- result{ev, true}
				return
			}
		}
	}()
	select {
	case r := <-got:
		return r.ev, r.open
	case <-time.After(timeout):
		c.t.Fatal("no X event arrived")
		return nil, false
	}
}

func kill(h *Harness) {
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		a, err := wm.ParseAction("kill")
		if err != nil {
			return err
		}
		return c.Dispatch(a)
	})
}

func TestXWindowsCloseTheICCCMWay(t *testing.T) {
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	polite := xDial(t, h)
	names := []string{"WM_PROTOCOLS", "WM_DELETE_WINDOW"}
	for _, name := range names {
		r, err := xproto.InternAtom(polite.x, false, uint16(len(name)), name).Reply()
		if err != nil {
			t.Fatal(err)
		}
		polite.atoms[name] = r.Atom
	}
	polite.window(image.Rect(0, 0, 50, 50), 0xffffff, false, map[string][]byte{"WM_PROTOCOLS": polite.atom("WM_DELETE_WINDOW")})
	h.Eventually("the polite window focused", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && ws[0].Focused
	})
	kill(h)
	msg := polite.clientMessage(clientTimeout)
	if msg.Type != polite.atoms["WM_PROTOCOLS"] || xproto.Atom(msg.Data.Data32[0]) != polite.atoms["WM_DELETE_WINDOW"] {
		t.Fatalf("closing a WM_DELETE_WINDOW window sent %#v", msg)
	}

	rude := xDial(t, h)
	rude.window(image.Rect(0, 0, 50, 50), 0xffffff, false, nil)
	h.Eventually("the rude window focused", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 2 && ws[1].Focused
	})
	kill(h)
	for {
		ev, open := rude.event(clientTimeout)
		if !open {
			return
		}
		if m, ok := ev.(xproto.ClientMessageEvent); ok {
			t.Fatalf("closing a window without WM_DELETE_WINDOW sent %#v instead of killing its client", m)
		}
	}
}

func TestXWindowsLearnTheirGeometryAndFrame(t *testing.T) {
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	xc := xDial(t, h)
	xc.intern("_NET_FRAME_EXTENTS", "_NET_REQUEST_FRAME_EXTENTS", "_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_DIALOG")
	win, err := xproto.NewWindowId(xc.x)
	if err != nil {
		t.Fatal(err)
	}
	screen := xproto.Setup(xc.x).DefaultScreen(xc.x)
	ask := xproto.ClientMessageEvent{Format: 32, Window: win, Type: xc.atoms["_NET_REQUEST_FRAME_EXTENTS"], Data: xproto.ClientMessageDataUnionData32New(make([]uint32, 5))}
	for _, ck := range []interface{ Check() error }{
		xproto.CreateWindowChecked(xc.x, screen.RootDepth, win, xc.root, 60, 35, 40, 30, 0, xproto.WindowClassInputOutput, screen.RootVisual,
			xproto.CwBackPixel|xproto.CwEventMask, []uint32{0xffffff, xproto.EventMaskStructureNotify}),
		xproto.ChangePropertyChecked(xc.x, xproto.PropModeReplace, win, xc.atoms["_NET_WM_WINDOW_TYPE"], xproto.AtomAtom, 32, 1, xc.atom("_NET_WM_WINDOW_TYPE_DIALOG")),
		xproto.SendEventChecked(xc.x, false, xc.root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(ask.Bytes())),
	} {
		if err := ck.Check(); err != nil {
			t.Fatal(err)
		}
	}
	h.Eventually("the requested frame extents", func(*headless.Backend, *compositor.Compositor) bool {
		r, err := xproto.GetProperty(xc.x, false, win, xc.atoms["_NET_FRAME_EXTENTS"], xproto.AtomCardinal, 0, 4).Reply()
		return err == nil && r.ValueLen == 4 && slices.Equal(r.Value, make([]byte, 16))
	})
	if err := xproto.MapWindowChecked(xc.x, win).Check(); err != nil {
		t.Fatal(err)
	}
	for {
		ev, open := xc.event(clientTimeout)
		if !open {
			t.Fatal("the X connection closed")
		}
		n, ok := ev.(xproto.ConfigureNotifyEvent)
		if ok && n.Window == win && n.X == 60 && n.Y == 35 && n.Width == 40 && n.Height == 30 {
			return
		}
	}
}

func TestXWindowsRenderUnscaledOnHiDPIOutputs(t *testing.T) {
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, scaled)
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		return c.SetOutputScale(b.Outputs()[0], 180)
	})
	xc := xDial(t, h)
	h.Eventually("Xwayland's root in physical pixels", func(*headless.Backend, *compositor.Compositor) bool {
		return xc.geometry(xc.root) == image.Rect(0, 0, 240, 150)
	})
	win := xc.window(image.Rect(0, 0, 50, 50), 0xff0000, false, map[string][]byte{"WM_CLASS": []byte("hidpi\x00HiDPI\x00")})
	h.Eventually("the X window tiled in physical pixels", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && ws[0].Rect == ipc.Rect{Width: 160, Height: 100} && xc.geometry(win) == image.Rect(0, 0, 240, 150)
	})
	h.Eventually("the X window filling the output pixel for pixel", func(b *headless.Backend, _ *compositor.Compositor) bool {
		snap := b.Outputs()[0].Primary()
		return pixel(snap, 0, 0) == [3]byte{0xff, 0, 0} && pixel(snap, 239, 149) == [3]byte{0xff, 0, 0}
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().MoveTo(b.Outputs()[0], 0.5, 0.5), b.Pointer().Frame())
	})
	h.Eventually("the X pointer in physical pixels", func(*headless.Backend, *compositor.Compositor) bool {
		p, err := xproto.QueryPointer(xc.x, win).Reply()
		return err == nil && p.WinX == 120 && p.WinY == 75
	})
}

func TestXWindowsMapAcrossMixedScales(t *testing.T) {
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, scaled, small)
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		return c.SetOutputScale(b.Outputs()[0], 180)
	})
	xc := xDial(t, h)
	h.Eventually("both outputs in X space", func(*headless.Backend, *compositor.Compositor) bool {
		return xc.geometry(xc.root) == image.Rect(0, 0, 400, 150)
	})
	left := xc.window(image.Rect(0, 0, 50, 50), 0xff0000, false, nil)
	h.Eventually("the first X window on the HiDPI output", func(*headless.Backend, *compositor.Compositor) bool {
		return xc.geometry(left) == image.Rect(0, 0, 240, 150)
	})
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		a, err := wm.ParseAction("focus right")
		if err != nil {
			return err
		}
		return c.Dispatch(a)
	})
	right := xc.window(image.Rect(0, 0, 50, 50), 0x00ff00, false, nil)
	h.Eventually("the second X window on the plain output, right of the first in X space", func(*headless.Backend, *compositor.Compositor) bool {
		return xc.geometry(right) == image.Rect(240, 0, 400, 100)
	})
	h.Eventually("each window drawn on its own output", func(b *headless.Backend, _ *compositor.Compositor) bool {
		outs := b.Outputs()
		return pixel(outs[0].Primary(), 239, 149) == [3]byte{0xff, 0, 0} && pixel(outs[1].Primary(), 159, 99) == [3]byte{0, 0xff, 0}
	})
}

func TestHungXClientsAreMarkedUnresponsive(t *testing.T) {
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	xc := xDial(t, h)
	xc.intern("WM_PROTOCOLS", "_NET_WM_PING")
	xc.window(image.Rect(0, 0, 50, 50), 0xffffff, false, map[string][]byte{"WM_PROTOCOLS": xc.atom("_NET_WM_PING")})
	h.Eventually("the X window focused", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 1 && ws[0].Focused
	})
	ping := xc.clientMessage(clientTimeout)
	if ping.Type != xc.atoms["WM_PROTOCOLS"] || xproto.Atom(ping.Data.Data32[0]) != xc.atoms["_NET_WM_PING"] {
		t.Fatalf("focusing the window sent %#v, not a _NET_WM_PING", ping)
	}
	time.Sleep(shell.PingTimeout)
	h.Eventually("the silent X window marked unresponsive", unresponsive(1, true))
	ping.Window = xc.root
	if err := xproto.SendEventChecked(xc.x, false, xc.root, xproto.EventMaskSubstructureNotify|xproto.EventMaskSubstructureRedirect, string(ping.Bytes())).Check(); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the answered X window marked responsive", unresponsive(1, false))
}

func TestAnXModalDialogKeepsTheFocus(t *testing.T) {
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	xc := xDial(t, h)
	main := xc.window(image.Rect(10, 10, 90, 70), 0xff0000, false, map[string][]byte{
		"WM_CLASS": []byte("xtest\x00XTest\x00"),
	})
	h.Eventually("the main window tiled", func(_ *headless.Backend, c *compositor.Compositor) bool { return len(windowsOf(c)) == 1 })
	xc.window(image.Rect(0, 0, 40, 30), 0x0000ff, false, map[string][]byte{
		"WM_CLASS":         []byte("dialog\x00XTest\x00"),
		"WM_TRANSIENT_FOR": binary.LittleEndian.AppendUint32(nil, uint32(main)),
		"_NET_WM_STATE":    xc.atom("_NET_WM_STATE_MODAL"),
	})
	dialogFocused := func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := windowsOf(c)
		return len(ws) == 2 && ws[1].Floating && ws[1].Focused
	}
	h.Eventually("the modal dialog focused", dialogFocused)
	h.click(0, 5, 5, btnLeft)
	h.Eventually("the modal dialog still focused after a click on its parent", dialogFocused)
}
