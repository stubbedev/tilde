package seat

import (
	"encoding/binary"
	"errors"
	"slices"
	"time"

	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

type keyboardState struct {
	focus   *surface.Surface
	pressed []uint32
	grab    KeyboardGrab
	grabbed []uint32
}

type KeyboardGrab interface {
	Keymap(fd int, size uint32) error
	RepeatInfo(rate, delay int32) error
	Key(serial, ms, code uint32, pressed bool) error
	Modifiers(serial uint32, m xkb.Modifiers) error
}

func (s *Seat) Grab(g KeyboardGrab) error {
	s.keyboard.grab = g
	return errors.Join(g.Keymap(s.cfg.Keymap.FD(), s.cfg.Keymap.Size()), g.RepeatInfo(s.cfg.RepeatRate, s.cfg.RepeatDelay), g.Modifiers(s.display.NextSerial(), s.state.Modifiers()))
}

func (s *Seat) Ungrab(g KeyboardGrab) error {
	if s.keyboard.grab != g {
		return nil
	}
	s.keyboard.grab = nil
	kbs := s.focusedKeyboards()
	var errs []error
	for _, kb := range kbs {
		errs = append(errs, s.sendModifiers(kb))
	}
	return errors.Join(errs...)
}

func (s *Seat) grabKey(code uint32, pressed, modsChanged bool, at time.Duration) (bool, error) {
	k := &s.keyboard
	i := slices.Index(k.grabbed, code)
	switch {
	case pressed && k.grab != nil:
		k.grabbed = append(k.grabbed, code)
	case !pressed && i >= 0:
		k.grabbed = slices.Delete(k.grabbed, i, i+1)
		if k.grab == nil {
			return true, nil
		}
	default:
		return false, nil
	}
	err := k.grab.Key(s.display.NextSerial(), wire.Millis(at), code, pressed)
	if modsChanged {
		err = errors.Join(err, k.grab.Modifiers(s.display.NextSerial(), s.state.Modifiers()))
	}
	return true, err
}

func (s *Seat) Focus() *surface.Surface {
	return s.keyboard.focus
}

func (s *Seat) keysArray() []byte {
	keys := make([]byte, 0, 4*len(s.keyboard.pressed))
	for _, k := range s.keyboard.pressed {
		keys = binary.NativeEndian.AppendUint32(keys, k)
	}
	return keys
}

func (s *Seat) sendModifiers(kb *sw.WlKeyboard) error {
	m := s.state.Modifiers()
	return kb.SendModifiers(s.display.NextSerial(), m.Depressed, m.Latched, m.Locked, m.Group)
}

func (s *Seat) enterKeyboard(kb *sw.WlKeyboard) error {
	if err := kb.SendEnter(s.display.NextSerial(), s.keyboard.focus.Resource(), s.keysArray()); err != nil {
		return err
	}
	return s.sendModifiers(kb)
}

func (s *Seat) SetKeyboardFocus(target *surface.Surface) error {
	if target == s.keyboard.focus {
		return nil
	}
	var errs []error
	if old := s.keyboard.focus; old != nil {
		if r := s.lookup(old.Client()); r != nil {
			for _, kb := range r.keyboards {
				errs = append(errs, kb.SendLeave(s.display.NextSerial(), old.Resource()))
			}
		}
	}
	s.keyboard.focus = target
	errs = append(errs, s.listener.KeyboardFocusChanged(target))
	if target != nil {
		if r := s.lookup(target.Client()); r != nil {
			for _, kb := range r.keyboards {
				errs = append(errs, s.enterKeyboard(kb))
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Seat) DepressedModifiers() uint32 {
	return s.state.Modifiers().Depressed
}

func (s *Seat) Key(code uint32, pressed bool, at time.Duration) error {
	k := &s.keyboard
	i := slices.Index(k.pressed, code)
	switch {
	case pressed && i >= 0, !pressed && i < 0:
		return nil
	case pressed:
		k.pressed = append(k.pressed, code)
	default:
		k.pressed = slices.Delete(k.pressed, i, i+1)
	}
	modsChanged := s.state.Key(code, pressed)
	if grabbed, err := s.grabKey(code, pressed, modsChanged, at); grabbed {
		return err
	}
	return s.sendKey(nil, code, pressed, modsChanged, at)
}

func (s *Seat) sendKey(v *VirtualKeyboard, code uint32, pressed, modsChanged bool, at time.Duration) error {
	c, r := s.resourcesAt(s.keyboard.focus)
	if r == nil {
		return nil
	}
	state := sw.WlKeyboardKeyStateReleased
	if pressed {
		state = sw.WlKeyboardKeyStatePressed
	}
	ms := wire.Millis(at)
	serial := s.eventSerial(c, pressed)
	var errs []error
	for _, kb := range r.keyboards {
		errs = append(errs, s.useKeymap(kb, v), kb.SendKey(serial, ms, code, state))
		if modsChanged {
			errs = append(errs, s.sendModifiers(kb))
		}
	}
	return errors.Join(errs...)
}

func (s *Seat) sendKeyboardConfig(kb *sw.WlKeyboard) error {
	if err := kb.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, s.cfg.Keymap.FD(), s.cfg.Keymap.Size()); err != nil {
		return err
	}
	if kb.Base().Version() < 4 {
		return nil
	}
	return kb.SendRepeatInfo(s.cfg.RepeatRate, s.cfg.RepeatDelay)
}

type Pending struct {
	cfg   Config
	state *xkb.State
}

func Prepare(cfg Config) (Pending, error) {
	if cfg.Keymap == nil {
		return Pending{}, errors.New("seat: no keymap")
	}
	st, err := cfg.Keymap.NewState()
	if err != nil {
		return Pending{}, err
	}
	return Pending{cfg: cfg, state: st}, nil
}

func (s *Seat) Reconfigure(p Pending) error {
	for _, code := range s.keyboard.pressed {
		p.state.Key(code, true)
	}
	s.state.Close()
	s.state, s.cfg = p.state, p.cfg
	clear(s.keymaps)
	var errs []error
	if g := s.keyboard.grab; g != nil {
		errs = append(errs, g.Keymap(s.cfg.Keymap.FD(), s.cfg.Keymap.Size()), g.RepeatInfo(s.cfg.RepeatRate, s.cfg.RepeatDelay))
	}
	for _, r := range s.clients {
		focused := s.keyboard.focus != nil && s.keyboard.focus.Client() == r.client
		for _, kb := range r.keyboards {
			errs = append(errs, s.sendKeyboardConfig(kb))
			if focused {
				errs = append(errs, s.sendModifiers(kb))
			}
		}
	}
	return errors.Join(errs...)
}

func (r seatRequests) GetKeyboard(obj *sw.WlSeat, kb *sw.WlKeyboard) (sw.WlKeyboardHandler, error) {
	s := r.s
	if !s.everCaps.Has(sw.WlSeatCapabilityKeyboard) {
		return nil, missing(obj, "keyboard")
	}
	res := s.resources(r.c)
	track(s, res, &res.keyboards, kb)
	server.OnDestroy(kb, func() error {
		delete(s.keymaps, kb)
		return nil
	})
	if err := s.sendKeyboardConfig(kb); err != nil {
		return nil, err
	}
	if s.keyboard.focus != nil && s.keyboard.focus.Client() == r.c {
		if err := s.enterKeyboard(kb); err != nil {
			return nil, err
		}
	}
	return keyboardRequests{}, nil
}

type keyboardRequests struct{}

func (keyboardRequests) Release(*sw.WlKeyboard) error {
	return nil
}

func (s *Seat) Keysym(code uint32) uint32 {
	return s.state.Keysym(code)
}

func (s *Seat) Layout() uint32 {
	return s.state.Modifiers().Group
}
