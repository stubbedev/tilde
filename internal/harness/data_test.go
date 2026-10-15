package harness

import (
	"errors"
	"image"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cwlr "github.com/stubbedev/tilde/internal/protocol/client/wlrlayershell"
	"github.com/stubbedev/tilde/internal/testclient"
)

type party struct {
	c    *testclient.Client
	seat *cw.WlSeat
	in   *testclient.Input
	cb   *testclient.Clipboard
	w    *testclient.Window
}

func join(t *testing.T, h *Harness, name string) *party {
	t.Helper()
	p := &party{c: testclient.Connect(t, h.Socket())}
	p.seat, p.in = p.c.Seat()
	p.cb = p.c.Clipboard(p.seat)
	p.w = p.c.Window(name, quadrants)
	p.in.Name(p.w.Surface, name)
	p.c.DispatchUntil(name+" to map", p.w.Mapped)
	return p
}

func (p *party) seen(t *testing.T, what string, cond func() bool) {
	t.Helper()
	p.c.DispatchUntil(what, cond)
}

func TestClipboardFollowsKeyboardFocus(t *testing.T) {
	h := Start(t, small)
	a := join(t, h, "a")
	a.seen(t, "a focused", func() bool { return slices.Contains(a.in.Log, "kb_enter a") })
	copied := a.c.DataSource(map[string]string{"text/plain": "hello", "text/html": "<b>hello</b>"})
	if err := a.cb.Device.SetSelection(copied.Data, a.in.Serial); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "a's own selection", func() bool { return a.cb.Selection != nil })

	b := join(t, h, "b")
	b.seen(t, "the selection offered to b", func() bool { return b.cb.Selection != nil })
	if want := []string{"text/html", "text/plain"}; !slices.Equal(b.cb.Selection.Mimes, want) {
		t.Fatalf("offered %v, want %v", b.cb.Selection.Mimes, want)
	}
	if i, j := slices.Index(b.in.Log, "kb_enter b"), slices.Index(b.cb.Log, "selection true"); i < 0 || j < 0 {
		t.Fatalf("b keyboard %v, data %v", b.in.Log, b.cb.Log)
	}
	got := b.c.Receive(func(fd int) error { return b.cb.Selection.Data.Receive("text/plain", fd) }, a.c)
	if got != "hello" {
		t.Fatalf("pasted %q, want hello", got)
	}

	late := a.c.DataSource(map[string]string{"text/plain": "sneaky"})
	if err := a.cb.Device.SetSelection(late.Data, a.in.Serial); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the unfocused copy refused", func() bool { return slices.Contains(late.Log, "cancelled") })

	replacement := b.c.DataSource(map[string]string{"text/plain": "world"})
	if err := errors.Join(b.cb.Device.SetSelection(replacement.Data, b.in.Serial), b.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the replaced source cancelled", func() bool { return slices.Contains(copied.Log, "cancelled") })
	if err := errors.Join(replacement.Data.Destroy(), b.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	b.seen(t, "the selection cleared with its source", func() bool { return b.cb.Log[len(b.cb.Log)-1] == "selection false" })
}

func TestPrimarySelection(t *testing.T) {
	h := Start(t, small)
	a := join(t, h, "a")
	a.seen(t, "a focused", func() bool { return slices.Contains(a.in.Log, "kb_enter a") })
	selected := a.c.PrimarySource(map[string]string{"text/plain;charset=utf-8": "middle"})
	if err := errors.Join(a.cb.Primaries.SetSelection(selected.Primary, a.in.Serial), a.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	b := join(t, h, "b")
	b.seen(t, "the primary selection offered to b", func() bool { return b.cb.Primary != nil })
	got := b.c.Receive(func(fd int) error { return b.cb.Primary.Primary.Receive("text/plain;charset=utf-8", fd) }, a.c)
	if got != "middle" {
		t.Fatalf("pasted %q, want middle", got)
	}
	if b.cb.Selection != nil {
		t.Fatal("the primary selection leaked into the clipboard")
	}
}

func dropZone(t *testing.T, h *Harness) *party {
	t.Helper()
	p := &party{c: testclient.Connect(t, h.Socket())}
	p.seat, p.in = p.c.Seat()
	p.cb = p.c.Clipboard(p.seat)
	p.w = p.c.Layer(testclient.LayerSpec{
		Layer:  cwlr.ZwlrLayerShellV1LayerTop,
		Anchor: cwlr.ZwlrLayerSurfaceV1AnchorTop | cwlr.ZwlrLayerSurfaceV1AnchorRight,
		Size:   image.Pt(40, 40),
	}, testclient.Fill(barColor[2], barColor[1], barColor[0]))
	p.in.Name(p.w.Surface, "zone")
	p.c.DispatchUntil("the drop zone to map", p.w.Mapped)
	return p
}

func TestDragAndDropBetweenClients(t *testing.T) {
	h := Start(t, small)
	a := join(t, h, "a")
	zone := dropZone(t, h)
	r := &inputRig{t: t, h: h, c: a.c, in: a.in, w: a.w}
	r.moveOn(0, 20, 50)
	a.seen(t, "the pointer over a", func() bool { return slices.Contains(a.in.Log, "motion 20,50") })
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(272, true), b.Pointer().Frame())
	})
	a.seen(t, "the press", func() bool { return a.in.Press != 0 })
	dragged := a.c.DataSource(map[string]string{"text/uri-list": "file:///tmp/x"})
	icon := a.c.Surface()
	if err := errors.Join(
		dragged.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy|cw.WlDataDeviceManagerDndActionMove),
		a.cb.Device.StartDrag(dragged.Data, a.w.Surface, icon, a.in.Press),
		icon.Attach(a.c.Buffer(image.Pt(6, 6), 0, testclient.Fill(0x80, 0x80, 0x80)), 0, 0),
		icon.Commit(),
	); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the origin loses the pointer", func() bool { return a.in.Log[len(a.in.Log)-2] == "leave a" })
	h.Eventually("the drag icon at the pointer", showing(22, 52, gray))

	r.moveOn(0, 140, 10)
	zone.seen(t, "the drag entering the zone", func() bool { return zone.cb.Drag != nil })
	if o := zone.cb.Drag; !slices.Equal(o.Mimes, []string{"text/uri-list"}) || o.SourceActions != cw.WlDataDeviceManagerDndActionCopy|cw.WlDataDeviceManagerDndActionMove {
		t.Fatalf("drag offer %+v", o)
	}
	mime := "text/uri-list"
	if err := errors.Join(
		zone.cb.Drag.Data.Accept(zone.cb.Serial, &mime),
		zone.cb.Drag.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy|cw.WlDataDeviceManagerDndActionMove, cw.WlDataDeviceManagerDndActionMove),
		zone.c.Display.Flush(),
	); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the source told the target and action", func() bool {
		return slices.Contains(dragged.Log, "target text/uri-list") && slices.Contains(dragged.Log, "action 2")
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(272, false), b.Pointer().Frame())
	})
	zone.seen(t, "the drop", func() bool { return slices.Contains(zone.cb.Log, "drop") })
	if zone.cb.Log[len(zone.cb.Log)-1] != "leave" {
		zone.seen(t, "the leave after the drop", func() bool { return zone.cb.Log[len(zone.cb.Log)-1] == "leave" })
	}
	got := zone.c.Receive(func(fd int) error { return zone.cb.Drag.Data.Receive(mime, fd) }, a.c)
	if got != "file:///tmp/x" {
		t.Fatalf("dropped %q", got)
	}
	if err := errors.Join(zone.cb.Drag.Data.Finish(), zone.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the source finished", func() bool { return slices.Contains(dragged.Log, "finished") })
	if i, j := slices.Index(dragged.Log, "dropped"), slices.Index(dragged.Log, "finished"); i < 0 || i > j {
		t.Fatalf("source events %v", dragged.Log)
	}
	zone.seen(t, "the pointer back on the zone", func() bool { return slices.Contains(zone.in.Log, "enter zone 20,10") })
}

func TestDragAndDropStartedByTouch(t *testing.T) {
	h := Start(t, small)
	a := join(t, h, "a")
	zone := dropZone(t, h)
	touch := func(f func(t *headless.Touch, o *headless.Output) error) {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return errors.Join(f(b.Touch(), b.Outputs()[0]), b.Touch().Frame())
		})
	}
	touch(func(t *headless.Touch, o *headless.Output) error { return t.Down(3, o, 20.0/160, 50.0/100) })
	a.seen(t, "the touch down on a", func() bool { return slices.Contains(a.in.Log, "down 3 a 20,50") })
	dragged := a.c.DataSource(map[string]string{"text/uri-list": "file:///tmp/t"})
	icon := a.c.Surface()
	if err := errors.Join(
		dragged.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy),
		a.cb.Device.StartDrag(dragged.Data, a.w.Surface, icon, a.in.Press),
		icon.Attach(a.c.Buffer(image.Pt(6, 6), 0, testclient.Fill(0x80, 0x80, 0x80)), 0, 0),
		icon.Commit(),
	); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the drag entering a", func() bool { return a.cb.Drag != nil })
	h.Eventually("the drag icon at the touch point", showing(22, 52, gray))

	touch(func(t *headless.Touch, _ *headless.Output) error { return t.Motion(3, 140.0/160, 10.0/100) })
	zone.seen(t, "the drag entering the zone", func() bool { return zone.cb.Drag != nil })
	h.Eventually("the drag icon following the touch point", showing(142, 12, gray))
	mime := "text/uri-list"
	if err := errors.Join(
		zone.cb.Drag.Data.Accept(zone.cb.Serial, &mime),
		zone.cb.Drag.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy, cw.WlDataDeviceManagerDndActionCopy),
		zone.c.Display.Flush(),
	); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the source told the target", func() bool { return slices.Contains(dragged.Log, "target text/uri-list") })
	touch(func(t *headless.Touch, _ *headless.Output) error { return t.Up(3) })
	zone.seen(t, "the drop", func() bool { return slices.Contains(zone.cb.Log, "drop") })
	got := zone.c.Receive(func(fd int) error { return zone.cb.Drag.Data.Receive(mime, fd) }, a.c)
	if got != "file:///tmp/t" {
		t.Fatalf("dropped %q", got)
	}
	if slices.ContainsFunc(a.in.Log, func(e string) bool { return strings.HasPrefix(e, "touch_motion 3") || e == "up 3" }) {
		t.Fatalf("the origin saw the touch point after the drag took it: %v", a.in.Log)
	}

	touch(func(t *headless.Touch, o *headless.Output) error { return t.Down(4, o, 20.0/160, 50.0/100) })
	a.seen(t, "a second touch down on a", func() bool { return slices.Contains(a.in.Log, "down 4 a 20,50") })
	stale := a.c.DataSource(map[string]string{"text/plain": "x"})
	if err := a.cb.Device.StartDrag(stale.Data, a.w.Surface, nil, a.in.Press-1); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "a drag with another serial refused", func() bool { return slices.Contains(stale.Log, "cancelled") })
}

func TestACancelledTouchCancelsItsDrag(t *testing.T) {
	h := Start(t, small)
	a := join(t, h, "a")
	touch := func(f func(t *headless.Touch, o *headless.Output) error) {
		h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
			return errors.Join(f(b.Touch(), b.Outputs()[0]), b.Touch().Frame())
		})
	}
	touch(func(t *headless.Touch, o *headless.Output) error { return t.Down(3, o, 20.0/160, 50.0/100) })
	a.seen(t, "the touch down on a", func() bool { return slices.Contains(a.in.Log, "down 3 a 20,50") })
	dragged := a.c.DataSource(map[string]string{"text/plain": "x"})
	if err := a.cb.Device.StartDrag(dragged.Data, a.w.Surface, nil, a.in.Press); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the drag entering a", func() bool { return a.cb.Drag != nil })
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(272, true), b.Pointer().Frame(), b.Pointer().Button(272, false), b.Pointer().Frame())
	})
	a.c.Sync()
	if slices.Contains(dragged.Log, "cancelled") || slices.Contains(dragged.Log, "dropped") {
		t.Fatalf("a mouse click ended a touch drag: %v", dragged.Log)
	}
	touch(func(t *headless.Touch, _ *headless.Output) error { return t.Cancel(3) })
	a.seen(t, "the drag cancelled", func() bool { return slices.Contains(dragged.Log, "cancelled") })
}

func TestDragWithoutATakerIsCancelled(t *testing.T) {
	h := Start(t, small)
	a := join(t, h, "a")
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(272, true), b.Pointer().Frame())
	})
	a.seen(t, "the press", func() bool { return a.in.Press != 0 })
	dragged := a.c.DataSource(map[string]string{"text/plain": "x"})
	if err := a.cb.Device.StartDrag(dragged.Data, a.w.Surface, nil, a.in.Press); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "the drag entering a", func() bool { return a.cb.Drag != nil })
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return errors.Join(b.Pointer().Button(272, false), b.Pointer().Frame())
	})
	a.seen(t, "the drag cancelled", func() bool { return slices.Contains(dragged.Log, "cancelled") })
	unheld := a.c.DataSource(map[string]string{"text/plain": "y"})
	if err := a.cb.Device.StartDrag(unheld.Data, a.w.Surface, nil, a.in.Press); err != nil {
		t.Fatal(err)
	}
	a.seen(t, "a drag without a held button refused", func() bool { return slices.Contains(unheld.Log, "cancelled") })
}

func TestDataDeviceProtocolErrors(t *testing.T) {
	cases := []struct {
		name  string
		press bool
		do    func(p *party) error
		code  uint32
	}{
		{"bad source actions", false, func(p *party) error {
			return p.c.DataSource(map[string]string{"a/b": "x"}).Data.SetActions(8)
		}, uint32(cw.WlDataSourceErrorInvalidActionMask)},
		{"actions set twice", false, func(p *party) error {
			s := p.c.DataSource(map[string]string{"a/b": "x"})
			return errors.Join(s.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy), s.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy))
		}, uint32(cw.WlDataSourceErrorInvalidSource)},
		{"drag source as the selection", false, func(p *party) error {
			s := p.c.DataSource(map[string]string{"a/b": "x"})
			return errors.Join(s.Data.SetActions(cw.WlDataDeviceManagerDndActionCopy), p.cb.Device.SetSelection(s.Data, 0))
		}, uint32(cw.WlDataSourceErrorInvalidSource)},
		{"source used twice for a drag", false, func(p *party) error {
			s := p.c.DataSource(map[string]string{"a/b": "x"})
			return errors.Join(p.cb.Device.SetSelection(s.Data, 0), p.cb.Device.StartDrag(s.Data, p.w.Surface, nil, 0))
		}, uint32(cw.WlDataDeviceErrorUsedSource)},
		{"finish on a selection offer", false, func(p *party) error {
			s := p.c.DataSource(map[string]string{"a/b": "x"})
			if err := errors.Join(p.cb.Device.SetSelection(s.Data, 0), p.c.Display.Flush()); err != nil {
				return err
			}
			p.c.DispatchUntil("the selection", func() bool { return p.cb.Selection != nil })
			return p.cb.Selection.Data.Finish()
		}, uint32(cw.WlDataOfferErrorInvalidFinish)},
		{"icon with another role", true, func(p *party) error {
			return p.cb.Device.StartDrag(nil, p.w.Surface, p.w.Surface, 0)
		}, uint32(cw.WlDataDeviceErrorRole)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Start(t, small)
			p := join(t, h, "a")
			p.seen(t, "focus", func() bool { return slices.Contains(p.in.Log, "kb_enter a") })
			if tc.press {
				h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
					return errors.Join(b.Pointer().Button(272, true), b.Pointer().Frame())
				})
				p.seen(t, "the press", func() bool { return p.in.Press != 0 })
			}
			if err := tc.do(p); err != nil {
				t.Fatal(err)
			}
			p.c.ExpectProtocolError(tc.code)
		})
	}
}
