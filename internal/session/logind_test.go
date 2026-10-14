package session

import (
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/testenv"
)

const sessionPath = dbus.ObjectPath("/org/freedesktop/login1/session/_31")

type fakeLogind struct {
	mu    sync.Mutex
	calls []string
	conn  *dbus.Conn
	props *prop.Properties
}

func (f *fakeLogind) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeLogind) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type manager struct{ f *fakeLogind }

func (m manager) GetSession(id string) (dbus.ObjectPath, *dbus.Error) {
	m.f.note("GetSession " + id)
	return sessionPath, nil
}

func (m manager) GetSessionByPID(uint32) (dbus.ObjectPath, *dbus.Error) {
	m.f.note("GetSessionByPID")
	return sessionPath, nil
}

type session struct{ f *fakeLogind }

func (s session) TakeControl(force bool) *dbus.Error {
	s.f.note("TakeControl")
	return nil
}

func (s session) ReleaseControl() *dbus.Error {
	s.f.note("ReleaseControl")
	return nil
}

func (s session) TakeDevice(major, minor uint32) (dbus.UnixFD, bool, *dbus.Error) {
	s.f.note("TakeDevice")
	fd, err := os.Open(os.DevNull)
	if err != nil {
		return 0, false, dbus.MakeFailedError(err)
	}
	return dbus.UnixFD(fd.Fd()), false, nil
}

func (s session) ReleaseDevice(major, minor uint32) *dbus.Error {
	s.f.note("ReleaseDevice")
	return nil
}

func (s session) PauseDeviceComplete(major, minor uint32) *dbus.Error {
	s.f.note("PauseDeviceComplete")
	return nil
}

type seat struct{ f *fakeLogind }

func (s seat) SwitchTo(vt uint32) *dbus.Error {
	s.f.note("SwitchTo")
	if vt == 0 {
		return dbus.MakeFailedError(errors.New("no VT 0"))
	}
	return nil
}

func logind(t *testing.T, address string) *fakeLogind {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &fakeLogind{conn: conn}
	seatPath := dbus.ObjectPath("/org/freedesktop/login1/seat/seat0")
	for _, e := range []struct {
		v     any
		path  dbus.ObjectPath
		iface string
	}{{manager{f}, managerPath, managerIface}, {session{f}, sessionPath, sessionIface}, {seat{f}, seatPath, seatIface}} {
		if err := conn.Export(e.v, e.path, e.iface); err != nil {
			t.Fatal(err)
		}
	}
	f.props, err = prop.Export(conn, sessionPath, prop.Map{sessionIface: {
		"Seat": {Value: struct {
			ID   string
			Path dbus.ObjectPath
		}{"seat0", seatPath}, Emit: prop.EmitTrue},
		"Active": {Value: true, Emit: prop.EmitTrue},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(login1, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", login1, reply, err)
	}
	return f
}

type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
	return nil
}

func (e *events) seen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.log)
}

func (e *events) DevicePaused(d Device) error { return e.add("paused") }

func (e *events) DeviceResumed(d Device, fd int) error {
	if fd < 0 {
		return errors.New("resumed without an fd")
	}
	return e.add("resumed")
}

func (e *events) ActiveChanged(active bool) error {
	if active {
		return e.add("active")
	}
	return e.add("inactive")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLogindSession(t *testing.T) {
	address := testenv.DBus(t)
	f := logind(t, address)
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ev := &events{}
	lg, err := Open(l, address, func(k string) string {
		if k == "XDG_SESSION_ID" {
			return "1"
		}
		return ""
	}, ev)
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- l.Run() }()
	defer func() {
		_ = l.Post(func() error {
			l.Stop()
			return nil
		})
		if err := errors.Join(<-done, l.Close()); err != nil {
			t.Error(err)
		}
	}()
	if !lg.Active() {
		t.Fatal("the session is not active")
	}
	fd, dev, err := lg.TakeDevice(os.DevNull)
	if err != nil || fd < 0 || dev != (Device{Major: 1, Minor: 3}) {
		t.Fatalf("TakeDevice: %d %+v %v", fd, dev, err)
	}
	if err := errors.Join(lg.ReleaseDevice(dev), lg.SwitchTo(2)); err != nil {
		t.Fatal(err)
	}
	if err := lg.SwitchTo(0); err == nil {
		t.Fatal("a refused VT switch succeeded")
	}
	if _, _, err := lg.TakeDevice("/"); err == nil {
		t.Fatal("a directory was taken as a device")
	}
	for _, sig := range []struct {
		name string
		args []any
	}{
		{"PauseDevice", []any{uint32(1), uint32(3), "pause"}},
		{"PauseDevice", []any{uint32(1), uint32(3), "gone"}},
		{"ResumeDevice", []any{uint32(1), uint32(3), dbus.UnixFD(0)}},
	} {
		if err := f.conn.Emit(sessionPath, sessionIface+"."+sig.name, sig.args...); err != nil {
			t.Fatal(err)
		}
	}
	f.props.SetMust(sessionIface, "Active", false)
	waitFor(t, "the signals", func() bool { return len(ev.seen()) == 4 })
	if got := ev.seen(); !slices.Equal(got, []string{"paused", "paused", "resumed", "inactive"}) {
		t.Fatalf("events %v", got)
	}
	waitFor(t, "the pause acknowledged", func() bool { return slices.Contains(f.seen(), "PauseDeviceComplete") })
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"GetSession 1", "TakeControl", "TakeDevice", "ReleaseDevice", "SwitchTo", "SwitchTo", "PauseDeviceComplete", "ReleaseControl"}
	if got := f.seen(); !slices.Equal(got, want) {
		t.Fatalf("logind saw %v, want %v", got, want)
	}
}
