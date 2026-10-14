package xwayland

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/stubbedev/tilde/internal/data"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
)

type Listener interface {
	XWindowMapped(w *Window) error
	XWindowUnmapped(w *Window) error
	XWindowChanged(w *Window) error
	XWindowRequest(w *Window, r Request) error
	XWindowResponsive(w *Window, responsive bool) error
	CurrentSelection(k data.Kind) data.Source
	SetSelection(k data.Kind, src data.Source) error
}

type Request interface {
	isRequest()
}

type SetFullscreen struct {
	On bool
}

type Activate struct{}

type Configure struct {
	Rect image.Rectangle
}

func (SetFullscreen) isRequest() {}
func (Activate) isRequest()      {}
func (Configure) isRequest()     {}

type State struct {
	Rect                  image.Rectangle
	Activated, Fullscreen bool
}

type props struct {
	class     Class
	title     string
	hints     Hints
	size      SizeHints
	transient xproto.Window
	types     []xproto.Atom
	states    []xproto.Atom
	protocols []xproto.Atom
	pid       uint32
}

type Window struct {
	wm       *wm
	id       xproto.Window
	override bool
	rect     image.Rectangle
	mapped   bool
	serial   uint64
	surface  *surface.Surface
	props    props
	shown    bool
	applied  State
	managed  bool
	ping     uint32
	timer    *loop.Timer
	hung     bool
}

type wm struct {
	x          *xgb.Conn
	root       xproto.Window
	sel        xproto.Window
	atoms      atoms
	windows    map[xproto.Window]*Window
	shell      *Shell
	listener   Listener
	loop       *loop.Loop
	stamps     uint32
	client     *server.Client
	post       func(func() error)
	focused    xproto.Window
	clients    []xproto.Window
	selections [2]*selection
	incoming   []*incoming
	outgoing   map[outKey]*outgoing
}

func (w *Window) Surface() *surface.Surface {
	return w.surface
}

func (w *Window) Override() bool {
	return w.override
}

func (w *Window) Rect() image.Rectangle {
	return w.rect
}

func (w *Window) Title() string {
	return w.props.title
}

func (w *Window) Class() Class {
	return w.props.class
}

func (w *Window) Pid() int32 {
	return int32(w.props.pid) //nolint:gosec // _NET_WM_PID holds a process id
}

func (w *Window) Urgent() bool {
	return w.props.hints.Urgent
}

func (w *Window) Parent() *Window {
	p := w.wm.windows[w.props.transient]
	if p == nil || !p.shown || p.override {
		return nil
	}
	return p
}

func (w *Window) has(list []xproto.Atom, names ...string) bool {
	for _, n := range names {
		if slices.Contains(list, w.wm.atoms[n]) {
			return true
		}
	}
	return false
}

func (w *Window) PrefersFloating() bool {
	return w.props.size.Fixed() || w.has(w.props.states, "_NET_WM_STATE_MODAL") ||
		w.has(w.props.types, "_NET_WM_WINDOW_TYPE_DIALOG", "_NET_WM_WINDOW_TYPE_UTILITY", "_NET_WM_WINDOW_TYPE_SPLASH", "_NET_WM_WINDOW_TYPE_TOOLBAR")
}

func (w *Window) Modal() bool {
	return w.has(w.props.states, "_NET_WM_STATE_MODAL")
}

func (w *Window) Fullscreen() bool {
	return w.has(w.props.states, "_NET_WM_STATE_FULLSCREEN")
}

func (w *Window) Applied() State {
	return w.applied
}

func (w *Window) Apply(s State) error {
	if s == w.applied {
		return nil
	}
	prev := w.applied
	w.applied = s
	if s.Rect != prev.Rect && !s.Rect.Empty() {
		w.rect = s.Rect
		xproto.ConfigureWindow(w.wm.x, w.id, xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight|xproto.ConfigWindowBorderWidth,
			[]uint32{uint32(int32(s.Rect.Min.X)), uint32(int32(s.Rect.Min.Y)), uint32(s.Rect.Dx()), uint32(s.Rect.Dy()), 0}) //nolint:gosec // X geometry is 16-bit and travels as 32-bit words
		w.wm.notifyGeometry(w)
	}
	if s.Fullscreen != prev.Fullscreen {
		w.setState("_NET_WM_STATE_FULLSCREEN", s.Fullscreen)
	}
	if s.Activated != prev.Activated {
		w.wm.activate(w, s.Activated)
	}
	return nil
}

func (w *Window) setState(name string, on bool) {
	atom := w.wm.atoms[name]
	w.props.states = slices.DeleteFunc(w.props.states, func(a xproto.Atom) bool { return a == atom })
	if on {
		w.props.states = append(w.props.states, atom)
	}
	w.wm.changeAtoms(w.id, w.wm.atoms["_NET_WM_STATE"], xproto.AtomAtom, w.props.states)
}

func (w *Window) Close() error {
	if w.has(w.props.protocols, "WM_DELETE_WINDOW") {
		w.wm.protocol(w.id, "WM_DELETE_WINDOW", xproto.TimeCurrentTime, 0)
		return nil
	}
	xproto.KillClient(w.wm.x, uint32(w.id))
	return nil
}

func (m *wm) protocol(win xproto.Window, name string, timestamp, detail uint32) {
	ev := xproto.ClientMessageEvent{
		Format: 32, Window: win, Type: m.atoms["WM_PROTOCOLS"],
		Data: xproto.ClientMessageDataUnionData32New([]uint32{uint32(m.atoms[name]), timestamp, detail, 0, 0}),
	}
	xproto.SendEvent(m.x, false, win, xproto.EventMaskNoEvent, string(ev.Bytes()))
}

func (w *Window) Ping(timeout time.Duration) error {
	if w.ping != 0 || !w.has(w.props.protocols, "_NET_WM_PING") {
		return nil
	}
	if w.timer == nil {
		t, err := w.wm.loop.AddTimer(w.pingTimedOut)
		if err != nil {
			return fmt.Errorf("xwayland: ping timer for window %d: %w", w.id, err)
		}
		w.timer = t
	}
	w.wm.stamps++
	w.ping = w.wm.stamps
	w.wm.protocol(w.id, "_NET_WM_PING", w.ping, uint32(w.id))
	return w.timer.Arm(timeout)
}

func (w *Window) pingTimedOut() error {
	if w.ping == 0 || w.hung {
		return nil
	}
	w.hung = true
	return w.wm.listener.XWindowResponsive(w, false)
}

func (m *wm) pong(id xproto.Window, stamp uint32) error {
	w, ok := m.windows[id]
	if !ok || w.ping == 0 || w.ping != stamp {
		return nil
	}
	w.ping = 0
	if err := w.timer.Disarm(); err != nil {
		return err
	}
	if !w.hung {
		return nil
	}
	w.hung = false
	return m.listener.XWindowResponsive(w, true)
}

func (m *wm) activate(w *Window, on bool) {
	if !on {
		if m.focused == w.id {
			m.focused = 0
			xproto.SetInputFocus(m.x, xproto.InputFocusPointerRoot, xproto.InputFocusPointerRoot, xproto.TimeCurrentTime)
			m.changeWindows(m.root, "_NET_ACTIVE_WINDOW", []xproto.Window{0})
		}
		return
	}
	m.focused = w.id
	if w.props.hints.Input {
		xproto.SetInputFocus(m.x, xproto.InputFocusPointerRoot, w.id, xproto.TimeCurrentTime)
	}
	if w.has(w.props.protocols, "WM_TAKE_FOCUS") {
		m.protocol(w.id, "WM_TAKE_FOCUS", xproto.TimeCurrentTime, 0)
	}
	xproto.ConfigureWindow(m.x, w.id, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
	m.changeWindows(m.root, "_NET_ACTIVE_WINDOW", []xproto.Window{w.id})
}

func (m *wm) changeAtoms(win xproto.Window, prop, typ xproto.Atom, list []xproto.Atom) {
	data := make([]byte, 0, 4*len(list))
	for _, a := range list {
		data = binary.LittleEndian.AppendUint32(data, uint32(a))
	}
	xproto.ChangeProperty(m.x, xproto.PropModeReplace, win, prop, typ, 32, uint32(len(list)), data) //nolint:gosec // property lists are short
}

func (m *wm) changeWindows(win xproto.Window, prop string, list []xproto.Window) {
	atoms := make([]xproto.Atom, len(list))
	for i, w := range list {
		atoms[i] = xproto.Atom(w)
	}
	m.changeAtoms(win, m.atoms[prop], xproto.AtomWindow, atoms)
}

func (m *wm) frameless(id xproto.Window) {
	m.changeAtoms(id, m.atoms["_NET_FRAME_EXTENTS"], xproto.AtomCardinal, []xproto.Atom{0, 0, 0, 0})
}

func (m *wm) window(id xproto.Window) *Window {
	w, ok := m.windows[id]
	if !ok {
		w = &Window{wm: m, id: id}
		m.windows[id] = w
	}
	return w
}

func (m *wm) created(id xproto.Window, rect image.Rectangle, override bool) error {
	w := m.window(id)
	w.rect, w.override = rect, override
	return nil
}

func (m *wm) destroyed(id xproto.Window) error {
	w, ok := m.windows[id]
	if !ok {
		return nil
	}
	delete(m.windows, id)
	var err error
	if w.timer != nil {
		err = w.timer.Remove()
	}
	return errors.Join(err, m.hide(w))
}

func (m *wm) mapRequested(id xproto.Window, p props) error {
	w := m.window(id)
	w.props, w.managed = p, true
	m.changeAtoms(id, m.atoms["WM_STATE"], m.atoms["WM_STATE"], []xproto.Atom{1, 0})
	m.frameless(id)
	xproto.MapWindow(m.x, id)
	return nil
}

func (m *wm) mapped(id xproto.Window, p props, override bool) error {
	w := m.window(id)
	w.mapped, w.override = true, override
	if override {
		w.props = p
	}
	return m.show(w)
}

func (m *wm) unmapped(id xproto.Window) error {
	w, ok := m.windows[id]
	if !ok {
		return nil
	}
	w.mapped = false
	if w.managed {
		m.changeAtoms(id, m.atoms["WM_STATE"], m.atoms["WM_STATE"], []xproto.Atom{0, 0})
	}
	return m.hide(w)
}

func (m *wm) configured(id xproto.Window, rect image.Rectangle) error {
	w, ok := m.windows[id]
	if !ok || w.rect == rect {
		return nil
	}
	w.rect = rect
	if !w.shown || !w.override {
		return nil
	}
	return m.listener.XWindowChanged(w)
}

func (m *wm) configureRequested(id xproto.Window, rect image.Rectangle, mask uint16) error {
	w := m.window(id)
	if !w.shown {
		if mask&(xproto.ConfigWindowX|xproto.ConfigWindowY) == 0 {
			rect = image.Rectangle{Min: w.rect.Min, Max: w.rect.Min.Add(rect.Size())}
		}
		w.rect = rect
		xproto.ConfigureWindow(m.x, id, xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
			[]uint32{uint32(int32(rect.Min.X)), uint32(int32(rect.Min.Y)), uint32(rect.Dx()), uint32(rect.Dy())}) //nolint:gosec // X geometry is 16-bit and travels as 32-bit words
		return nil
	}
	if err := m.listener.XWindowRequest(w, Configure{Rect: rect}); err != nil {
		return err
	}
	m.notifyGeometry(w)
	return nil
}

func (m *wm) notifyGeometry(w *Window) {
	r := w.rect
	ev := xproto.ConfigureNotifyEvent{Event: w.id, Window: w.id, X: int16(r.Min.X), Y: int16(r.Min.Y), Width: uint16(r.Dx()), Height: uint16(r.Dy())} //nolint:gosec // X geometry is 16-bit
	xproto.SendEvent(m.x, false, w.id, xproto.EventMaskStructureNotify, string(ev.Bytes()))
}

func (m *wm) propertyChanged(id xproto.Window, p props) error {
	w, ok := m.windows[id]
	if !ok {
		return nil
	}
	w.props = p
	if !w.shown {
		return nil
	}
	return m.listener.XWindowChanged(w)
}

func (m *wm) serialSet(id xproto.Window, serial uint64) error {
	w := m.window(id)
	w.serial = serial
	if s, ok := m.shell.Surface(serial); ok {
		w.surface = s
	}
	return m.show(w)
}

func (m *wm) associated(serial uint64, s *surface.Surface) error {
	for _, w := range m.windows {
		if w.serial == serial {
			w.surface = s
			return m.show(w)
		}
	}
	return nil
}

func (m *wm) dissociated(serial uint64) error {
	for _, w := range m.windows {
		if w.serial == serial {
			w.surface = nil
			return m.hide(w)
		}
	}
	return nil
}

func (m *wm) stateRequested(id xproto.Window, action uint32, a, b xproto.Atom) error {
	w, ok := m.windows[id]
	if !ok {
		return nil
	}
	full := m.atoms["_NET_WM_STATE_FULLSCREEN"]
	if a != full && b != full {
		return nil
	}
	on := w.Fullscreen()
	switch action {
	case 0:
		on = false
	case 1:
		on = true
	case 2:
		on = !on
	default:
		return fmt.Errorf("xwayland: _NET_WM_STATE action %d on window %#x", action, id)
	}
	if !w.shown {
		w.setState("_NET_WM_STATE_FULLSCREEN", on)
		return nil
	}
	return m.listener.XWindowRequest(w, SetFullscreen{On: on})
}

func (m *wm) activeRequested(id xproto.Window) error {
	w, ok := m.windows[id]
	if !ok || !w.shown {
		return nil
	}
	return m.listener.XWindowRequest(w, Activate{})
}

func (m *wm) show(w *Window) error {
	if w.shown || !w.mapped || w.surface == nil {
		return nil
	}
	w.shown = true
	if w.managed {
		m.clients = append(m.clients, w.id)
		m.changeWindows(m.root, "_NET_CLIENT_LIST", m.clients)
	}
	return m.listener.XWindowMapped(w)
}

func (m *wm) hide(w *Window) error {
	if !w.shown {
		return nil
	}
	w.shown, w.applied = false, State{}
	if m.focused == w.id {
		m.focused = 0
	}
	if i := slices.Index(m.clients, w.id); i >= 0 {
		m.clients = slices.Delete(m.clients, i, i+1)
		m.changeWindows(m.root, "_NET_CLIENT_LIST", m.clients)
	}
	return m.listener.XWindowUnmapped(w)
}

func (m *wm) close() error {
	var errs []error
	for _, w := range m.windows {
		errs = append(errs, m.hide(w))
	}
	clear(m.windows)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
