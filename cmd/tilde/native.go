package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend"
	drmbackend "github.com/stubbedev/tilde/internal/backend/drm"
	kdrm "github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/edid"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/session"
)

type card struct {
	name    string
	device  session.Device
	dev     *kdrm.Device
	backend *drmbackend.Backend
}

type gpuCard struct {
	udev    session.UdevDevice
	name    string
	bootVGA bool
	render  bool
}

type native struct {
	l       *loop.Loop
	paths   gpu.Paths
	vendors edid.Vendors
	logind  *session.Logind
	monitor *session.Monitor
	input   *session.Libinput
	handler backend.Handler
	cards   []*card
	report  func(error)
}

func choosePrimary(cards []gpuCard, configured string) (int, string, error) {
	if configured != "" {
		if i := slices.IndexFunc(cards, func(c gpuCard) bool { return c.name == configured }); i >= 0 {
			return i, "set by gpu in the config", nil
		}
		names := make([]string, len(cards))
		for i, c := range cards {
			names[i] = c.name
		}
		return -1, "", fmt.Errorf("the configured gpu %s is not a DRM card on this seat (%s)", configured, strings.Join(names, ", "))
	}
	if i := slices.IndexFunc(cards, func(c gpuCard) bool { return c.bootVGA && c.render }); i >= 0 {
		return i, "the boot VGA device", nil
	}
	if i := slices.IndexFunc(cards, func(c gpuCard) bool { return c.render }); i >= 0 {
		return i, "the first card with a render node", nil
	}
	if len(cards) > 0 {
		return 0, "the first card; none has a render node, so it renders in software", nil
	}
	return -1, "", errors.New("no DRM card on this seat")
}

func (n *native) gpuCards() ([]gpuCard, error) {
	devices, err := session.Enumerate(session.SystemRoots, n.logind.Seat(), session.DRM)
	if err != nil {
		return nil, err
	}
	var cards []gpuCard
	for _, d := range devices {
		name := filepath.Base(d.Devnode)
		if !strings.HasPrefix(name, "card") {
			continue
		}
		boot, err := d.BootVGA()
		if err != nil {
			return nil, err
		}
		render, err := d.HasRenderNode()
		if err != nil {
			return nil, err
		}
		cards = append(cards, gpuCard{udev: d, name: name, bootVGA: boot, render: render})
	}
	slices.SortFunc(cards, func(a, b gpuCard) int { return cmp.Compare(a.udev.Device.Minor, b.udev.Device.Minor) })
	return cards, nil
}

func openNative(l *loop.Loop, getenv func(string) string, input session.InputConfig, configured string, report func(error)) (n *native, err error) {
	n = &native{l: l, paths: gpu.PathsFromEnv(getenv), report: report}
	if n.logind, err = session.Open(l, session.SystemBus(getenv), getenv, n); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, n.Close())
		}
	}()
	if n.vendors, err = edid.LoadVendors(edid.PNPPath(getenv)); err != nil {
		return nil, err
	}
	cards, err := n.gpuCards()
	if err != nil {
		return nil, err
	}
	primary, why, err := choosePrimary(cards, configured)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "tilde: rendering on %s, %s\n", cards[primary].name, why)
	if err := n.add(cards[primary]); err != nil {
		return nil, err
	}
	for i, c := range cards {
		if i != primary {
			n.addSecondary(c)
		}
	}
	if n.input, err = session.OpenLibinput(l, session.LibinputPath(getenv), n.logind, input, report); err != nil {
		return nil, err
	}
	if n.monitor, err = session.Watch(l, n.logind.Seat(), []session.Subsystem{session.DRM, session.Input}, n.udev); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *native) primary() *card {
	return n.cards[0]
}

func (n *native) add(g gpuCard) error {
	fd, device, err := n.logind.TakeDevice(g.udev.Devnode)
	if err != nil {
		return err
	}
	c := &card{name: g.name, device: device, dev: kdrm.FromFD(fd, g.udev.Devnode)}
	if len(n.cards) == 0 {
		c.backend, err = drmbackend.New(n.l, c.dev, n.paths, n.vendors)
	} else {
		c.backend, err = drmbackend.NewSecondary(n.l, c.dev, n.primary().backend.Renderer(), n.vendors)
	}
	if err != nil {
		return errors.Join(fmt.Errorf("drm: %s: %w", g.name, err), c.dev.Close(), n.logind.ReleaseDevice(device))
	}
	n.cards = append(n.cards, c)
	return nil
}

func (n *native) addSecondary(g gpuCard) {
	if err := n.add(g); err != nil {
		n.report(fmt.Errorf("%w; its outputs stay dark", err))
		return
	}
	fmt.Fprintf(os.Stderr, "tilde: showing outputs of %s through a copy from %s\n", g.name, n.primary().name)
}

func (n *native) cardOf(d session.Device) *card {
	if i := slices.IndexFunc(n.cards, func(c *card) bool { return c.device == d }); i >= 0 {
		return n.cards[i]
	}
	return nil
}

func (n *native) Renderer() render.Renderer {
	return n.primary().backend.Renderer()
}

func (n *native) Start(h backend.Handler) error {
	n.handler = h
	for _, c := range n.cards {
		if err := c.backend.Start(n); err != nil {
			return err
		}
	}
	n.input.Start(n)
	return n.addInputs()
}

func (n *native) addInputs() error {
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

func (n *native) Outputs() []*drmbackend.Output {
	var out []*drmbackend.Output
	for _, c := range n.cards {
		out = append(out, c.backend.Outputs()...)
	}
	return out
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
	if c := n.cardOf(d); c != nil {
		c.backend.Pause()
	}
	return nil
}

func (n *native) DeviceResumed(d session.Device, fd int) error {
	if err := unix.Close(fd); err != nil {
		return fmt.Errorf("close the resumed device fd: %w", err)
	}
	if c := n.cardOf(d); c != nil {
		return c.backend.Resume()
	}
	return nil
}

func (n *native) PrepareForSleep(sleeping bool) error {
	if sleeping {
		return nil
	}
	var errs []error
	for _, c := range n.cards {
		errs = append(errs, c.backend.Wake())
	}
	return errors.Join(errs...)
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
		return errors.Join(n.addInputs(), n.syncCards())
	case ev.Subsystem == session.Input && ev.Action == session.Add:
		if err := n.input.Add(ev.Devnode); err != nil {
			n.report(err)
		}
		return nil
	case ev.Subsystem == session.Input && ev.Action == session.Remove:
		return n.input.Remove(ev.Devnode)
	case ev.Subsystem != session.DRM:
		return nil
	case ev.Action == session.Remove:
		if c := n.cardOf(ev.Device); c != nil {
			return n.remove(c)
		}
		return nil
	case ev.Action == session.Add:
		return n.syncCards()
	case ev.Properties["HOTPLUG"] == "1":
		if c := n.cardOf(ev.Device); c != nil {
			return c.backend.Hotplug()
		}
	}
	return nil
}

func (n *native) syncCards() error {
	cards, err := n.gpuCards()
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range slices.Clone(n.cards) {
		if !slices.ContainsFunc(cards, func(g gpuCard) bool { return g.udev.Device == c.device }) {
			errs = append(errs, n.remove(c))
		}
	}
	for _, g := range cards {
		if n.cardOf(g.udev.Device) != nil {
			continue
		}
		before := len(n.cards)
		n.addSecondary(g)
		if len(n.cards) > before {
			errs = append(errs, n.cards[len(n.cards)-1].backend.Start(n))
		}
	}
	for _, c := range n.cards {
		errs = append(errs, c.backend.Hotplug())
	}
	return errors.Join(errs...)
}

func (n *native) remove(c *card) error {
	if c == n.primary() {
		return fmt.Errorf("drm: the primary GPU %s went away; every output renders on it, so tilde cannot go on", c.name)
	}
	n.cards = slices.DeleteFunc(n.cards, func(x *card) bool { return x == c })
	fmt.Fprintf(os.Stderr, "tilde: %s went away\n", c.name)
	if err := errors.Join(c.backend.Unplug(), c.dev.Close()); err != nil {
		n.report(fmt.Errorf("drm: let go of %s: %w", c.name, err))
	}
	return nil
}

func (n *native) Close() error {
	var errs []error
	if n.monitor != nil {
		errs = append(errs, n.monitor.Close())
	}
	if n.input != nil {
		errs = append(errs, n.input.Close())
	}
	for _, c := range slices.Backward(n.cards) {
		errs = append(errs, c.backend.Close(), c.dev.Close(), n.logind.ReleaseDevice(c.device))
	}
	if n.logind != nil {
		errs = append(errs, n.logind.Close())
	}
	return errors.Join(errs...)
}
