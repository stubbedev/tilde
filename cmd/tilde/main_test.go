package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/ipc"
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
		requested, display string
		want               backendKind
	}{
		{"", "wayland-1", nestedBackend},
		{"", "", drmBackend},
		{"headless", "wayland-1", headlessBackend},
		{"nested", "", nestedBackend},
		{"drm", "wayland-1", drmBackend},
	}
	for _, c := range cases {
		if got, err := chooseBackend(c.requested, c.display); err != nil || got != c.want {
			t.Errorf("chooseBackend(%q, %q) = %v, %v; want %v", c.requested, c.display, got, err, c.want)
		}
	}
	if _, err := chooseBackend("vnc", ""); err == nil {
		t.Error("an unknown backend was accepted")
	}
	if drmBackend.String() != "drm" {
		t.Errorf("drm backend is called %q", drmBackend)
	}
}

func TestChooseRenderer(t *testing.T) {
	for _, c := range []struct {
		requested string
		backend   backendKind
		want      rendererKind
	}{{"", nestedBackend, glesRenderer}, {"", headlessBackend, softRenderer}, {"", drmBackend, glesRenderer}, {"soft", nestedBackend, softRenderer}, {"gles", headlessBackend, glesRenderer}} {
		if got, err := chooseRenderer(c.requested, c.backend); err != nil || got != c.want {
			t.Errorf("chooseRenderer(%q, %v) = %v, %v; want %v", c.requested, c.backend, got, err, c.want)
		}
	}
	for _, c := range []struct {
		requested string
		backend   backendKind
	}{{"vulkan", nestedBackend}, {"soft", drmBackend}} {
		if _, err := chooseRenderer(c.requested, c.backend); err == nil {
			t.Errorf("chooseRenderer(%q, %v) succeeded", c.requested, c.backend)
		}
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
	return source{path: path, explicit: explicit, lib: testenv.XKBLibrary(t), getenv: os.Getenv}
}

func TestLoadingTheConfig(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	first, err := testSource(t, missing, false).load()
	if err != nil {
		t.Fatalf("a missing default config: %v", err)
	}
	if len(first.cfg.Binds) == 0 || len(first.env) == 0 {
		t.Errorf("the built-in config gave %d binds, env %v", len(first.cfg.Binds), first.env)
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

func TestTheConfigEnvChoosesTheCursor(t *testing.T) {
	process := func(name string) string {
		return map[string]string{"XCURSOR_SIZE": "32", "XCURSOR_PATH": t.TempDir()}[name]
	}
	cursor, err := cursorConfig(withEnv([][2]string{{"XCURSOR_SIZE", "48"}}, process))
	if err != nil || cursor.Size != 48 {
		t.Fatalf("cursor %+v (%v), want the size 48 the config env sets over the process's 32", cursor, err)
	}
	if cursor, err := cursorConfig(withEnv(nil, process)); err != nil || cursor.Size != 32 {
		t.Fatalf("cursor %+v (%v), want the process's size 32 without a config env", cursor, err)
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

func outputScale(t *testing.T, socket string) float64 {
	t.Helper()
	raw, err := ipc.Do(socket, ipc.Request{Request: ipc.KindOutputs})
	if err != nil {
		t.Fatal(err)
	}
	var outputs []ipc.Output
	if err := json.Unmarshal(raw, &outputs); err != nil || len(outputs) != 1 {
		t.Fatalf("outputs %s: %v", raw, err)
	}
	return outputs[0].Scale
}

const noXwayland = "xwayland:\n  enabled: false\n"

func startSession(t *testing.T, config string) (string, string, <-chan error) {
	t.Helper()
	testenv.Font(t)
	testenv.XKBLibrary(t)
	dir := testenv.SocketDir(t)
	runtime := filepath.Join(dir, "runtime")
	if err := os.Mkdir(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"WAYLAND_DISPLAY", ipc.SocketEnv, "XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE"} {
		t.Setenv(name, "")
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(noXwayland+config), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- run([]string{"--backend", "headless", "--config", path}) }()
	getenv := func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return runtime
		}
		return ""
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		socket, err := ipc.FindSocket(getenv)
		if err == nil {
			if _, err = ipc.Do(socket, ipc.Request{Request: ipc.KindVersion}); err == nil {
				return socket, path, done
			}
		}
		select {
		case err := <-done:
			t.Fatalf("tilde exited: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no IPC socket: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func expectCleanExit(t *testing.T, socket string, done <-chan error, how string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("tilde ignored %s", how)
	}
	if _, err := os.Stat(socket); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the socket outlived tilde: %v", err)
	}
}

func TestConfiguredSessionOverIPC(t *testing.T) {
	socket, path, done := startSession(t, "outputs:\n  - {match: HEADLESS-1, scale: 2}\nbinds:\n  - {chord: SUPER + Q, action: kill}\n")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(noXwayland+text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := outputScale(t, socket); got != 2 {
		t.Fatalf("scale %g from the config, want 2", got)
	}
	stream, err := ipc.Subscribe(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	write("outputs:\n  - {match: HEADLESS-1, scale: 1.5}\n")
	if _, err := ipc.Do(socket, ipc.Request{Request: ipc.KindDispatch, Action: "reload"}); err != nil {
		t.Fatal(err)
	}
	if got := outputScale(t, socket); got != 1.5 {
		t.Fatalf("scale %g after the reload, want 1.5", got)
	}
	for _, want := range []string{`{"event":"dispatch","action":"reload"}`, `{"event":"output","change":"changed"`} {
		line, err := stream.Next()
		if err != nil || !strings.HasPrefix(string(line), want) {
			t.Fatalf("event %s, %v; want %s", line, err, want)
		}
	}

	write("outputs:\n  - {match: HEADLESS-1, scale: 1.5}\nbinds:\n  - {chord: SUPER + Q, action: teleport}\n")
	if _, err := ipc.Do(socket, ipc.Request{Request: ipc.KindDispatch, Action: "reload"}); err == nil || !strings.Contains(err.Error(), path+":6: binds[0].action") {
		t.Fatalf("a bad config reloaded: %v", err)
	}
	if got := outputScale(t, socket); got != 1.5 {
		t.Fatalf("scale %g after a rejected reload, want 1.5", got)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	expectCleanExit(t, socket, done, "SIGTERM")
}

func TestTheExitActionEndsTheSession(t *testing.T) {
	socket, _, done := startSession(t, "binds:\n  - {chord: SUPER + Delete, action: exit}\n")
	if _, err := ipc.Do(socket, ipc.Request{Request: ipc.KindDispatch, Action: "exit"}); err != nil {
		t.Fatal(err)
	}
	expectCleanExit(t, socket, done, "exit")
}
