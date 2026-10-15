// Package inputmethod implements zwp_text_input_manager_v3 and
// zwp_input_method_manager_v2: the focused client's enabled text input and
// the seat's input method relay state, text, preedit and deletions to each
// other, the input method can grab the keyboard, and its popups follow the
// text cursor.
package inputmethod

import (
	"errors"
	"image"
	"slices"

	sim "github.com/stubbedev/tilde/internal/protocol/server/inputmethod"
	stix "github.com/stubbedev/tilde/internal/protocol/server/textinput"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/seat"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/xkb"
)

const (
	textInputVersion   = 1
	inputMethodVersion = 1
	popupRole          = "zwp_input_popup_surface_v2"
)

var Interface = sim.ZwpInputMethodManagerV2Interface

type Keyboard interface {
	Grab(g seat.KeyboardGrab) error
	Ungrab(g seat.KeyboardGrab) error
}

type Listener interface {
	PopupsChanged() error
}

type Manager struct {
	keyboard Keyboard
	listener Listener
	focus    *surface.Surface
	inputs   []*textInput
	active   *textInput
	im       *inputMethod
}

func New(d *server.Display, keyboard Keyboard, listener Listener) (*Manager, error) {
	m := &Manager{keyboard: keyboard, listener: listener}
	if _, err := d.AddGlobal(stix.ZwpTextInputManagerV3Interface, textInputVersion, m.bindTextInput); err != nil {
		return nil, err
	}
	if _, err := d.AddGlobal(Interface, inputMethodVersion, m.bindInputMethod); err != nil {
		return nil, err
	}
	return m, nil
}

type Popup struct {
	Surface *surface.Surface
	Anchor  *surface.Surface
	Cursor  image.Rectangle
	p       *popup
}

func (p Popup) Placed(cursor image.Rectangle) error {
	return p.p.placed(cursor)
}

func (m *Manager) Popups() []Popup {
	if m.im == nil || m.im.phase != imActive || m.active == nil || m.active.focus == nil {
		return nil
	}
	out := make([]Popup, 0, len(m.im.popups))
	for _, p := range m.im.popups {
		out = append(out, Popup{Surface: p.s, Anchor: m.active.focus, Cursor: m.active.current.cursor, p: p})
	}
	return out
}

func (m *Manager) Focus(s *surface.Surface) error {
	if s == m.focus {
		return nil
	}
	var errs []error
	if old := m.focus; old != nil {
		for _, ti := range m.inputs {
			if ti.focus == old {
				ti.focus, ti.current.enabled = nil, false
				errs = append(errs, ti.c.Sent(ti.res.SendLeave(old.Resource())))
			}
		}
		if m.active != nil {
			m.active = nil
			errs = append(errs, m.deactivate())
		}
	}
	m.focus = s
	if s != nil {
		for _, ti := range m.inputs {
			errs = append(errs, ti.enter(s))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) deactivate() error {
	if m.im == nil || m.im.phase != imActive {
		return nil
	}
	m.im.phase = imInactive
	return errors.Join(m.im.c.Sent(m.im.res.SendDeactivate()), m.im.done(), m.listener.PopupsChanged())
}

func (m *Manager) activate(ti *textInput) error {
	m.active = ti
	if m.im == nil {
		return nil
	}
	m.im.phase = imActive
	return errors.Join(m.im.c.Sent(m.im.res.SendActivate()), m.im.state(ti), m.listener.PopupsChanged())
}

func (m *Manager) bindTextInput(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := stix.NewZwpTextInputManagerV3(c, id, v, textInputManager{m, c})
	return err
}

type textInputManager struct {
	m *Manager
	c *server.Client
}

func (textInputManager) Destroy(*stix.ZwpTextInputManagerV3) error { return nil }

func (r textInputManager) GetTextInput(_ *stix.ZwpTextInputManagerV3, res *stix.ZwpTextInputV3, _ *sw.WlSeat) (stix.ZwpTextInputV3Handler, error) {
	ti := &textInput{m: r.m, c: r.c, res: res}
	r.m.inputs = append(r.m.inputs, ti)
	server.OnDestroy(res, ti.gone)
	if f := r.m.focus; f != nil {
		return ti, ti.enter(f)
	}
	return ti, nil
}

type surrounding struct {
	text           string
	cursor, anchor int32
}

type contentType struct {
	hint    stix.ZwpTextInputV3ContentHint
	purpose stix.ZwpTextInputV3ContentPurpose
}

type textInputState struct {
	enabled     bool
	surrounding *surrounding
	cause       *stix.ZwpTextInputV3ChangeCause
	content     *contentType
	cursor      image.Rectangle
}

type textInput struct {
	m       *Manager
	c       *server.Client
	res     *stix.ZwpTextInputV3
	focus   *surface.Surface
	pending textInputState
	current textInputState
	commits uint32
}

func (ti *textInput) enter(s *surface.Surface) error {
	if s.Client() != ti.c {
		return nil
	}
	ti.focus = s
	return ti.c.Sent(ti.res.SendEnter(s.Resource()))
}

func (ti *textInput) gone() error {
	m := ti.m
	m.inputs = slices.DeleteFunc(m.inputs, func(x *textInput) bool { return x == ti })
	if m.active != ti {
		return nil
	}
	m.active = nil
	return m.deactivate()
}

func (*textInput) Destroy(*stix.ZwpTextInputV3) error { return nil }

func (ti *textInput) Enable(*stix.ZwpTextInputV3) error {
	ti.pending = textInputState{enabled: true}
	return nil
}

func (ti *textInput) Disable(*stix.ZwpTextInputV3) error {
	ti.pending.enabled = false
	return nil
}

func (ti *textInput) SetSurroundingText(_ *stix.ZwpTextInputV3, text string, cursor, anchor int32) error {
	ti.pending.surrounding = &surrounding{text: text, cursor: cursor, anchor: anchor}
	return nil
}

func (ti *textInput) SetTextChangeCause(_ *stix.ZwpTextInputV3, cause stix.ZwpTextInputV3ChangeCause) error {
	ti.pending.cause = &cause
	return nil
}

func (ti *textInput) SetContentType(_ *stix.ZwpTextInputV3, hint stix.ZwpTextInputV3ContentHint, purpose stix.ZwpTextInputV3ContentPurpose) error {
	ti.pending.content = &contentType{hint: hint, purpose: purpose}
	return nil
}

func (ti *textInput) SetCursorRectangle(_ *stix.ZwpTextInputV3, x, y, w, h int32) error {
	ti.pending.cursor = image.Rect(int(x), int(y), int(x)+int(max(w, 0)), int(y)+int(max(h, 0)))
	return nil
}

func (ti *textInput) Commit(*stix.ZwpTextInputV3) error {
	ti.commits++
	if ti.focus == nil {
		return nil
	}
	m := ti.m
	ti.current = ti.pending
	ti.pending.cause = nil
	switch {
	case m.active != nil && m.active != ti:
		return nil
	case ti.current.enabled && m.active != ti:
		return m.activate(ti)
	case ti.current.enabled:
		if m.im == nil || m.im.phase != imActive {
			return nil
		}
		return errors.Join(m.im.state(ti), m.listener.PopupsChanged())
	case m.active == ti:
		m.active = nil
		return m.deactivate()
	}
	return nil
}

var errNotAdvertised = errors.New("inputmethod: the request needs zwp_text_input_v3 version 2, which is not advertised")

func (*textInput) SetAvailableActions(*stix.ZwpTextInputV3, []byte) error { return errNotAdvertised }

func (*textInput) ShowInputPanel(*stix.ZwpTextInputV3) error { return errNotAdvertised }

func (*textInput) HideInputPanel(*stix.ZwpTextInputV3) error { return errNotAdvertised }

func (m *Manager) bindInputMethod(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := sim.NewZwpInputMethodManagerV2(c, id, v, inputMethodManager{m, c})
	return err
}

type inputMethodManager struct {
	m *Manager
	c *server.Client
}

func (inputMethodManager) Destroy(*sim.ZwpInputMethodManagerV2) error { return nil }

func (r inputMethodManager) GetInputMethod(_ *sim.ZwpInputMethodManagerV2, _ *sw.WlSeat, res *sim.ZwpInputMethodV2) (sim.ZwpInputMethodV2Handler, error) {
	im := &inputMethod{m: r.m, c: r.c, res: res}
	if r.m.im != nil {
		im.phase = imInert
		return im, r.c.Sent(res.SendUnavailable())
	}
	r.m.im = im
	server.OnDestroy(res, im.gone)
	if ti := r.m.active; ti != nil {
		im.phase = imActive
		return im, errors.Join(r.c.Sent(res.SendActivate()), im.state(ti))
	}
	return im, nil
}

type preedit struct {
	text       string
	begin, end int32
}

type imState struct {
	commit  *string
	preedit *preedit
	before  uint32
	after   uint32
}

type imPhase uint8

const (
	imInactive imPhase = iota
	imActive
	imInert
)

type inputMethod struct {
	m       *Manager
	c       *server.Client
	res     *sim.ZwpInputMethodV2
	phase   imPhase
	dones   uint32
	pending imState
	popups  []*popup
	grab    *keyboardGrab
}

func (im *inputMethod) done() error {
	im.dones++
	return im.c.Sent(im.res.SendDone())
}

func (im *inputMethod) state(ti *textInput) error {
	st := ti.current
	var errs []error
	if s := st.surrounding; s != nil {
		errs = append(errs, im.c.Sent(im.res.SendSurroundingText(s.text, uint32(max(s.cursor, 0)), uint32(max(s.anchor, 0)))))
	}
	if st.cause != nil {
		errs = append(errs, im.c.Sent(im.res.SendTextChangeCause(*st.cause)))
	}
	if ct := st.content; ct != nil {
		errs = append(errs, im.c.Sent(im.res.SendContentType(ct.hint, ct.purpose)))
	}
	return errors.Join(append(errs, im.done())...)
}

func (im *inputMethod) gone() error {
	m := im.m
	var errs []error
	if im.grab != nil {
		errs = append(errs, m.keyboard.Ungrab(im.grab))
		im.grab = nil
	}
	for _, p := range slices.Clone(im.popups) {
		errs = append(errs, p.gone())
	}
	if m.im == im {
		m.im = nil
	}
	if ti := m.active; ti != nil {
		errs = append(errs, ti.relay(imState{}))
	}
	return errors.Join(errs...)
}

func (*inputMethod) Destroy(*sim.ZwpInputMethodV2) error { return nil }

func (im *inputMethod) CommitString(_ *sim.ZwpInputMethodV2, text string) error {
	im.pending.commit = &text
	return nil
}

func (im *inputMethod) SetPreeditString(_ *sim.ZwpInputMethodV2, text string, begin, end int32) error {
	im.pending.preedit = &preedit{text: text, begin: begin, end: end}
	return nil
}

func (im *inputMethod) DeleteSurroundingText(_ *sim.ZwpInputMethodV2, before, after uint32) error {
	im.pending.before, im.pending.after = before, after
	return nil
}

func (im *inputMethod) Commit(_ *sim.ZwpInputMethodV2, serial uint32) error {
	st := im.pending
	im.pending = imState{}
	if im.phase != imActive || im.m.active == nil || serial != im.dones {
		return nil
	}
	return im.m.active.relay(st)
}

func (ti *textInput) relay(st imState) error {
	var errs []error
	if p := st.preedit; p != nil {
		errs = append(errs, ti.c.Sent(ti.res.SendPreeditString(&p.text, p.begin, p.end)))
	}
	if st.before != 0 || st.after != 0 {
		errs = append(errs, ti.c.Sent(ti.res.SendDeleteSurroundingText(st.before, st.after)))
	}
	if st.commit != nil {
		errs = append(errs, ti.c.Sent(ti.res.SendCommitString(st.commit)))
	}
	return errors.Join(append(errs, ti.c.Sent(ti.res.SendDone(ti.commits)))...)
}

func (im *inputMethod) GetInputPopupSurface(obj *sim.ZwpInputMethodV2, res *sim.ZwpInputPopupSurfaceV2, wl *sw.WlSurface) (sim.ZwpInputPopupSurfaceV2Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	p := &popup{im: im, s: s, res: res}
	if err := s.SetRole(p); err != nil {
		return nil, obj.ProtocolError(sim.ZwpInputMethodV2ErrorRole, err.Error())
	}
	im.popups = append(im.popups, p)
	server.OnDestroy(res, p.gone)
	server.OnDestroy(wl, p.gone)
	return p, im.m.listener.PopupsChanged()
}

func (im *inputMethod) GrabKeyboard(_ *sim.ZwpInputMethodV2, res *sim.ZwpInputMethodKeyboardGrabV2) (sim.ZwpInputMethodKeyboardGrabV2Handler, error) {
	g := &keyboardGrab{c: im.c, res: res}
	if im.phase == imInert || im.grab != nil {
		return g, nil
	}
	im.grab = g
	server.OnDestroy(res, func() error {
		if im.grab != g {
			return nil
		}
		im.grab = nil
		return im.m.keyboard.Ungrab(g)
	})
	return g, im.m.keyboard.Grab(g)
}

type popup struct {
	im     *inputMethod
	s      *surface.Surface
	res    *sim.ZwpInputPopupSurfaceV2
	cursor image.Rectangle
	sent   bool
}

func (*popup) RoleName() string { return popupRole }

func (*popup) Committed(*surface.Surface) error { return nil }

func (*popup) Destroy(*sim.ZwpInputPopupSurfaceV2) error { return nil }

func (p *popup) placed(cursor image.Rectangle) error {
	if p.sent && cursor == p.cursor {
		return nil
	}
	p.cursor, p.sent = cursor, true
	return p.im.c.Sent(p.res.SendTextInputRectangle(int32(cursor.Min.X), int32(cursor.Min.Y), int32(cursor.Dx()), int32(cursor.Dy()))) //nolint:gosec // surface coordinates fit in int32
}

func (p *popup) gone() error {
	im := p.im
	if !slices.Contains(im.popups, p) {
		return nil
	}
	im.popups = slices.DeleteFunc(im.popups, func(x *popup) bool { return x == p })
	p.s.ClearRole(p)
	return im.m.listener.PopupsChanged()
}

type keyboardGrab struct {
	c   *server.Client
	res *sim.ZwpInputMethodKeyboardGrabV2
}

func (*keyboardGrab) Release(*sim.ZwpInputMethodKeyboardGrabV2) error { return nil }

func (g *keyboardGrab) Keymap(fd int, size uint32) error {
	return g.c.Sent(g.res.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, fd, size))
}

func (g *keyboardGrab) RepeatInfo(rate, delay int32) error {
	return g.c.Sent(g.res.SendRepeatInfo(rate, delay))
}

func (g *keyboardGrab) Key(serial, ms, code uint32, pressed bool) error {
	state := sw.WlKeyboardKeyStateReleased
	if pressed {
		state = sw.WlKeyboardKeyStatePressed
	}
	return g.c.Sent(g.res.SendKey(serial, ms, code, state))
}

func (g *keyboardGrab) Modifiers(serial uint32, m xkb.Modifiers) error {
	return g.c.Sent(g.res.SendModifiers(serial, m.Depressed, m.Latched, m.Locked, m.Group))
}
