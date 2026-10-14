package harness

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/ipc"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wm"
)

func query[T any](h *Harness, kind ipc.Kind) T {
	h.t.Helper()
	raw, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: kind})
	if err != nil {
		h.t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		h.t.Fatalf("decode %s %s: %v", kind, raw, err)
	}
	return v
}

func (h *Harness) watch() <-chan string {
	h.t.Helper()
	stream, err := ipc.Subscribe(h.IPCSocket())
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = stream.Close() })
	lines := make(chan string, 4096)
	go func() {
		defer close(lines)
		for {
			line, err := stream.Next()
			if err != nil {
				return
			}
			lines <- strings.TrimSpace(string(line))
		}
	}()
	return lines
}

func expectEvent(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(clientTimeout)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the event stream ended before %s", want)
			}
			if line == want {
				return
			}
		case <-deadline:
			t.Fatalf("no %s on the event stream", want)
		}
	}
}

func TestEveryActionThroughIPC(t *testing.T) {
	reloads := 0
	h := StartWith(t, func(cfg *compositor.Config) {
		cfg.Reload = func() error {
			reloads++
			return nil
		}
	}, desktop)
	lines := h.watch()
	c := testclient.Connect(t, h.Socket())
	var windows []*testclient.Window
	for _, name := range []string{"a", "b", "c"} {
		w := c.Window(name, quadrants)
		c.DispatchUntil(name+" to map", w.Mapped)
		windows = append(windows, w)
	}
	actions := []string{
		"focus left", "move right", "makegroup tab ephemeral", "togglefloating", "togglefloating",
		"fullscreen", "fullscreen", "resizeactive 10 0", "submap resize", "submap reset",
		"movetoworkspace 3", "workspace 3", "workspace e-1", "exec true", "reload", "dpms off", "dpms toggle HEADLESS-1", "kill",
	}
	mouseOnly := []string{"movewindow", "resizewindow"}
	for _, spec := range wm.Actions {
		if !slices.ContainsFunc(append(actions, mouseOnly...), func(a string) bool { return strings.Fields(a)[0] == spec.Name }) {
			t.Errorf("action %s is not walked through IPC", spec.Name)
		}
	}
	for _, a := range mouseOnly {
		if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindDispatch, Action: a}); err == nil || !strings.Contains(err.Error(), compositor.ErrNotFromMouseBind.Error()) {
			t.Fatalf("%s through IPC: %v", a, err)
		}
	}
	for _, a := range actions {
		if _, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindDispatch, Action: a}); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		expectEvent(t, lines, `{"event":"dispatch","action":"`+a+`"}`)
		if a == "workspace 3" {
			expectEvent(t, lines, `{"event":"workspace","output":"HEADLESS-1","workspace":3}`)
			if w := query[ipc.Window](h, ipc.KindActiveWindow); w.Workspace != 3 || !w.Focused {
				t.Fatalf("the window moved to workspace 3 is not active: %+v", w)
			}
		}
	}
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
	if reloads != 1 {
		t.Fatalf("reload ran %d times", reloads)
	}
	c.DispatchUntil("kill to close a window", func() bool {
		return slices.ContainsFunc(windows, func(w *testclient.Window) bool { return w.Closed })
	})
	outputs := query[[]ipc.Output](h, ipc.KindOutputs)
	if len(outputs) != 1 || outputs[0].Workspace != 1 || !outputs[0].Focused || outputs[0].Rect != (ipc.Rect{Width: 1280, Height: 800}) {
		t.Fatalf("outputs %+v", outputs)
	}
	got := query[[]ipc.Workspace](h, ipc.KindWorkspaces)
	want := []ipc.Workspace{{ID: 1, Output: "HEADLESS-1", Windows: 2, Visible: true, Focused: true}, {ID: 3, Output: "HEADLESS-1", Windows: 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("workspaces\n got %+v\nwant %+v", got, want)
	}
}

func (h *Harness) expectActive(c *Client) {
	h.t.Helper()
	deadline := time.Now().Add(clientTimeout)
	for {
		raw, err := ipc.Do(h.IPCSocket(), ipc.Request{Request: ipc.KindActiveWindow})
		if err != nil {
			h.t.Fatal(err)
		}
		var w ipc.Window
		if raw != nil {
			if err := json.Unmarshal(raw, &w); err != nil {
				h.t.Fatal(err)
			}
		}
		if raw != nil && int(w.PID) == c.cmd.Process.Pid && w.Focused {
			if windows := query[[]ipc.Window](h, ipc.KindWindows); !slices.ContainsFunc(windows, func(x ipc.Window) bool { return x.ID == w.ID }) {
				h.t.Fatalf("the active window %d is not among the windows %+v", w.ID, windows)
			}
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("IPC never showed %s (pid %d) as the active window; last %s", filepath.Base(c.cmd.Path), c.cmd.Process.Pid, raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
