// Package xkb compiles keymaps and tracks modifier and layout state with
// libxkbcommon, loaded at runtime through purego, and serves keymaps to
// clients as sealed memfds.
package xkb

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

var defaultLibrary = "libxkbcommon.so.0"

const (
	LibraryEnv  = "TILDE_LIBXKBCOMMON"
	libc        = "libc.so.6"
	evdevOffset = 8

	contextNoEnvironmentNames = 1 << 1
	keymapFormatTextV1        = 1
	keyUp                     = 0
	keyDown                   = 1
	modsDepressed             = 1 << 0
	modsLatched               = 1 << 1
	modsLocked                = 1 << 2
	layoutEffective           = 1 << 7
)

func LibraryPath(getenv func(string) string) string {
	if p := getenv(LibraryEnv); p != "" {
		return p
	}
	return defaultLibrary
}

type Names struct {
	Rules, Model, Layout, Variant, Options string
}

func NamesFromEnv(getenv func(string) string) Names {
	return Names{
		Rules:   getenv("XKB_DEFAULT_RULES"),
		Model:   getenv("XKB_DEFAULT_MODEL"),
		Layout:  getenv("XKB_DEFAULT_LAYOUT"),
		Variant: getenv("XKB_DEFAULT_VARIANT"),
		Options: getenv("XKB_DEFAULT_OPTIONS"),
	}
}

func (n Names) withDefaults() Names {
	if n.Rules == "" {
		n.Rules = "evdev"
	}
	if n.Model == "" {
		n.Model = "pc105"
	}
	if n.Layout == "" {
		n.Layout = "us"
	}
	return n
}

type ruleNames struct {
	rules, model, layout, variant, options *byte
}

func cString(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

type Library struct {
	context         uintptr
	contextUnref    func(ctx uintptr)
	keymapNew       func(ctx uintptr, names *ruleNames, flags int32) uintptr
	keymapString    func(km uintptr, format int32) unsafe.Pointer
	keymapUnref     func(km uintptr)
	stateNew        func(km uintptr) uintptr
	stateUnref      func(st uintptr)
	updateKey       func(st uintptr, key uint32, direction int32) int32
	serializeMods   func(st uintptr, components int32) uint32
	serializeLayout func(st uintptr, components int32) uint32
	strlen          func(s unsafe.Pointer) uintptr
	free            func(p unsafe.Pointer)
}

func Load(path string) (*Library, error) {
	handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("xkb: load libxkbcommon from %s (set %s to its path): %w", path, LibraryEnv, err)
	}
	c, err := purego.Dlopen(libc, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("xkb: load %s: %w", libc, err)
	}
	l := &Library{}
	var contextNew func(flags int32) uintptr
	for _, f := range []struct {
		lib  uintptr
		fn   any
		name string
	}{
		{handle, &contextNew, "xkb_context_new"},
		{handle, &l.contextUnref, "xkb_context_unref"},
		{handle, &l.keymapNew, "xkb_keymap_new_from_names"},
		{handle, &l.keymapString, "xkb_keymap_get_as_string"},
		{handle, &l.keymapUnref, "xkb_keymap_unref"},
		{handle, &l.stateNew, "xkb_state_new"},
		{handle, &l.stateUnref, "xkb_state_unref"},
		{handle, &l.updateKey, "xkb_state_update_key"},
		{handle, &l.serializeMods, "xkb_state_serialize_mods"},
		{handle, &l.serializeLayout, "xkb_state_serialize_layout"},
		{c, &l.strlen, "strlen"},
		{c, &l.free, "free"},
	} {
		if _, err := purego.Dlsym(f.lib, f.name); err != nil {
			return nil, fmt.Errorf("xkb: %s lacks %s: %w", path, f.name, err)
		}
		purego.RegisterLibFunc(f.fn, f.lib, f.name)
	}
	if l.context = contextNew(contextNoEnvironmentNames); l.context == 0 {
		return nil, errors.New("xkb: xkb_context_new failed")
	}
	return l, nil
}

func (l *Library) Close() {
	l.contextUnref(l.context)
}

type Keymap struct {
	lib    *Library
	keymap uintptr
	text   string
	fd     int
	size   uint32
}

func (l *Library) Compile(names Names) (*Keymap, error) {
	names = names.withDefaults()
	rn := &ruleNames{cString(names.Rules), cString(names.Model), cString(names.Layout), cString(names.Variant), cString(names.Options)}
	km := l.keymapNew(l.context, rn, 0)
	runtime.KeepAlive(rn)
	if km == 0 {
		return nil, fmt.Errorf("xkb: libxkbcommon cannot compile a keymap from %+v", names)
	}
	p := l.keymapString(km, keymapFormatTextV1)
	if p == nil {
		l.keymapUnref(km)
		return nil, fmt.Errorf("xkb: libxkbcommon cannot serialize the keymap for %+v", names)
	}
	text := strings.Clone(unsafe.String((*byte)(p), l.strlen(p))) //nolint:gosec // p is a NUL-terminated string libxkbcommon allocated
	l.free(p)
	if len(text) >= math.MaxUint32 {
		l.keymapUnref(km)
		return nil, fmt.Errorf("xkb: the keymap for %+v is %d bytes, too large for wl_keyboard", names, len(text))
	}
	fd, err := sealedMemfd(text + "\x00")
	if err != nil {
		l.keymapUnref(km)
		return nil, err
	}
	return &Keymap{lib: l, keymap: km, text: text, fd: fd, size: uint32(len(text) + 1)}, nil //nolint:gosec // bounded by the check above
}

func sealedMemfd(content string) (int, error) {
	fd, err := unix.MemfdCreate("tilde-keymap", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return -1, fmt.Errorf("xkb: memfd_create: %w", err)
	}
	for b := []byte(content); len(b) > 0; {
		n, err := unix.Write(fd, b)
		if err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("xkb: write keymap: %w", err)
		}
		b = b[n:]
	}
	seals := unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE | unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, seals); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("xkb: seal keymap: %w", err)
	}
	return fd, nil
}

func (k *Keymap) FD() int {
	return k.fd
}

func (k *Keymap) Size() uint32 {
	return k.size
}

func (k *Keymap) Text() string {
	return k.text
}

func (k *Keymap) Close() error {
	k.lib.keymapUnref(k.keymap)
	if err := unix.Close(k.fd); err != nil {
		return fmt.Errorf("xkb: close keymap fd %d: %w", k.fd, err)
	}
	return nil
}

type Modifiers struct {
	Depressed, Latched, Locked, Group uint32
}

type State struct {
	lib   *Library
	state uintptr
}

func (k *Keymap) NewState() (*State, error) {
	st := k.lib.stateNew(k.keymap)
	if st == 0 {
		return nil, errors.New("xkb: xkb_state_new failed")
	}
	return &State{lib: k.lib, state: st}, nil
}

func (s *State) Close() {
	s.lib.stateUnref(s.state)
}

func (s *State) Key(evdev uint32, pressed bool) bool {
	direction := int32(keyUp)
	if pressed {
		direction = keyDown
	}
	changed := s.lib.updateKey(s.state, evdev+evdevOffset, direction)
	return changed&(modsDepressed|modsLatched|modsLocked|layoutEffective) != 0
}

func (s *State) Modifiers() Modifiers {
	return Modifiers{
		Depressed: s.lib.serializeMods(s.state, modsDepressed),
		Latched:   s.lib.serializeMods(s.state, modsLatched),
		Locked:    s.lib.serializeMods(s.state, modsLocked),
		Group:     s.lib.serializeLayout(s.state, layoutEffective),
	}
}
