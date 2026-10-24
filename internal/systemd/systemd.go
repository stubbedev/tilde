// Package systemd hands the session's environment to D-Bus activation and
// the systemd user manager, and runs lys-wm-session.target while lys-wm runs.
package systemd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	Target      = "lys-wm-session.target"
	callTimeout = 5 * time.Second
	manager     = "org.freedesktop.systemd1.Manager"
)

type Session struct {
	conn  *dbus.Conn
	names []string
}

func call(obj dbus.BusObject, method string, args ...any) error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if err := obj.CallWithContext(ctx, method, 0, args...).Err; err != nil {
		return fmt.Errorf("systemd: %s: %w", method, err)
	}
	return nil
}

func (s *Session) manager() dbus.BusObject {
	return s.conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1")
}

func Start(address string, env [][2]string) (*Session, error) {
	if address == "" {
		return nil, errors.New("systemd: DBUS_SESSION_BUS_ADDRESS is not set")
	}
	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, fmt.Errorf("systemd: connect to the session bus: %w", err)
	}
	s := &Session{conn: conn, names: make([]string, len(env))}
	activation := make(map[string]string, len(env))
	assignments := make([]string, len(env))
	for i, kv := range env {
		s.names[i] = kv[0]
		activation[kv[0]] = kv[1]
		assignments[i] = kv[0] + "=" + kv[1]
	}
	if err := errors.Join(
		call(conn.BusObject(), "org.freedesktop.DBus.UpdateActivationEnvironment", activation),
		call(s.manager(), manager+".SetEnvironment", assignments),
	); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	if err := call(s.manager(), manager+".StartUnit", Target, "replace"); err != nil {
		return nil, errors.Join(err, call(s.manager(), manager+".UnsetEnvironment", s.names), conn.Close())
	}
	return s, nil
}

func (s *Session) Close() error {
	return errors.Join(
		call(s.manager(), manager+".StopUnit", Target, "replace"),
		call(s.manager(), manager+".UnsetEnvironment", s.names),
		s.conn.Close(),
	)
}
