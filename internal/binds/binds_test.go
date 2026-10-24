package binds

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stubbedev/lys-wm/internal/wm"
)

type fakeKeymap struct{}

var fakeMods = map[string]uint32{"Shift": 1, "Control": 4, "Mod1": 8, "Mod4": 64}

func (fakeKeymap) ModMask(name string) (uint32, error) {
	if m, ok := fakeMods[name]; ok {
		return m, nil
	}
	return 0, fmt.Errorf("no modifier %s", name)
}

func (fakeKeymap) Keycode(name string) (uint32, error) {
	codes := map[string]uint32{"Q": 16, "q": 16, "Return": 28, "bracketleft": 26}
	if c, ok := codes[name]; ok {
		return c, nil
	}
	return 0, fmt.Errorf("no keysym %s", name)
}

func TestParseChord(t *testing.T) {
	cases := map[string]Chord{
		"SUPER + SHIFT + Q":   {Mods: 65, Trigger: Key{16}},
		"super+q":             {Mods: 64, Trigger: Key{16}},
		"CTRL + ALT + Return": {Mods: 12, Trigger: Key{28}},
		"SUPER + mouse:272":   {Mods: 64, Trigger: Button{272}},
		"SUPER + mouse_down":  {Mods: 64, Trigger: Scroll{ScrollDown}},
		"bracketleft":         {Trigger: Key{26}},
	}
	for text, want := range cases {
		got, err := ParseChord(text, fakeKeymap{})
		if err != nil || got != want {
			t.Errorf("ParseChord(%q) = %+v, %v; want %+v", text, got, err, want)
		}
	}
	for _, bad := range []string{"SUPER +", "HYPER + Q", "SUPER + nope", "SUPER + mouse:x"} {
		if _, err := ParseChord(bad, fakeKeymap{}); !errors.Is(err, ErrBadChord) {
			t.Errorf("ParseChord(%q) = %v, want ErrBadChord", bad, err)
		}
	}
}

func TestMatch(t *testing.T) {
	kill := Bind{Chord: Chord{Mods: 65, Trigger: Key{16}}, Action: wm.KillAction{}}
	volume := Bind{Chord: Chord{Trigger: Key{115}}, Flags: Flags{Locked: true}, Action: wm.ExecAction{Command: "vol +"}}
	resizeExit := Bind{Submap: "resize", Chord: Chord{Trigger: Key{28}}, Action: wm.SubmapAction{}}
	m, err := NewMap([]Bind{kill, volume, resizeExit}, 1|4|8|64)
	if err != nil {
		t.Fatal(err)
	}
	const capsAndNum = 2 | 16
	if b, ok := m.Match("", 65|capsAndNum, Key{16}, false); !ok || b.Action != (wm.KillAction{}) {
		t.Fatal("caps and num lock got in the way of a bind")
	}
	if _, ok := m.Match("", 64, Key{16}, false); ok {
		t.Fatal("a chord matched with a modifier missing")
	}
	if _, ok := m.Match("", 0, Key{28}, false); ok {
		t.Fatal("a submap bind matched outside its submap")
	}
	if _, ok := m.Match("resize", 0, Key{28}, false); !ok {
		t.Fatal("a submap bind did not match inside its submap")
	}
	if _, ok := m.Match("", 65, Key{16}, true); ok {
		t.Fatal("a bind without the locked flag matched while locked")
	}
	if _, ok := m.Match("", 0, Key{115}, true); !ok {
		t.Fatal("a locked bind did not match while locked")
	}
	if _, err := NewMap([]Bind{kill, kill}, 0xff); err == nil {
		t.Fatal("two binds on one chord were accepted")
	}
}
