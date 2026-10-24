// Package gammacontrol implements zwlr_gamma_control_manager_v1: one client
// at a time sets an output's gamma ramps, as night-light tools do, and the
// output returns to the identity when it lets go.
package gammacontrol

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	swgc "github.com/stubbedev/lys-wm/internal/protocol/server/wlrgammacontrol"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 1

var Interface = swgc.ZwlrGammaControlManagerV1Interface

type Outputs interface {
	Owner(res *sw.WlOutput) *output.Output
}

type Manager struct {
	outputs  Outputs
	report   func(error)
	controls map[*output.Output]*control
}

func New(d *server.Display, outputs Outputs, report func(error)) (*Manager, error) {
	m := &Manager{outputs: outputs, report: report, controls: map[*output.Output]*control{}}
	if _, err := d.AddGlobal(Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Removed(o *output.Output) error {
	k, ok := m.controls[o]
	if !ok {
		return nil
	}
	delete(m.controls, o)
	k.gamma = nil
	return k.res.SendFailed()
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := swgc.NewZwlrGammaControlManagerV1(c, id, v, requests{m, c})
	return err
}

type requests struct {
	m *Manager
	c *server.Client
}

func (requests) Destroy(*swgc.ZwlrGammaControlManagerV1) error { return nil }

func (r requests) GetGammaControl(_ *swgc.ZwlrGammaControlManagerV1, res *swgc.ZwlrGammaControlV1, wl *sw.WlOutput) (swgc.ZwlrGammaControlV1Handler, error) {
	k := &control{m: r.m, c: r.c, res: res}
	o := r.m.outputs.Owner(wl)
	if o == nil {
		return k, res.SendFailed()
	}
	g, ok := o.Backend().(backend.Gamma)
	if _, taken := r.m.controls[o]; !ok || taken || g.GammaSize() == 0 {
		return k, res.SendFailed()
	}
	k.out, k.gamma = o, g
	r.m.controls[o] = k
	server.OnDestroy(res, k.release)
	return k, res.SendGammaSize(uint32(g.GammaSize())) //nolint:gosec // a LUT size is far below 2^32
}

type control struct {
	m     *Manager
	c     *server.Client
	res   *swgc.ZwlrGammaControlV1
	out   *output.Output
	gamma backend.Gamma
	set   bool
}

var errRampSize = errors.New("the gamma table is not three ramps of the gamma size")

func ramps(fd, size int) ([]uint16, error) {
	buf := make([]byte, 3*2*size+1)
	n, err := readFromStart(fd, buf)
	if errors.Is(err, unix.ESPIPE) {
		n, err = readStream(fd, buf)
	}
	if err != nil {
		return nil, err
	}
	if n != len(buf)-1 {
		return nil, errRampSize
	}
	table := make([]uint16, 3*size)
	for i := range table {
		table[i] = binary.NativeEndian.Uint16(buf[2*i:])
	}
	return table, nil
}

func readFromStart(fd int, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		k, err := unix.Pread(fd, buf[n:], int64(n))
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ESPIPE):
			return 0, err
		case err != nil:
			return n, fmt.Errorf("read the gamma table: %w", err)
		case k == 0:
			return n, nil
		}
		n += k
	}
	return n, nil
}

func readStream(fd int, buf []byte) (int, error) {
	if err := unix.SetNonblock(fd, true); err != nil {
		return 0, fmt.Errorf("make the gamma table fd non-blocking: %w", err)
	}
	n := 0
	for n < len(buf) {
		k, err := unix.Read(fd, buf[n:])
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EAGAIN) || err == nil && k == 0:
			return n, nil
		case err != nil:
			return n, fmt.Errorf("read the gamma table: %w", err)
		}
		n += k
	}
	return n, nil
}

func (k *control) SetGamma(obj *swgc.ZwlrGammaControlV1, fd int) error {
	return errors.Join(k.setGamma(obj, fd), unix.Close(fd))
}

func (k *control) setGamma(obj *swgc.ZwlrGammaControlV1, fd int) error {
	if k.gamma == nil {
		return nil
	}
	table, err := ramps(fd, k.gamma.GammaSize())
	if errors.Is(err, errRampSize) {
		return obj.ProtocolError(swgc.ZwlrGammaControlV1ErrorInvalidGamma, "%v: want %d entries per ramp", err, k.gamma.GammaSize())
	}
	if err == nil {
		err = k.gamma.SetGamma(table)
	}
	if err != nil {
		k.m.report(fmt.Errorf("gammacontrol: %w", err))
		return errors.Join(k.release(), k.res.SendFailed())
	}
	k.set = true
	return nil
}

func (*control) Destroy(*swgc.ZwlrGammaControlV1) error { return nil }

func (k *control) release() error {
	if k.gamma == nil {
		return nil
	}
	g := k.gamma
	k.gamma = nil
	delete(k.m.controls, k.out)
	if !k.set {
		return nil
	}
	return g.SetGamma(nil)
}
