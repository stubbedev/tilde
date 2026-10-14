package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	drmbackend "github.com/stubbedev/tilde/internal/backend/drm"
	kdrm "github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/edid"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/session"
)

type native struct {
	*drmbackend.Backend
	logind  *session.Logind
	monitor *session.Monitor
	dev     *kdrm.Device
	card    session.Device
}

func openNative(l *loop.Loop, getenv func(string) string) (n *native, err error) {
	n = &native{}
	if n.logind, err = session.Open(l, session.SystemBus(getenv), getenv, n); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, n.Close())
		}
	}()
	devices, err := session.Enumerate(session.SystemRoots, n.logind.Seat(), session.DRM)
	if err != nil {
		return nil, err
	}
	var card string
	for _, d := range devices {
		if strings.HasPrefix(filepath.Base(d.Devnode), "card") {
			card = d.Devnode
			break
		}
	}
	if card == "" {
		return nil, fmt.Errorf("no DRM card on %s", n.logind.Seat())
	}
	fd, dev, err := n.logind.TakeDevice(card)
	if err != nil {
		return nil, err
	}
	n.dev, n.card = kdrm.FromFD(fd, card), dev
	vendors, err := edid.LoadVendors(edid.PNPPath(getenv))
	if err != nil {
		return nil, err
	}
	if n.Backend, err = drmbackend.New(l, n.dev, gpu.PathsFromEnv(getenv), vendors); err != nil {
		return nil, err
	}
	if n.monitor, err = session.Watch(l, n.logind.Seat(), []session.Subsystem{session.DRM}, n.udev); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *native) DevicePaused(d session.Device) error {
	if d == n.card && n.Backend != nil {
		n.Pause()
	}
	return nil
}

func (n *native) DeviceResumed(d session.Device, fd int) error {
	if err := unix.Close(fd); err != nil {
		return fmt.Errorf("close the resumed device fd: %w", err)
	}
	if d != n.card || n.Backend == nil {
		return nil
	}
	return n.Resume()
}

func (n *native) ActiveChanged(bool) error {
	return nil
}

func (n *native) udev(ev session.UdevEvent) error {
	if ev.Action == session.Overflow || ev.Device == n.card && ev.Properties["HOTPLUG"] == "1" {
		return n.Hotplug()
	}
	return nil
}

func (n *native) Close() error {
	var errs []error
	if n.monitor != nil {
		errs = append(errs, n.monitor.Close())
	}
	if n.Backend != nil {
		errs = append(errs, n.Backend.Close())
	}
	if n.dev != nil {
		errs = append(errs, n.dev.Close(), n.logind.ReleaseDevice(n.card))
	}
	if n.logind != nil {
		errs = append(errs, n.logind.Close())
	}
	return errors.Join(errs...)
}
