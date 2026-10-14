package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/testenv"
)

func TestParseDebug(t *testing.T) {
	if d, err := parseDebug(""); err != nil || d.wire {
		t.Fatalf("empty = %+v, %v", d, err)
	}
	if d, err := parseDebug("wire"); err != nil || !d.wire {
		t.Fatalf("wire = %+v, %v", d, err)
	}
	if _, err := parseDebug("wire,nope"); err == nil {
		t.Fatal("unknown category accepted")
	}
}

func TestChooseBackend(t *testing.T) {
	cases := []struct {
		requested, display, want string
	}{
		{"", "wayland-1", "nested"},
		{"", "", "headless"},
		{"headless", "wayland-1", "headless"},
		{"nested", "", "nested"},
	}
	for _, c := range cases {
		if got, err := chooseBackend(c.requested, c.display); err != nil || got != c.want {
			t.Errorf("chooseBackend(%q, %q) = %q, %v; want %q", c.requested, c.display, got, err, c.want)
		}
	}
	if _, err := chooseBackend("drm", ""); err == nil {
		t.Error("an unknown backend was accepted")
	}
}

func TestConfigPath(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"XDG_CONFIG_HOME": "/cfg", "HOME": "/home/u"}, "/cfg/tilde/config.yaml"},
		{map[string]string{"XDG_CONFIG_HOME": "relative", "HOME": "/home/u"}, "/home/u/.config/tilde/config.yaml"},
		{map[string]string{"HOME": "/home/u"}, "/home/u/.config/tilde/config.yaml"},
	}
	for _, c := range cases {
		if got, err := configPath(func(k string) string { return c.env[k] }); err != nil || got != c.want {
			t.Errorf("%v: %q, %v; want %q", c.env, got, err, c.want)
		}
	}
	if _, err := configPath(func(string) string { return "" }); err == nil {
		t.Error("no HOME resolved a config path")
	}
}

func TestFontPath(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if got := fontPath("/a.ttf", env("/b.ttf")); got != "/a.ttf" {
		t.Errorf("the config font lost to %q", got)
	}
	if got := fontPath("", env("/b.ttf")); got != "/b.ttf" {
		t.Errorf("TILDE_FONT lost to %q", got)
	}
	if got := fontPath("", env("")); got != defaultFont {
		t.Errorf("the built-in font lost to %q", got)
	}
}

func testSource(t *testing.T, path string, explicit bool) source {
	t.Helper()
	testenv.Font(t)
	return source{path: path, explicit: explicit, lib: testenv.XKBLibrary(t), cursor: compositor.CursorConfig{Size: 24}, getenv: os.Getenv}
}

func TestLoadingTheConfig(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	first, err := testSource(t, missing, false).load()
	if err != nil {
		t.Fatalf("a missing default config: %v", err)
	}
	if len(first.cfg.Binds) == 0 || first.cfg.Cursor.Size != 24 || len(first.env) == 0 {
		t.Errorf("the built-in config gave %d binds, cursor %+v, env %v", len(first.cfg.Binds), first.cfg.Cursor, first.env)
	}
	if err := first.keymap.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := testSource(t, missing, true).load(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing --config file: %v", err)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("binds:\n  - chord: SUPER + Nope\n    action: kill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := testSource(t, bad, true).load(); err == nil || !strings.HasPrefix(err.Error(), bad+":2: chord: bad chord") {
		t.Errorf("an unknown key: %v", err)
	}
	layout := filepath.Join(dir, "layout.yaml")
	if err := os.WriteFile(layout, []byte("input:\n  keyboard:\n    layout: no-such-layout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := testSource(t, layout, true).load(); err == nil || !strings.Contains(err.Error(), "no-such-layout") {
		t.Errorf("an unknown layout: %v", err)
	}
}

func TestSessionVars(t *testing.T) {
	env := map[string]string{"WAYLAND_DISPLAY": "wayland-1", "TILDE_SOCKET": "/run/t.sock"}
	got := sessionVars([][2]string{{"XDG_CURRENT_DESKTOP", "tilde"}}, func(k string) string { return env[k] })
	want := [][2]string{{"XDG_CURRENT_DESKTOP", "tilde"}, {"WAYLAND_DISPLAY", "wayland-1"}, {"TILDE_SOCKET", "/run/t.sock"}}
	if !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}
