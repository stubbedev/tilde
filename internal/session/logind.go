// Package session gives tilde its seat: devices through logind, VT
// switching, and device discovery.
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
)

const (
	login1        = "org.freedesktop.login1"
	managerPath   = "/org/freedesktop/login1"
	managerIface  = login1 + ".Manager"
	sessionIface  = login1 + ".Session"
	seatIface     = login1 + ".Seat"
	propsIface    = "org.freedesktop.DBus.Properties"
	callTimeout   = 5 * time.Second
	defaultSystem = "unix:path=/run/dbus/system_bus_socket"
)

type Device struct {
	Major, Minor uint32
}

func DeviceOf(path string) (Device, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return Device{}, fmt.Errorf("session: stat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return Device{}, fmt.Errorf("session: %s is not a character device", path)
	}
	return Device{Major: unix.Major(st.Rdev), Minor: unix.Minor(st.Rdev)}, nil
}

type Listener interface {
	DevicePaused(d Device) error
	DeviceResumed(d Device, fd int) error
	ActiveChanged(active bool) error
}

type Logind struct {
	conn     *dbus.Conn
	session  dbus.BusObject
	seat     dbus.BusObject
	listener Listener
	loop     *loop.Loop
	signals  chan *dbus.Signal
	done     chan struct{}
	active   bool
}

func SystemBus(getenv func(string) string) string {
	if a := getenv("DBUS_SYSTEM_BUS_ADDRESS"); a != "" {
		return a
	}
	return defaultSystem
}

func call(obj dbus.BusObject, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return obj.CallWithContext(ctx, method, 0, args...)
}

func Open(l *loop.Loop, address string, getenv func(string) string, listener Listener) (lg *Logind, err error) {
	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, fmt.Errorf("session: connect to the system bus: %w", err)
	}
	lg = &Logind{conn: conn, listener: listener, loop: l, signals: make(chan *dbus.Signal, 32), done: make(chan struct{})}
	defer func() {
		if err != nil {
			err = errors.Join(err, conn.Close())
		}
	}()
	manager := conn.Object(login1, managerPath)
	var path dbus.ObjectPath
	if id := getenv("XDG_SESSION_ID"); id != "" {
		err = call(manager, managerIface+".GetSession", id).Store(&path)
	} else {
		err = call(manager, managerIface+".GetSessionByPID", uint32(os.Getpid())).Store(&path) //nolint:gosec // process IDs fit a uint32
	}
	if err != nil {
		return nil, fmt.Errorf("session: find this process's logind session: %w", err)
	}
	lg.session = conn.Object(login1, path)
	var seat struct {
		ID   string
		Path dbus.ObjectPath
	}
	if err := lg.property(lg.session, sessionIface, "Seat", &seat); err != nil {
		return nil, err
	}
	lg.seat = conn.Object(login1, seat.Path)
	if err := lg.property(lg.session, sessionIface, "Active", &lg.active); err != nil {
		return nil, err
	}
	if err := call(lg.session, sessionIface+".TakeControl", false).Err; err != nil {
		return nil, fmt.Errorf("session: take control of %s: %w", path, err)
	}
	for _, opts := range [][]dbus.MatchOption{
		{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(sessionIface)},
		{dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(propsIface), dbus.WithMatchMember("PropertiesChanged")},
	} {
		if err := conn.AddMatchSignal(opts...); err != nil {
			return nil, fmt.Errorf("session: subscribe to %s: %w", path, err)
		}
	}
	conn.Signal(lg.signals)
	go lg.relay()
	return lg, nil
}

func (lg *Logind) property(obj dbus.BusObject, iface, name string, out any) error {
	v, err := obj.GetProperty(iface + "." + name)
	if err != nil {
		return fmt.Errorf("session: read %s.%s: %w", iface, name, err)
	}
	if err := v.Store(out); err != nil {
		return fmt.Errorf("session: decode %s.%s: %w", iface, name, err)
	}
	return nil
}

func (lg *Logind) Active() bool {
	return lg.active
}

func (lg *Logind) relay() {
	for {
		select {
		case sig, ok := <-lg.signals:
			if !ok {
				return
			}
			if err := lg.loop.Post(func() error { return lg.handle(sig) }); err != nil {
				return
			}
		case <-lg.done:
			return
		}
	}
}

func (lg *Logind) handle(sig *dbus.Signal) error {
	switch sig.Name {
	case sessionIface + ".PauseDevice":
		var d Device
		var kind string
		if err := dbus.Store(sig.Body, &d.Major, &d.Minor, &kind); err != nil {
			return fmt.Errorf("session: PauseDevice: %w", err)
		}
		err := lg.listener.DevicePaused(d)
		if kind == "pause" {
			err = errors.Join(err, call(lg.session, sessionIface+".PauseDeviceComplete", d.Major, d.Minor).Err)
		}
		return err
	case sessionIface + ".ResumeDevice":
		var d Device
		var fd dbus.UnixFD
		if err := dbus.Store(sig.Body, &d.Major, &d.Minor, &fd); err != nil {
			return fmt.Errorf("session: ResumeDevice: %w", err)
		}
		return lg.listener.DeviceResumed(d, int(fd))
	case propsIface + ".PropertiesChanged":
		var iface string
		var changed map[string]dbus.Variant
		var invalidated []string
		if err := dbus.Store(sig.Body, &iface, &changed, &invalidated); err != nil {
			return fmt.Errorf("session: PropertiesChanged: %w", err)
		}
		v, ok := changed["Active"]
		if iface != sessionIface || !ok {
			return nil
		}
		if err := v.Store(&lg.active); err != nil {
			return fmt.Errorf("session: decode Active: %w", err)
		}
		return lg.listener.ActiveChanged(lg.active)
	}
	return nil
}

func (lg *Logind) TakeDevice(path string) (int, Device, error) {
	d, err := DeviceOf(path)
	if err != nil {
		return -1, Device{}, err
	}
	var fd dbus.UnixFD
	var inactive bool
	if err := call(lg.session, sessionIface+".TakeDevice", d.Major, d.Minor).Store(&fd, &inactive); err != nil {
		return -1, Device{}, fmt.Errorf("session: take %s: %w", path, err)
	}
	return int(fd), d, nil
}

func (lg *Logind) ReleaseDevice(d Device) error {
	if err := call(lg.session, sessionIface+".ReleaseDevice", d.Major, d.Minor).Err; err != nil {
		return fmt.Errorf("session: release device %d:%d: %w", d.Major, d.Minor, err)
	}
	return nil
}

func (lg *Logind) SwitchTo(vt uint32) error {
	if err := call(lg.seat, seatIface+".SwitchTo", vt).Err; err != nil {
		return fmt.Errorf("session: switch to VT %d: %w", vt, err)
	}
	return nil
}

func (lg *Logind) Close() error {
	close(lg.done)
	lg.conn.RemoveSignal(lg.signals)
	return errors.Join(call(lg.session, sessionIface+".ReleaseControl").Err, lg.conn.Close())
}
