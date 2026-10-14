package systemd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/stubbedev/tilde/internal/testenv"
)

const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

func bus(t *testing.T) string {
	t.Helper()
	daemon := testenv.Require(t, "dbus-daemon")
	dir := t.TempDir()
	config := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(config, fmt.Appendf(nil, busConfig, filepath.Join(dir, "bus")), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), daemon, "--config-file="+config, "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	address, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("dbus-daemon printed no address: %v", err)
	}
	return strings.TrimSpace(address)
}

type fakeManager struct {
	mu    sync.Mutex
	calls []string
	env   map[string]string
	fail  string
}

func (m *fakeManager) note(call string) *dbus.Error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
	if m.fail != "" && strings.HasPrefix(call, m.fail) {
		return dbus.MakeFailedError(errors.New("refused"))
	}
	return nil
}

func (m *fakeManager) SetEnvironment(assignments []string) *dbus.Error {
	m.mu.Lock()
	for _, a := range assignments {
		k, v, _ := strings.Cut(a, "=")
		m.env[k] = v
	}
	m.mu.Unlock()
	return m.note("SetEnvironment " + strings.Join(assignments, " "))
}

func (m *fakeManager) UnsetEnvironment(names []string) *dbus.Error {
	m.mu.Lock()
	for _, k := range names {
		delete(m.env, k)
	}
	m.mu.Unlock()
	return m.note("UnsetEnvironment " + strings.Join(names, " "))
}

func (m *fakeManager) StartUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	return "/org/freedesktop/systemd1/job/1", m.note("StartUnit " + name + " " + mode)
}

func (m *fakeManager) StopUnit(name, mode string) (dbus.ObjectPath, *dbus.Error) {
	return "/org/freedesktop/systemd1/job/2", m.note("StopUnit " + name + " " + mode)
}

func (m *fakeManager) state() ([]string, map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls), maps.Clone(m.env)
}

func systemd(t *testing.T, address, fail string) *fakeManager {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	m := &fakeManager{env: map[string]string{"KEEP": "1"}, fail: fail}
	if err := conn.Export(m, "/org/freedesktop/systemd1", manager); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName("org.freedesktop.systemd1", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own org.freedesktop.systemd1: %v, %v", reply, err)
	}
	return m
}

var env = [][2]string{{"WAYLAND_DISPLAY", "wayland-1"}, {"XDG_CURRENT_DESKTOP", "tilde"}, {"TILDE_SOCKET", "/run/user/1000/tilde/wayland-1.sock"}}

func TestSessionLifecycle(t *testing.T) {
	address := bus(t)
	m := systemd(t, address, "")
	s, err := Start(address, env)
	if err != nil {
		t.Fatal(err)
	}
	calls, got := m.state()
	wantCalls := []string{
		"SetEnvironment WAYLAND_DISPLAY=wayland-1 XDG_CURRENT_DESKTOP=tilde TILDE_SOCKET=/run/user/1000/tilde/wayland-1.sock",
		"StartUnit tilde-session.target replace",
	}
	if !slices.Equal(calls, wantCalls) || got["XDG_CURRENT_DESKTOP"] != "tilde" {
		t.Fatalf("calls %q, env %v", calls, got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	calls, got = m.state()
	wantCalls = append(wantCalls, "StopUnit tilde-session.target replace", "UnsetEnvironment WAYLAND_DISPLAY XDG_CURRENT_DESKTOP TILDE_SOCKET")
	if !slices.Equal(calls, wantCalls) || len(got) != 1 || got["KEEP"] != "1" {
		t.Fatalf("after close: calls %q, env %v", calls, got)
	}
}

func TestAFailedStartLeavesNoEnvironment(t *testing.T) {
	address := bus(t)
	m := systemd(t, address, "StartUnit")
	if _, err := Start(address, env); err == nil || !strings.Contains(err.Error(), "StartUnit") {
		t.Fatalf("a refused StartUnit: %v", err)
	}
	if _, got := m.state(); len(got) != 1 {
		t.Fatalf("env left behind: %v", got)
	}
	if _, err := Start("", env); err == nil {
		t.Fatal("no bus address was accepted")
	}
	if _, err := Start("unix:path="+filepath.Join(t.TempDir(), "none"), env); err == nil {
		t.Fatal("a missing bus was accepted")
	}
}
