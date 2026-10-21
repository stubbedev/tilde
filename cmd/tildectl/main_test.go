package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/ipc"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/testenv"
	"github.com/stubbedev/tilde/internal/wm"
)

func TestParseArgs(t *testing.T) {
	for args, want := range map[string]ipc.Request{
		"dispatch movetoworkspace 3": {Request: ipc.KindDispatch, Action: "movetoworkspace 3"},
		"dispatch exec foot -e htop": {Request: ipc.KindDispatch, Action: "exec foot -e htop"},
		"reload":                     {Request: ipc.KindDispatch, Action: "reload"},
		"windows":                    {Request: ipc.KindWindows},
		"activewindow":               {Request: ipc.KindActiveWindow},
		"watch":                      {Request: ipc.KindWatch},
	} {
		if got, err := parseArgs(strings.Fields(args)); err != nil || got != want {
			t.Errorf("%q: %+v, %v; want %+v", args, got, err, want)
		}
	}
	for _, args := range []string{"", "dispatch", "windows 2", "reload now", "teleport"} {
		if _, err := parseArgs(strings.Fields(args)); !errors.Is(err, errUsage) {
			t.Errorf("%q: %v", args, err)
		}
	}
}

type handler struct{ dispatched []wm.Action }

func (h *handler) Snapshot() ipc.Snapshot {
	return ipc.Snapshot{Windows: []ipc.Window{{ID: 3, AppID: "foot", Focused: true}}}
}

func (h *handler) Dispatch(a wm.Action) error {
	h.dispatched = append(h.dispatched, a)
	return nil
}

func TestRunAgainstASocket(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{}
	path := filepath.Join(testenv.SocketDir(t), "tilde", "wayland-1.sock")
	s, err := ipc.Listen(l, path, h, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	defer func() {
		_ = l.Post(func() error {
			l.Stop()
			return nil
		})
		if err := errors.Join(<-done, s.Close(), l.Close()); err != nil {
			t.Error(err)
		}
	}()
	getenv := func(k string) string {
		if k == ipc.SocketEnv {
			return path
		}
		return ""
	}
	var out bytes.Buffer
	if err := run([]string{"activewindow"}, getenv, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "{\n  \"id\": 3,\n  \"app_id\": \"foot\",") {
		t.Fatalf("activewindow printed %q", out.String())
	}
	out.Reset()
	if err := run([]string{"--socket", path, "reload"}, func(string) string { return "" }, &out); err != nil || out.Len() != 0 {
		t.Fatalf("reload: %q, %v", out.String(), err)
	}
	if err := l.Post(func() error {
		if len(h.dispatched) != 1 || h.dispatched[0] != (wm.ReloadAction{}) {
			t.Errorf("dispatched %v", h.dispatched)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--socket", filepath.Join(testenv.SocketDir(t), "none.sock"), "windows"}, getenv, &out); err == nil {
		t.Fatal("a missing socket succeeded")
	}
}
