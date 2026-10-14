package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend"
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
	input   *session.Libinput
	handler backend.Handler
	dev     *kdrm.Device
	card    session.Device
	report  func(error)
}

func openNative(l *loop.Loop, getenv func(string) string, report func(error)) (n *native, err error) {
	n = &native{report: report}
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
	if n.input, err = session.OpenLibinput(l, session.LibinputPath(getenv), n.logind, nil, report); err != nil {
		return nil, err
	}
	if n.monitor, err = session.Watch(l, n.logind.Seat(), []session.Subsystem{session.DRM, session.Input}, n.udev); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *native) Start(h backend.Handler) error {
	n.handler = h
	if err := n.Backend.Start(n); err != nil {
		return err
	}
	n.input.Start(n)
	devices, err := session.Enumerate(session.SystemRoots, n.logind.Seat(), session.Input)
	if err != nil {
		return err
	}
	for _, d := range devices {
		if err := n.input.Add(d.Devnode); err != nil {
			n.report(err)
		}
	}
	return nil
}

func (n *native) OutputAdded(o backend.Output) error   { return n.handler.OutputAdded(o) }
func (n *native) OutputRemoved(o backend.Output) error { return n.handler.OutputRemoved(o) }
func (n *native) OutputChanged(o backend.Output) error { return n.handler.OutputChanged(o) }
func (n *native) OutputDamaged(o backend.Output) error { return n.handler.OutputDamaged(o) }
func (n *native) Frame(o backend.Output) error         { return n.handler.Frame(o) }
func (n *native) DeviceAdded(d backend.Device) error   { return n.handler.DeviceAdded(d) }
func (n *native) DeviceRemoved(d backend.Device) error { return n.handler.DeviceRemoved(d) }

func (n *native) Presented(o backend.Output, p backend.Presentation) error {
	return n.handler.Presented(o, p)
}

func (n *native) Input(d backend.Device, e backend.Event) error {
	switch ev := e.(type) {
	case backend.PointerMotionAbsolute:
		if ev.Output == nil {
			o, ok := n.firstOutput()
			if !ok {
				return nil
			}
			ev.Output = o
			e = ev
		}
	case backend.TouchDown:
		if ev.Output == nil {
			o, ok := n.firstOutput()
			if !ok {
				return nil
			}
			ev.Output = o
			e = ev
		}
	}
	return n.handler.Input(d, e)
}

func (n *native) firstOutput() (backend.Output, bool) {
	outputs := n.Outputs()
	if len(outputs) == 0 {
		return nil, false
	}
	return outputs[0], true
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

func (n *native) PrepareForSleep(sleeping bool) error {
	if sleeping || n.Backend == nil {
		return nil
	}
	return n.Wake()
}

func (n *native) ActiveChanged(active bool) error {
	if n.input == nil {
		return nil
	}
	if !active {
		n.input.Suspend()
		return nil
	}
	return n.input.Resume()
}

func (n *native) udev(ev session.UdevEvent) error {
	switch {
	case ev.Action == session.Overflow:
		return n.rescan()
	case ev.Subsystem == session.Input && ev.Action == session.Add:
		if err := n.input.Add(ev.Devnode); err != nil {
			n.report(err)
		}
		return nil
	case ev.Subsystem == session.Input && ev.Action == session.Remove:
		return n.input.Remove(ev.Devnode)
	case ev.Device == n.card && ev.Properties["HOTPLUG"] == "1":
		return n.Hotplug()
	}
	return nil
}

func (n *native) rescan() error {
	devices, err := session.Enumerate(session.SystemRoots, n.logind.Seat(), session.Input)
	if err != nil {
		return err
	}
	for _, d := range devices {
		if err := n.input.Add(d.Devnode); err != nil {
			n.report(err)
		}
	}
	return n.Hotplug()
}

func (n *native) Close() error {
	var errs []error
	if n.monitor != nil {
		errs = append(errs, n.monitor.Close())
	}
	if n.input != nil {
		errs = append(errs, n.input.Close())
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
