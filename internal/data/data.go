// Package data implements the clipboard, the primary selection and drag and
// drop: wl_data_device_manager and zwp_primary_selection_device_manager_v1.
package data

import (
	"errors"
	"fmt"
	"slices"

	"golang.org/x/sys/unix"

	sps "github.com/stubbedev/tilde/internal/protocol/server/primaryselection"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	dataDeviceManagerVersion = 3
	primaryVersion           = 1
)

type Kind uint8

const (
	Clipboard Kind = iota
	Primary
	kinds
)

type Source interface {
	Client() *server.Client
	Mimes() []string
	Send(mime string, fd int) error
	Cancel() error
}

type Listener interface {
	CanDrag(origin *surface.Surface, serial uint32) bool
	DragStarted(d *Drag) error
	DragEnded(d *Drag) error
	SelectionChanged(k Kind, src Source) error
}

type devices struct {
	client    *server.Client
	data      []*sw.WlDataDevice
	primaries []*sps.ZwpPrimarySelectionDeviceV1
}

type Manager struct {
	display   *server.Display
	listener  Listener
	clients   []*devices
	focus     *server.Client
	selection [kinds]Source
	offers    [kinds][]*selectionOffer
	drag      *Drag
}

func New(d *server.Display, listener Listener) (*Manager, error) {
	m := &Manager{display: d, listener: listener}
	if _, err := d.AddGlobal(sw.WlDataDeviceManagerInterface, dataDeviceManagerVersion, m.bindData); err != nil {
		return nil, err
	}
	if _, err := d.AddGlobal(sps.ZwpPrimarySelectionDeviceManagerV1Interface, primaryVersion, m.bindPrimary); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) devicesOf(c *server.Client) *devices {
	for _, d := range m.clients {
		if d.client == c {
			return d
		}
	}
	d := &devices{client: c}
	m.clients = append(m.clients, d)
	return d
}

func (m *Manager) lookup(c *server.Client) *devices {
	for _, d := range m.clients {
		if d.client == c {
			return d
		}
	}
	return nil
}

func (m *Manager) forget(d *devices) {
	if len(d.data) == 0 && len(d.primaries) == 0 {
		m.clients = slices.DeleteFunc(m.clients, func(x *devices) bool { return x == d })
	}
}

func track[T wire.Resource](m *Manager, d *devices, list *[]T, res T) {
	*list = append(*list, res)
	d.client.OnDestroy(res, func() error {
		*list = slices.DeleteFunc(*list, func(x T) bool { return wire.Resource(x) == wire.Resource(res) })
		m.forget(d)
		return nil
	})
}

func (m *Manager) Selection(k Kind) Source {
	return m.selection[k]
}

func (m *Manager) SetFocus(c *server.Client) error {
	if c == m.focus {
		return nil
	}
	m.focus = c
	var errs []error
	for k := range kinds {
		errs = append(errs, m.offerSelection(k, c))
	}
	return errors.Join(errs...)
}

func (m *Manager) SetSelection(k Kind, src Source) error {
	old := m.selection[k]
	if old == src {
		return nil
	}
	m.selection[k] = src
	var errs []error
	if old != nil {
		errs = append(errs, old.Cancel())
	}
	return errors.Join(append(errs, m.offerSelection(k, m.focus), m.listener.SelectionChanged(k, src))...)
}

func (m *Manager) offerSelection(k Kind, c *server.Client) error {
	for _, o := range m.offers[k] {
		o.dead = true
	}
	m.offers[k] = m.offers[k][:0]
	if c == nil {
		return nil
	}
	d := m.lookup(c)
	if d == nil {
		return nil
	}
	src := m.selection[k]
	var errs []error
	switch k {
	case Clipboard:
		for _, dev := range d.data {
			errs = append(errs, c.Sent(m.offerData(c, dev, src)))
		}
	case Primary:
		for _, dev := range d.primaries {
			errs = append(errs, c.Sent(m.offerPrimary(dev, src)))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) sourceGone(src Source) error {
	var errs []error
	for k := range kinds {
		if m.selection[k] == src {
			m.selection[k] = nil
			errs = append(errs, m.offerSelection(k, m.focus), m.listener.SelectionChanged(k, nil))
		}
	}
	return errors.Join(errs...)
}

type selectionOffer struct {
	source Source
	dead   bool
}

func (o *selectionOffer) receive(mime string, fd int) error {
	if o.dead || !slices.Contains(o.source.Mimes(), mime) {
		return closeFD(fd)
	}
	return errors.Join(o.source.Send(mime, fd), closeFD(fd))
}

func closeFD(fd int) error {
	if err := unix.Close(fd); err != nil {
		return fmt.Errorf("data: close transfer fd %d: %w", fd, err)
	}
	return nil
}
