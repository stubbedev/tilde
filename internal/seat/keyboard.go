package seat

import (
	"encoding/binary"
	"errors"
	"slices"
	"time"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

type keyboardState struct {
	focus   *surface.Surface
	pressed []uint32
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

func (s *Seat) sendModifiers(c *server.Client, kb *sw.WlKeyboard) error {
	m := s.state.Modifiers()
	return c.Sent(kb.SendModifiers(s.display.NextSerial(), m.Depressed, m.Latched, m.Locked, m.Group))
}

func (s *Seat) enterKeyboard(c *server.Client, kb *sw.WlKeyboard) error {
	if err := c.Sent(kb.SendEnter(s.display.NextSerial(), s.keyboard.focus.Resource(), s.keysArray())); err != nil {
		return err
	}
	return s.sendModifiers(c, kb)
}

func (s *Seat) SetKeyboardFocus(target *surface.Surface) error {
	if target == s.keyboard.focus {
		return nil
	}
	var errs []error
	if old := s.keyboard.focus; old != nil {
		if r := s.lookup(old.Client()); r != nil {
			for _, kb := range r.keyboards {
				errs = append(errs, old.Client().Sent(kb.SendLeave(s.display.NextSerial(), old.Resource())))
			}
		}
	}
	s.keyboard.focus = target
	if target != nil {
		if r := s.lookup(target.Client()); r != nil {
			for _, kb := range r.keyboards {
				errs = append(errs, s.enterKeyboard(target.Client(), kb))
			}
		}
	}
	return errors.Join(errs...)
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
	if k.focus == nil {
		return nil
	}
	c := k.focus.Client()
	r := s.lookup(c)
	if r == nil {
		return nil
	}
	state := sw.WlKeyboardKeyStateReleased
	if pressed {
		state = sw.WlKeyboardKeyStatePressed
	}
	ms := wire.Millis(at)
	var errs []error
	for _, kb := range r.keyboards {
		errs = append(errs, c.Sent(kb.SendKey(s.display.NextSerial(), ms, code, state)))
		if modsChanged {
			errs = append(errs, s.sendModifiers(c, kb))
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
	if err := kb.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, s.cfg.Keymap.FD(), s.cfg.Keymap.Size()); err != nil {
		return nil, err
	}
	if kb.Base().Version() >= 4 {
		if err := kb.SendRepeatInfo(s.cfg.RepeatRate, s.cfg.RepeatDelay); err != nil {
			return nil, err
		}
	}
	if s.keyboard.focus != nil && s.keyboard.focus.Client() == r.c {
		if err := s.enterKeyboard(r.c, kb); err != nil {
			return nil, err
		}
	}
	return keyboardRequests{}, nil
}

type keyboardRequests struct{}

func (keyboardRequests) Release(*sw.WlKeyboard) error {
	return nil
}
