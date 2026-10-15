package xwayland

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"net"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/composite"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
)

func init() {
	xgb.Logger = log.New(io.Discard, "", 0)
}

type atoms map[string]xproto.Atom

var atomNames = []string{
	"WM_S0", "_NET_WM_CM_S0", "WM_PROTOCOLS", "WM_DELETE_WINDOW", "WM_TAKE_FOCUS", "WM_STATE", "WM_CLASS", "WM_NAME",
	"WM_HINTS", "WM_NORMAL_HINTS", "WM_TRANSIENT_FOR", "WL_SURFACE_SERIAL", "UTF8_STRING",
	"_NET_SUPPORTED", "_NET_SUPPORTING_WM_CHECK", "_NET_WM_NAME", "_NET_WM_PID", "_NET_ACTIVE_WINDOW", "_NET_CLIENT_LIST",
	"_NET_WM_STATE", "_NET_WM_STATE_FULLSCREEN", "_NET_WM_STATE_MODAL",
	"_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_NORMAL", "_NET_WM_WINDOW_TYPE_DIALOG", "_NET_WM_WINDOW_TYPE_UTILITY",
	"_NET_WM_WINDOW_TYPE_SPLASH", "_NET_WM_WINDOW_TYPE_TOOLBAR",
	"CLIPBOARD", "PRIMARY", "TARGETS", "INCR", "_WL_SELECTION",
	"_NET_FRAME_EXTENTS", "_NET_REQUEST_FRAME_EXTENTS", "_NET_WM_PING",
}

var supported = []string{
	"_NET_SUPPORTED", "_NET_SUPPORTING_WM_CHECK", "_NET_WM_NAME", "_NET_WM_PID", "_NET_ACTIVE_WINDOW", "_NET_CLIENT_LIST",
	"_NET_WM_STATE", "_NET_WM_STATE_FULLSCREEN", "_NET_WM_STATE_MODAL", "_NET_WM_WINDOW_TYPE",
	"_NET_FRAME_EXTENTS", "_NET_REQUEST_FRAME_EXTENTS", "_NET_WM_PING",
}

var watched = []string{
	"WM_CLASS", "WM_NAME", "_NET_WM_NAME", "WM_HINTS", "WM_NORMAL_HINTS", "WM_TRANSIENT_FOR", "_NET_WM_WINDOW_TYPE",
	"_NET_WM_STATE", "WM_PROTOCOLS", "_NET_WM_PID",
}

func intern(x *xgb.Conn, names []string) (atoms, error) {
	cookies := make([]xproto.InternAtomCookie, len(names))
	for i, name := range names {
		cookies[i] = xproto.InternAtom(x, false, uint16(len(name)), name) //nolint:gosec // atom names are short constants
	}
	out := make(atoms, len(names))
	for i, c := range cookies {
		reply, err := c.Reply()
		if err != nil {
			return nil, fmt.Errorf("intern %s: %w", names[i], err)
		}
		out[names[i]] = reply.Atom
	}
	return out, nil
}

type xconn struct {
	x      *xgb.Conn
	atoms  atoms
	root   xproto.Window
	check  xproto.Window
	sel    xproto.Window
	post   func(func(*wm) error)
	report func(error)
}

func claim(conn net.Conn) (*xconn, error) {
	x, err := xgb.NewConnNet(conn)
	if err != nil {
		return nil, fmt.Errorf("connect as the window manager: %w", err)
	}
	c := &xconn{x: x}
	if err := c.own(); err != nil {
		x.Close()
		return nil, err
	}
	return c, nil
}

func (c *xconn) own() error {
	x := c.x
	if err := composite.Init(x); err != nil {
		return fmt.Errorf("the Composite extension: %w", err)
	}
	if err := xfixes.Init(x); err != nil {
		return fmt.Errorf("the XFixes extension: %w", err)
	}
	if _, err := xfixes.QueryVersion(x, 5, 0).Reply(); err != nil {
		return fmt.Errorf("the XFixes version: %w", err)
	}
	screen := xproto.Setup(x).DefaultScreen(x)
	c.root = screen.Root
	var err error
	if c.atoms, err = intern(x, atomNames); err != nil {
		return err
	}
	if c.check, err = xproto.NewWindowId(x); err != nil {
		return fmt.Errorf("allocate the window manager's window: %w", err)
	}
	if c.sel, err = xproto.NewWindowId(x); err != nil {
		return fmt.Errorf("allocate the selection window: %w", err)
	}
	owner := uint32(xfixes.SelectionEventMaskSetSelectionOwner | xfixes.SelectionEventMaskSelectionWindowDestroy | xfixes.SelectionEventMaskSelectionClientClose)
	id := binary.LittleEndian.AppendUint32(nil, uint32(c.check))
	var support []byte
	for _, name := range supported {
		support = binary.LittleEndian.AppendUint32(support, uint32(c.atoms[name]))
	}
	const name = "tilde"
	events := uint32(xproto.EventMaskSubstructureRedirect | xproto.EventMaskSubstructureNotify | xproto.EventMaskPropertyChange)
	checks := []interface{ Check() error }{
		xproto.ChangeWindowAttributesChecked(x, c.root, xproto.CwEventMask, []uint32{events}),
		composite.RedirectSubwindowsChecked(x, c.root, composite.RedirectManual),
		xproto.CreateWindowChecked(x, 0, c.check, c.root, -100, -100, 1, 1, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil),
		xproto.ChangePropertyChecked(x, xproto.PropModeReplace, c.root, c.atoms["_NET_SUPPORTING_WM_CHECK"], xproto.AtomWindow, 32, 1, id),
		xproto.ChangePropertyChecked(x, xproto.PropModeReplace, c.check, c.atoms["_NET_SUPPORTING_WM_CHECK"], xproto.AtomWindow, 32, 1, id),
		xproto.ChangePropertyChecked(x, xproto.PropModeReplace, c.check, c.atoms["_NET_WM_NAME"], c.atoms["UTF8_STRING"], 8, uint32(len(name)), []byte(name)),
		xproto.ChangePropertyChecked(x, xproto.PropModeReplace, c.root, c.atoms["_NET_SUPPORTED"], xproto.AtomAtom, 32, uint32(len(supported)), support), //nolint:gosec // a short constant list
		xproto.SetSelectionOwnerChecked(x, c.check, c.atoms["WM_S0"], xproto.TimeCurrentTime),
		xproto.SetSelectionOwnerChecked(x, c.check, c.atoms["_NET_WM_CM_S0"], xproto.TimeCurrentTime),
		xproto.CreateWindowChecked(x, 0, c.sel, c.root, -100, -100, 1, 1, 0, xproto.WindowClassInputOnly, screen.RootVisual, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}),
		xfixes.SelectSelectionInputChecked(x, c.sel, c.atoms["CLIPBOARD"], owner),
		xfixes.SelectSelectionInputChecked(x, c.sel, c.atoms["PRIMARY"], owner),
	}
	errs := make([]error, 0, len(checks))
	for _, ck := range checks {
		errs = append(errs, ck.Check())
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("become the window manager: %w", err)
	}
	return nil
}

func rectOf(x, y int16, w, h uint16) image.Rectangle {
	return image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h))
}

func (c *xconn) run() {
	for {
		ev, xerr := c.x.WaitForEvent()
		switch {
		case ev == nil && xerr == nil:
			return
		case xerr != nil:
			if _, gone := xerr.(xproto.WindowError); !gone {
				c.report(fmt.Errorf("xwayland: X error: %w", xerr))
			}
		default:
			c.handle(ev)
		}
	}
}

func (c *xconn) handle(ev xgb.Event) {
	switch e := ev.(type) {
	case xproto.CreateNotifyEvent:
		xproto.ChangeWindowAttributes(c.x, e.Window, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})
		rect := rectOf(e.X, e.Y, e.Width, e.Height)
		c.post(func(m *wm) error { return m.created(e.Window, rect, e.OverrideRedirect) })
	case xproto.DestroyNotifyEvent:
		c.post(func(m *wm) error { return m.destroyed(e.Window) })
	case xproto.MapRequestEvent:
		if p, ok := c.props(e.Window); ok {
			c.post(func(m *wm) error { return m.mapRequested(e.Window, p) })
		}
	case xproto.MapNotifyEvent:
		if p, ok := c.props(e.Window); ok {
			c.post(func(m *wm) error { return m.mapped(e.Window, p, e.OverrideRedirect) })
		}
	case xproto.UnmapNotifyEvent:
		c.post(func(m *wm) error { return m.unmapped(e.Window) })
	case xproto.ConfigureNotifyEvent:
		rect := rectOf(e.X, e.Y, e.Width, e.Height)
		c.post(func(m *wm) error { return m.configured(e.Window, rect) })
	case xproto.ConfigureRequestEvent:
		rect := rectOf(e.X, e.Y, e.Width, e.Height)
		c.post(func(m *wm) error { return m.configureRequested(e.Window, rect, e.ValueMask) })
	case xproto.PropertyNotifyEvent:
		c.property(e)
	case xproto.ClientMessageEvent:
		c.message(e)
	case xfixes.SelectionNotifyEvent:
		c.post(func(m *wm) error { return m.selectionOwned(e.Selection, e.Owner, e.SelectionTimestamp) })
	case xproto.SelectionNotifyEvent:
		c.converted(e)
	case xproto.SelectionRequestEvent:
		c.post(func(m *wm) error { return m.requested(e) })
	}
}

func (c *xconn) property(e xproto.PropertyNotifyEvent) {
	switch {
	case e.Window == c.sel && e.State == xproto.PropertyNewValue && e.Atom == c.atoms["_WL_SELECTION"]:
		if r, ok := c.read(e.Window, e.Atom, false); ok {
			c.post(func(m *wm) error { return m.chunk(r.Type, r.Value) })
		}
		return
	case e.State == xproto.PropertyDelete:
		c.post(func(m *wm) error { return m.propertyDeleted(e.Window, e.Atom) })
	}
	if !c.watches(e.Atom) {
		return
	}
	if p, ok := c.props(e.Window); ok {
		c.post(func(m *wm) error { return m.propertyChanged(e.Window, p) })
	}
}

func (c *xconn) read(win xproto.Window, property xproto.Atom, del bool) (*xproto.GetPropertyReply, bool) {
	r, err := xproto.GetProperty(c.x, del, win, property, xproto.GetPropertyTypeAny, 0, maxSelection/4+1).Reply()
	if err != nil {
		c.report(fmt.Errorf("xwayland: read the selection property: %w", err))
		return nil, false
	}
	return r, true
}

func (c *xconn) converted(e xproto.SelectionNotifyEvent) {
	if e.Requestor != c.sel {
		return
	}
	if e.Target == c.atoms["TARGETS"] {
		offered := map[string]xproto.Atom{}
		if e.Property != 0 {
			if r, ok := c.read(c.sel, e.Property, true); ok {
				offered = c.mimes(r.Value)
			}
		}
		c.post(func(m *wm) error { return m.targetsOffered(e.Selection, offered) })
		return
	}
	if e.Property == 0 {
		c.post(func(m *wm) error { return m.converted(e.Target, 0, nil, false) })
		return
	}
	r, ok := c.read(c.sel, e.Property, false)
	c.post(func(m *wm) error {
		if !ok {
			return m.converted(e.Target, 0, nil, false)
		}
		return m.converted(e.Target, r.Type, r.Value, true)
	})
}

func (c *xconn) mimes(value []byte) map[string]xproto.Atom {
	list, err := atomList(value, "TARGETS")
	if err != nil {
		c.report(fmt.Errorf("xwayland: the X selection's targets: %w", err))
		return nil
	}
	cookies := make([]xproto.GetAtomNameCookie, len(list))
	for i, a := range list {
		cookies[i] = xproto.GetAtomName(c.x, a)
	}
	out := make(map[string]xproto.Atom, len(list))
	for i, ck := range cookies {
		r, err := ck.Reply()
		if err != nil {
			c.report(fmt.Errorf("xwayland: name of target atom %d: %w", list[i], err))
			continue
		}
		if mime, ok := mimeOf(r.Name); ok {
			out[mime] = list[i]
		}
	}
	return out
}

func (c *xconn) watches(a xproto.Atom) bool {
	for _, name := range watched {
		if c.atoms[name] == a {
			return true
		}
	}
	return false
}

func (c *xconn) message(e xproto.ClientMessageEvent) {
	d := e.Data.Data32
	switch e.Type {
	case c.atoms["WL_SURFACE_SERIAL"]:
		serial := uint64(d[1])<<32 | uint64(d[0])
		c.post(func(m *wm) error { return m.serialSet(e.Window, serial) })
	case c.atoms["_NET_WM_STATE"]:
		c.post(func(m *wm) error { return m.stateRequested(e.Window, d[0], xproto.Atom(d[1]), xproto.Atom(d[2])) })
	case c.atoms["_NET_ACTIVE_WINDOW"]:
		c.post(func(m *wm) error { return m.activeRequested(e.Window) })
	case c.atoms["WM_PROTOCOLS"]:
		if xproto.Atom(d[0]) == c.atoms["_NET_WM_PING"] {
			c.post(func(m *wm) error { return m.pong(xproto.Window(d[2]), d[1]) })
		}
	case c.atoms["_NET_REQUEST_FRAME_EXTENTS"]:
		c.post(func(m *wm) error {
			m.frameless(e.Window)
			return nil
		})
	}
}

func (c *xconn) props(win xproto.Window) (props, bool) {
	names := watched
	cookies := make([]xproto.GetPropertyCookie, len(names))
	for i, name := range names {
		cookies[i] = xproto.GetProperty(c.x, false, win, c.atoms[name], xproto.GetPropertyTypeAny, 0, 2048)
	}
	values := make(map[string][]byte, len(names))
	for i, ck := range cookies {
		reply, err := ck.Reply()
		if err != nil {
			if _, gone := errors.AsType[xproto.WindowError](err); gone {
				return props{}, false
			}
			c.report(fmt.Errorf("xwayland: read %s of window %#x: %w", names[i], win, err))
			continue
		}
		if reply.ValueLen > 0 {
			values[names[i]] = reply.Value
		}
	}
	p, err := parseProps(values)
	if err != nil {
		c.report(fmt.Errorf("xwayland: window %#x: %w", win, err))
	}
	return p, true
}

func atomList(data []byte, what string) ([]xproto.Atom, error) {
	v, err := cardinals(data, what, 0)
	if err != nil {
		return nil, err
	}
	out := make([]xproto.Atom, len(v))
	for i, a := range v {
		out[i] = xproto.Atom(a)
	}
	return out, nil
}

func parseProps(values map[string][]byte) (props, error) {
	var p props
	var errs []error
	p.hints = Hints{Input: true}
	if v, ok := values["WM_CLASS"]; ok {
		var err error
		p.class, err = parseClass(v)
		errs = append(errs, err)
	}
	switch {
	case values["_NET_WM_NAME"] != nil:
		var err error
		p.title, err = parseUTF8(values["_NET_WM_NAME"])
		errs = append(errs, err)
	case values["WM_NAME"] != nil:
		p.title = latin1(values["WM_NAME"])
	}
	if v, ok := values["WM_HINTS"]; ok {
		var err error
		p.hints, err = parseHints(v)
		errs = append(errs, err)
	}
	if v, ok := values["WM_NORMAL_HINTS"]; ok {
		var err error
		p.size, err = parseSizeHints(v)
		errs = append(errs, err)
	}
	if v, ok := values["WM_TRANSIENT_FOR"]; ok {
		w, err := cardinals(v, "WM_TRANSIENT_FOR", 1)
		if err == nil {
			p.transient = xproto.Window(w[0])
		}
		errs = append(errs, err)
	}
	if v, ok := values["_NET_WM_PID"]; ok {
		pid, err := cardinals(v, "_NET_WM_PID", 1)
		if err == nil {
			p.pid = pid[0]
		}
		errs = append(errs, err)
	}
	for _, l := range []struct {
		name string
		dst  *[]xproto.Atom
	}{{"_NET_WM_WINDOW_TYPE", &p.types}, {"_NET_WM_STATE", &p.states}, {"WM_PROTOCOLS", &p.protocols}} {
		if v, ok := values[l.name]; ok {
			var err error
			*l.dst, err = atomList(v, l.name)
			errs = append(errs, err)
		}
	}
	return p, errors.Join(errs...)
}
