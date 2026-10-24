package seat

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"

	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
)

type VirtualKeyboard struct {
	fd      int
	size    uint32
	mods    [4]uint32
	version uint64
}

type sentKeymap struct {
	v       *VirtualKeyboard
	version uint64
}

func (v *VirtualKeyboard) sent() sentKeymap {
	if v == nil {
		return sentKeymap{}
	}
	return sentKeymap{v: v, version: v.version}
}

func NewVirtualKeyboard() *VirtualKeyboard {
	return &VirtualKeyboard{fd: -1}
}

func (v *VirtualKeyboard) HasKeymap() bool {
	return v.fd >= 0
}

func (v *VirtualKeyboard) SetKeymap(fd int, size uint32) error {
	err := v.Close()
	v.fd, v.size = fd, size
	v.version++
	return err
}

func (v *VirtualKeyboard) Close() error {
	if v.fd < 0 {
		return nil
	}
	err := unix.Close(v.fd)
	v.fd = -1
	if err != nil {
		return fmt.Errorf("seat: close a virtual keymap: %w", err)
	}
	return nil
}

func (s *Seat) useKeymap(kb *sw.WlKeyboard, v *VirtualKeyboard) error {
	if s.keymaps[kb] == v.sent() {
		return nil
	}
	s.keymaps[kb] = v.sent()
	if v == nil {
		return errors.Join(kb.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, s.cfg.Keymap.FD(), s.cfg.Keymap.Size()), s.sendModifiers(kb))
	}
	return errors.Join(kb.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, v.fd, v.size), v.sendModifiers(s, kb))
}

func (v *VirtualKeyboard) sendModifiers(s *Seat, kb *sw.WlKeyboard) error {
	return kb.SendModifiers(s.display.NextSerial(), v.mods[0], v.mods[1], v.mods[2], v.mods[3])
}

func (s *Seat) focusedKeyboards() []*sw.WlKeyboard {
	if _, r := s.resourcesAt(s.keyboard.focus); r != nil {
		return r.keyboards
	}
	return nil
}

func (s *Seat) VirtualKey(v *VirtualKeyboard, code uint32, pressed bool, at time.Duration) error {
	if !v.HasKeymap() {
		return errors.New("seat: a virtual key without a keymap")
	}
	return s.sendKey(v, code, pressed, false, at)
}

func (s *Seat) VirtualModifiers(v *VirtualKeyboard, depressed, latched, locked, group uint32) error {
	v.mods = [4]uint32{depressed, latched, locked, group}
	kbs := s.focusedKeyboards()
	var errs []error
	for _, kb := range kbs {
		if s.keymaps[kb].v == v {
			errs = append(errs, v.sendModifiers(s, kb))
		}
	}
	return errors.Join(errs...)
}
