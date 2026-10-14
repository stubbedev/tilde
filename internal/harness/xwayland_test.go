package harness

import (
	"encoding/binary"
	"net"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
)

func xConnect(t *testing.T, h *Harness) {
	t.Helper()
	path := filepath.Join(h.RuntimeDir, ".X11-unix", "X"+strings.TrimPrefix(h.xwayland.Display(), ":"))
	conn, err := net.DialTimeout("unix", path, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(clientTimeout)); err != nil {
		t.Fatal(err)
	}
	setup := binary.LittleEndian.AppendUint16([]byte{'l', 0}, 11)
	setup = append(setup, make([]byte, 8)...)
	if _, err := conn.Write(setup); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 8)
	if _, err := conn.Read(reply); err != nil {
		t.Fatalf("no X connection setup reply: %v", err)
	}
	if reply[0] != 1 {
		t.Fatalf("X connection setup failed: %v", reply)
	}
}

func TestXwaylandStartsOnTheFirstXClientAndAfterACrash(t *testing.T) {
	reports := make(chan error, 4)
	h := StartXwayland(t, func(err error) { reports <- err }, small)
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		if h.xwayland.Running() || h.xwayland.Pid() != 0 {
			t.Error("Xwayland runs before any X client connected")
		}
		return nil
	})
	xConnect(t, h)
	h.Eventually("Xwayland ready", func(*headless.Backend, *compositor.Compositor) bool { return h.xwayland.Running() })
	if !strings.Contains(h.Trace(), "xwayland_shell_v1") {
		t.Fatal("Xwayland never bound xwayland_shell_v1")
	}
	var pid int
	h.Do(func(*headless.Backend, *compositor.Compositor) error {
		pid = h.xwayland.Pid()
		return syscall.Kill(pid, syscall.SIGKILL)
	})
	select {
	case err := <-reports:
		if !strings.Contains(err.Error(), "signal: killed") {
			t.Fatalf("reported %v", err)
		}
	case <-time.After(clientTimeout):
		t.Fatal("the crash was not reported")
	}
	xConnect(t, h)
	h.Eventually("Xwayland restarted", func(*headless.Backend, *compositor.Compositor) bool {
		return h.xwayland.Running() && h.xwayland.Pid() != pid
	})
}
