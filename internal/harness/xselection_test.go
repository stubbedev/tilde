package harness

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/data"
)

const xchunk = 16 << 10

func (c *xclient) intern(names ...string) {
	c.t.Helper()
	for _, name := range names {
		r, err := xproto.InternAtom(c.x, false, uint16(len(name)), name).Reply()
		if err != nil {
			c.t.Fatal(err)
		}
		c.atoms[name] = r.Atom
	}
}

func (c *xclient) inputWindow() xproto.Window {
	c.t.Helper()
	win, err := xproto.NewWindowId(c.x)
	if err != nil {
		c.t.Fatal(err)
	}
	screen := xproto.Setup(c.x).DefaultScreen(c.x)
	if err := xproto.CreateWindowChecked(c.x, 0, win, c.root, 0, 0, 1, 1, 0, xproto.WindowClassInputOnly, screen.RootVisual, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		c.t.Fatal(err)
	}
	return win
}

func (c *xclient) next(timeout time.Duration) (xgb.Event, error) {
	type result struct {
		ev  xgb.Event
		err error
	}
	got := make(chan result, 1)
	go func() {
		for {
			ev, xerr := c.x.WaitForEvent()
			switch {
			case ev == nil && xerr == nil:
				got <- result{err: errors.New("the X connection closed")}
				return
			case ev != nil:
				got <- result{ev: ev}
				return
			}
		}
	}()
	select {
	case r := <-got:
		return r.ev, r.err
	case <-time.After(timeout):
		return nil, errors.New("no X event arrived")
	}
}

func (c *xclient) convert(win xproto.Window, selection, target string) (xproto.Atom, []byte, error) {
	prop := c.atoms["XSEL_DATA"]
	if err := xproto.ConvertSelectionChecked(c.x, win, c.atoms[selection], c.atoms[target], prop, xproto.TimeCurrentTime).Check(); err != nil {
		return 0, nil, err
	}
	read := func() (*xproto.GetPropertyReply, error) {
		return xproto.GetProperty(c.x, true, win, prop, xproto.GetPropertyTypeAny, 0, 1<<24).Reply()
	}
	for {
		ev, err := c.next(clientTimeout)
		if err != nil {
			return 0, nil, err
		}
		n, ok := ev.(xproto.SelectionNotifyEvent)
		if !ok {
			continue
		}
		if n.Property == 0 {
			return 0, nil, fmt.Errorf("the paste of %s was refused", target)
		}
		r, err := read()
		if err != nil || r.Type != c.atoms["INCR"] {
			return r.Type, r.Value, err
		}
		var got []byte
		for {
			ev, err := c.next(clientTimeout)
			if err != nil {
				return 0, nil, err
			}
			p, ok := ev.(xproto.PropertyNotifyEvent)
			if !ok || p.Atom != prop || p.State != xproto.PropertyNewValue {
				continue
			}
			chunk, err := read()
			if err != nil || len(chunk.Value) == 0 {
				return chunk.Type, got, err
			}
			got = append(got, chunk.Value...)
		}
	}
}

func (c *xclient) paste(win xproto.Window, selection, target string, dispatch func(done <-chan struct{})) (xproto.Atom, []byte) {
	c.t.Helper()
	type result struct {
		typ   xproto.Atom
		value []byte
		err   error
	}
	got := make(chan result, 1)
	done := make(chan struct{})
	go func() {
		typ, value, err := c.convert(win, selection, target)
		close(done)
		got <- result{typ, value, err}
	}()
	dispatch(done)
	r := <-got
	if r.err != nil {
		c.t.Fatal(r.err)
	}
	return r.typ, r.value
}

func (c *xclient) names(list []byte) []string {
	c.t.Helper()
	var out []string
	for i := 0; i+4 <= len(list); i += 4 {
		r, err := xproto.GetAtomName(c.x, xproto.Atom(binary.LittleEndian.Uint32(list[i:]))).Reply()
		if err != nil {
			c.t.Fatal(err)
		}
		out = append(out, r.Name)
	}
	slices.Sort(out)
	return out
}

func TestWaylandSelectionsPasteIntoX(t *testing.T) {
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	a := join(t, h, "a")
	a.seen(t, "a focused", func() bool { return slices.Contains(a.in.Log, "kb_enter a") })
	big := strings.Repeat("0123456789abcdef", 12<<10)
	for _, text := range []string{"hello from wayland", big} {
		src := a.c.DataSource(map[string]string{"text/plain;charset=utf-8": text, "text/html": "<b>hi</b>"})
		if err := errors.Join(a.cb.Device.SetSelection(src.Data, a.in.Serial), a.c.Display.Roundtrip()); err != nil {
			t.Fatal(err)
		}
		xc := xDial(t, h)
		xc.intern("CLIPBOARD", "TARGETS", "UTF8_STRING", "INCR", "XSEL_DATA")
		h.Eventually("the X clipboard owned for Wayland", func(*headless.Backend, *compositor.Compositor) bool {
			r, err := xproto.GetSelectionOwner(xc.x, xc.atoms["CLIPBOARD"]).Reply()
			return err == nil && r.Owner != 0
		})
		win := xc.inputWindow()
		idle := func(<-chan struct{}) {}
		serve := func(done <-chan struct{}) {
			a.seen(t, "the source asked for the text", func() bool { return slices.Contains(src.Log, "send text/plain;charset=utf-8") })
			<-done
		}
		_, targets := xc.paste(win, "CLIPBOARD", "TARGETS", idle)
		if got := xc.names(targets); !slices.Equal(got, []string{"TARGETS", "UTF8_STRING", "text/html"}) {
			t.Fatalf("targets %v", got)
		}
		typ, got := xc.paste(win, "CLIPBOARD", "UTF8_STRING", serve)
		if typ != xc.atoms["UTF8_STRING"] || string(got) != text {
			t.Fatalf("pasted %d bytes of type %d, want %d bytes", len(got), typ, len(text))
		}
	}

	selected := a.c.PrimarySource(map[string]string{"text/plain;charset=utf-8": "middle click"})
	if err := errors.Join(a.cb.Primaries.SetSelection(selected.Primary, a.in.Serial), a.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	xc := xDial(t, h)
	xc.intern("PRIMARY", "UTF8_STRING", "INCR", "XSEL_DATA")
	h.Eventually("the X primary selection owned for Wayland", func(*headless.Backend, *compositor.Compositor) bool {
		r, err := xproto.GetSelectionOwner(xc.x, xc.atoms["PRIMARY"]).Reply()
		return err == nil && r.Owner != 0
	})
	_, got := xc.paste(xc.inputWindow(), "PRIMARY", "UTF8_STRING", func(done <-chan struct{}) {
		a.seen(t, "the primary source asked", func() bool { return slices.Contains(selected.Log, "send text/plain;charset=utf-8") })
		<-done
	})
	if string(got) != "middle click" {
		t.Fatalf("pasted %q from PRIMARY", got)
	}
}

type xowner struct {
	*xclient
	win  xproto.Window
	text []byte
	done chan error
}

func (o *xowner) serve() {
	sending := map[xproto.Atom][]byte{}
	for {
		ev, err := o.x.WaitForEvent()
		if ev == nil && err == nil {
			o.done <- nil
			return
		}
		switch e := ev.(type) {
		case xproto.SelectionRequestEvent:
			reply := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: e.Property}
			switch e.Target {
			case o.atoms["TARGETS"]:
				list := slices.Concat(binary.LittleEndian.AppendUint32(nil, uint32(o.atoms["TARGETS"])), binary.LittleEndian.AppendUint32(nil, uint32(o.atoms["UTF8_STRING"])))
				xproto.ChangeProperty(o.x, xproto.PropModeReplace, e.Requestor, e.Property, xproto.AtomAtom, 32, 2, list)
			case o.atoms["UTF8_STRING"]:
				if len(o.text) <= xchunk {
					xproto.ChangeProperty(o.x, xproto.PropModeReplace, e.Requestor, e.Property, e.Target, 8, uint32(len(o.text)), o.text)
					break
				}
				xproto.ChangeWindowAttributes(o.x, e.Requestor, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})
				xproto.ChangeProperty(o.x, xproto.PropModeReplace, e.Requestor, e.Property, o.atoms["INCR"], 32, 1, binary.LittleEndian.AppendUint32(nil, uint32(len(o.text))))
				sending[e.Property] = o.text
			default:
				reply.Property = 0
			}
			xproto.SendEvent(o.x, false, e.Requestor, xproto.EventMaskNoEvent, string(reply.Bytes()))
		case xproto.PropertyNotifyEvent:
			rest, ok := sending[e.Atom]
			if !ok || e.State != xproto.PropertyDelete {
				continue
			}
			n := min(xchunk, len(rest))
			xproto.ChangeProperty(o.x, xproto.PropModeReplace, e.Window, e.Atom, o.atoms["UTF8_STRING"], 8, uint32(n), rest[:n])
			if n == 0 {
				delete(sending, e.Atom)
			} else {
				sending[e.Atom] = rest[n:]
			}
		}
	}
}

func receive(t *testing.T, p *party, mime string) string {
	t.Helper()
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(fds[0], true); err != nil {
		t.Fatal(err)
	}
	r := os.NewFile(uintptr(fds[0]), "paste")
	defer r.Close()
	if err := errors.Join(p.cb.Selection.Data.Receive(mime, fds[1]), unix.Close(fds[1]), p.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	if err := r.SetReadDeadline(time.Now().Add(clientTimeout)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func TestXSelectionsPasteIntoWayland(t *testing.T) {
	h := StartXwayland(t, func(err error) { t.Errorf("xwayland reported: %v", err) }, small)
	big := bytes.Repeat([]byte("fedcba9876543210"), 12<<10)
	for i, text := range [][]byte{[]byte("hello from x"), big} {
		xc := xDial(t, h)
		xc.intern("CLIPBOARD", "TARGETS", "UTF8_STRING", "INCR")
		o := &xowner{xclient: xc, text: text, done: make(chan error, 1)}
		o.win = xc.window(image.Rect(0, 0, 40, 40), 0xffffff, false, nil)
		h.Eventually("the X window focused", func(_ *headless.Backend, c *compositor.Compositor) bool {
			ws := windowsOf(c)
			return len(ws) > 0 && ws[len(ws)-1].Focused && ws[len(ws)-1].AppID == ""
		})
		var before data.Source
		h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
			before = c.CurrentSelection(data.Clipboard)
			return nil
		})
		if err := xproto.SetSelectionOwnerChecked(xc.x, o.win, xc.atoms["CLIPBOARD"], xproto.TimeCurrentTime).Check(); err != nil {
			t.Fatal(err)
		}
		go o.serve()
		h.Eventually("the X selection taken for Wayland", func(_ *headless.Backend, c *compositor.Compositor) bool {
			src := c.CurrentSelection(data.Clipboard)
			return src != nil && src != before && slices.Equal(src.Mimes(), []string{"text/plain;charset=utf-8"})
		})
		p := join(t, h, []string{"small", "big"}[i])
		p.seen(t, "the X selection offered", func() bool { return p.cb.Selection != nil })
		if got := receive(t, p, "text/plain;charset=utf-8"); got != string(text) {
			t.Fatalf("pasted %d bytes, want %d", len(got), len(text))
		}
	}
}
