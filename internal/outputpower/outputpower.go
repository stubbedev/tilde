// Package outputpower implements zwlr_output_power_manager_v1: clients such
// as idle daemons turn outputs off and on.
package outputpower

import (
	"errors"
	"fmt"

	"github.com/stubbedev/tilde/internal/output"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	swop "github.com/stubbedev/tilde/internal/protocol/server/wlroutputpower"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const version = 1

type Controller interface {
	Owner(res *sw.WlOutput) *output.Output
	SetPower(o *output.Output, on bool) error
	Powered(o *output.Output) bool
}

type Manager struct {
	ctl    Controller
	report func(error)
	powers []*power
}

func New(d *server.Display, ctl Controller, report func(error)) (*Manager, error) {
	m := &Manager{ctl: ctl, report: report}
	if _, err := d.AddGlobal(swop.ZwlrOutputPowerManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := swop.NewZwlrOutputPowerManagerV1(c, id, v, requests{m, c})
	return err
}

type requests struct {
	m *Manager
	c *server.Client
}

func (requests) Destroy(*swop.ZwlrOutputPowerManagerV1) error {
	return nil
}

func modeOf(on bool) swop.ZwlrOutputPowerV1Mode {
	if on {
		return swop.ZwlrOutputPowerV1ModeOn
	}
	return swop.ZwlrOutputPowerV1ModeOff
}

func (r requests) GetOutputPower(_ *swop.ZwlrOutputPowerManagerV1, res *swop.ZwlrOutputPowerV1, wl *sw.WlOutput) (swop.ZwlrOutputPowerV1Handler, error) {
	p := &power{m: r.m, c: r.c, res: res, out: r.m.ctl.Owner(wl)}
	server.Track(res, &r.m.powers, p)
	if p.out == nil {
		return p, r.c.Sent(res.SendFailed())
	}
	return p, r.c.Sent(res.SendMode(modeOf(r.m.ctl.Powered(p.out))))
}

type power struct {
	m   *Manager
	c   *server.Client
	res *swop.ZwlrOutputPowerV1
	out *output.Output
}

func (p *power) Destroy(*swop.ZwlrOutputPowerV1) error {
	return nil
}

func (p *power) SetMode(obj *swop.ZwlrOutputPowerV1, mode swop.ZwlrOutputPowerV1Mode) error {
	if mode != swop.ZwlrOutputPowerV1ModeOn && mode != swop.ZwlrOutputPowerV1ModeOff {
		return obj.ProtocolError(swop.ZwlrOutputPowerV1ErrorInvalidMode, fmt.Sprintf("mode %d is neither on nor off", mode))
	}
	if p.out == nil {
		return nil
	}
	if err := p.m.ctl.SetPower(p.out, mode == swop.ZwlrOutputPowerV1ModeOn); err != nil {
		p.m.report(fmt.Errorf("outputpower: %w", err))
		p.out = nil
		return p.c.Sent(p.res.SendFailed())
	}
	return nil
}

func (m *Manager) Changed(o *output.Output) error {
	on := m.ctl.Powered(o)
	var errs []error
	for _, p := range m.powers {
		if p.out == o {
			errs = append(errs, p.c.Sent(p.res.SendMode(modeOf(on))))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) Removed(o *output.Output) error {
	var errs []error
	for _, p := range m.powers {
		if p.out == o {
			p.out = nil
			errs = append(errs, p.c.Sent(p.res.SendFailed()))
		}
	}
	return errors.Join(errs...)
}
