// Package binds maps typed input chords to window manager actions, with
// submaps and bind flags.
package binds

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/stubbedev/tilde/internal/wm"
)

type Resolver interface {
	Keycode(keysym string) (uint32, error)
	ModMask(name string) (uint32, error)
}

type Trigger interface {
	fmt.Stringer
	trigger()
}

type (
	Key       struct{ Code uint32 }
	Button    struct{ Code uint32 }
	ScrollDir uint8
	Scroll    struct{ Dir ScrollDir }
)

const (
	ScrollUp ScrollDir = iota + 1
	ScrollDown
)

func (Key) trigger()    {}
func (Button) trigger() {}
func (Scroll) trigger() {}

func (k Key) String() string    { return fmt.Sprintf("key %d", k.Code) }
func (b Button) String() string { return fmt.Sprintf("mouse:%d", b.Code) }
func (s Scroll) String() string {
	if s.Dir == ScrollUp {
		return "mouse_up"
	}
	return "mouse_down"
}

type Chord struct {
	Mods    uint32
	Trigger Trigger
}

type Flags struct {
	Locked    bool
	Repeating bool
}

type Bind struct {
	Submap string
	Chord  Chord
	Flags  Flags
	Action wm.Action
}

var ErrBadChord = errors.New("bad chord")

var modifiers = map[string]string{
	"SUPER": "Mod4", "MOD4": "Mod4", "WIN": "Mod4", "LOGO": "Mod4",
	"SHIFT":   "Shift",
	"CTRL":    "Control",
	"CONTROL": "Control",
	"ALT":     "Mod1", "MOD1": "Mod1",
}

func Relevant(r Resolver) (uint32, error) {
	var mask uint32
	for _, name := range []string{"Shift", "Control", "Mod1", "Mod4"} {
		m, err := r.ModMask(name)
		if err != nil {
			return 0, err
		}
		mask |= m
	}
	return mask, nil
}

func ParseChord(text string, r Resolver) (Chord, error) {
	parts := strings.Split(text, "+")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	last := parts[len(parts)-1]
	if last == "" {
		return Chord{}, fmt.Errorf("%w %q: no key", ErrBadChord, text)
	}
	var c Chord
	for _, p := range parts[:len(parts)-1] {
		name, ok := modifiers[strings.ToUpper(p)]
		if !ok {
			return Chord{}, fmt.Errorf("%w %q: unknown modifier %q", ErrBadChord, text, p)
		}
		m, err := r.ModMask(name)
		if err != nil {
			return Chord{}, fmt.Errorf("%w %q: %w", ErrBadChord, text, err)
		}
		c.Mods |= m
	}
	switch lower := strings.ToLower(last); {
	case lower == "mouse_up":
		c.Trigger = Scroll{ScrollUp}
	case lower == "mouse_down":
		c.Trigger = Scroll{ScrollDown}
	case strings.HasPrefix(lower, "mouse:"):
		code, err := strconv.ParseUint(last[len("mouse:"):], 10, 32)
		if err != nil {
			return Chord{}, fmt.Errorf("%w %q: bad button %q", ErrBadChord, text, last)
		}
		c.Trigger = Button{uint32(code)}
	default:
		code, err := r.Keycode(last)
		if err != nil {
			return Chord{}, fmt.Errorf("%w %q: %w", ErrBadChord, text, err)
		}
		c.Trigger = Key{code}
	}
	return c, nil
}

type Map struct {
	binds    []Bind
	relevant uint32
}

func Duplicates(binds []Bind) [][2]int {
	type key struct {
		submap string
		chord  Chord
	}
	first := map[key]int{}
	var dups [][2]int
	for i, b := range binds {
		k := key{b.Submap, b.Chord}
		if j, ok := first[k]; ok {
			dups = append(dups, [2]int{j, i})
			continue
		}
		first[k] = i
	}
	return dups
}

func NewMap(binds []Bind, relevant uint32) (Map, error) {
	if dups := Duplicates(binds); len(dups) > 0 {
		a, b := binds[dups[0][0]], binds[dups[0][1]]
		return Map{}, fmt.Errorf("binds: %s and %s share a chord in submap %q", a.Action, b.Action, b.Submap)
	}
	return Map{binds: binds, relevant: relevant}, nil
}

func (m Map) Match(submap string, mods uint32, t Trigger, locked bool) (Bind, bool) {
	mods &= m.relevant
	for _, b := range m.binds {
		if b.Submap == submap && b.Chord.Mods == mods && b.Chord.Trigger == t && (!locked || b.Flags.Locked) {
			return b, true
		}
	}
	return Bind{}, false
}
