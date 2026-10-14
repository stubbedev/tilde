package xkb_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/testenv"
	"github.com/stubbedev/tilde/internal/xkb"
)

var layouts = xkb.Names{Layout: "us,dk,es", Options: "grp:toggle"}

func compile(t *testing.T) *xkb.Keymap {
	t.Helper()
	km, err := testenv.XKBLibrary(t).Compile(layouts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = km.Close() })
	return km
}

func state(t *testing.T) *xkb.State {
	t.Helper()
	st, err := compile(t).NewState()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

const (
	keyLeftShift = 42
	keyRightAlt  = 100
	keyA         = 30
)

func TestModifiersAndLayoutGroups(t *testing.T) {
	st := state(t)
	if !st.Key(keyLeftShift, true) || st.Modifiers().Depressed == 0 {
		t.Fatalf("shift press: %+v", st.Modifiers())
	}
	if !st.Key(keyLeftShift, false) || st.Modifiers().Depressed != 0 {
		t.Fatalf("shift release: %+v", st.Modifiers())
	}
	var groups []uint32
	for range 3 {
		st.Key(keyRightAlt, true)
		st.Key(keyRightAlt, false)
		groups = append(groups, st.Modifiers().Group)
	}
	if !slices.Equal(groups, []uint32{1, 2, 0}) {
		t.Fatalf("grp:toggle cycled through groups %v, want 1, 2, 0", groups)
	}
	if st.Key(keyA, true) {
		t.Fatal("pressing a letter reported a modifier change")
	}
}

func TestKeymapFDIsSealedAndComplete(t *testing.T) {
	km := compile(t)
	seals, err := unix.FcntlInt(uintptr(km.FD()), unix.F_GET_SEALS, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE | unix.F_SEAL_SEAL
	if seals&want != want {
		t.Fatalf("seals %#x, want %#x", seals, want)
	}
	m, err := unix.Mmap(km.FD(), 0, int(km.Size()), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Munmap(m)
	if string(m) != km.Text()+"\x00" {
		t.Fatal("the fd does not hold the NUL-terminated keymap text")
	}
	if _, err := unix.Write(km.FD(), []byte("x")); err == nil {
		t.Fatal("the keymap fd accepted a write")
	}
}

func TestInvalidNamesAndLibraries(t *testing.T) {
	if _, err := testenv.XKBLibrary(t).Compile(xkb.Names{Layout: "no-such-layout"}); err == nil {
		t.Fatal("an unknown layout compiled")
	}
	if _, err := xkb.Load("/nonexistent/libxkbcommon.so.0"); err == nil {
		t.Fatal("a missing library loaded")
	}
}

func TestEnvironment(t *testing.T) {
	env := map[string]string{"XKB_DEFAULT_LAYOUT": "us,dk", "XKB_DEFAULT_OPTIONS": "grp:toggle", xkb.LibraryEnv: "/lib/x.so"}
	getenv := func(k string) string { return env[k] }
	if got := xkb.NamesFromEnv(getenv); got != (xkb.Names{Layout: "us,dk", Options: "grp:toggle"}) {
		t.Fatalf("names %+v", got)
	}
	if got := xkb.LibraryPath(getenv); got != "/lib/x.so" {
		t.Fatalf("library %q", got)
	}
}

func TestLibxkbcommonClientsReadAllThreeLayouts(t *testing.T) {
	xkbcli := testenv.Require(t, "xkbcli")
	km := compile(t)
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(args ...string) string {
		var stderr bytes.Buffer
		cmd := exec.CommandContext(context.Background(), xkbcli, args...)
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("xkbcli %v: %v\n%s", args, err, stderr.String())
		}
		return string(out)
	}
	served := write("served.xkb", km.Text())
	once := run("compile-keymap", "--format", "1", "--keymap", served)
	twice := run("compile-keymap", "--format", "1", "--keymap", write("once.xkb", once))
	if once != twice {
		t.Fatal("re-serializing the served keymap through libxkbcommon is not stable")
	}
	for _, c := range []struct{ cp, key, layout string }{
		{"0xe6", "AC10", "Danish"},
		{"0xf8", "AC11", "Danish"},
		{"0xe5", "AD11", "Danish"},
		{"0xf1", "AC10", "Spanish"},
		{"0xe7", "BKSL", "Spanish"},
	} {
		out := run("how-to-type", "--format", "1", "--keymap="+served, c.cp)
		found := false
		for line := range strings.SplitSeq(out, "\n") {
			if f := strings.Fields(line); len(f) > 3 && f[1] == c.key && strings.Contains(line, c.layout) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not typed on %s in the %s layout:\n%s", c.cp, c.key, c.layout, out)
		}
	}
}

func TestKeycodesFromTheFirstLayout(t *testing.T) {
	km := compile(t)
	for name, want := range map[string]uint32{"q": 16, "Q": 16, "bracketleft": 26, "Return": 28, "exclam": 2, "XF86AudioRaiseVolume": 115} {
		got, err := km.Keycode(name)
		if err != nil || got != want {
			t.Errorf("Keycode(%q) = %d, %v; want %d", name, got, err, want)
		}
	}
	if _, err := km.Keycode("not_a_key"); !errors.Is(err, xkb.ErrUnknownKeysym) {
		t.Errorf("an unknown keysym: %v", err)
	}
	if _, err := km.Keycode("aring"); !errors.Is(err, xkb.ErrKeysymNotMapped) {
		t.Errorf("å is only on the dk layout: %v", err)
	}
	for _, name := range []string{"Shift", "Control", "Mod1", "Mod4"} {
		if m, err := km.ModMask(name); err != nil || m == 0 {
			t.Errorf("ModMask(%q) = %#x, %v", name, m, err)
		}
	}
	if _, err := km.ModMask("Hyper9"); !errors.Is(err, xkb.ErrUnknownModifier) {
		t.Errorf("an unknown modifier: %v", err)
	}
}
