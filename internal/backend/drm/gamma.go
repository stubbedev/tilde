package drm

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/stubbedev/tilde/internal/backend"
	kdrm "github.com/stubbedev/tilde/internal/drm"
)

var _ backend.Gamma = (*Output)(nil)

type gamma struct {
	current uint32
	next    uint32
	dirty   bool
}

func (o *Output) GammaSize() int {
	if _, ok := o.crtc.props["GAMMA_LUT"]; !ok {
		return 0
	}
	return int(o.crtc.props["GAMMA_LUT_SIZE"].Value) //nolint:gosec // a LUT size is a small count
}

func lut(ramps []uint16) []byte {
	n := len(ramps) / 3
	b := make([]byte, 0, 8*n)
	for i := range n {
		for _, v := range []uint16{ramps[i], ramps[n+i], ramps[2*n+i], 0} {
			b = binary.NativeEndian.AppendUint16(b, v)
		}
	}
	return b
}

func (o *Output) SetGamma(ramps []uint16) error {
	if size := o.GammaSize(); ramps != nil && len(ramps) != 3*size {
		return fmt.Errorf("drm: %s takes 3 ramps of %d gamma entries, not %d entries", o.Name(), size, len(ramps))
	}
	var blob uint32
	if ramps != nil {
		var err error
		if blob, err = o.b.dev.CreateBlob(lut(ramps)); err != nil {
			return fmt.Errorf("drm: the gamma LUT of %s: %w", o.Name(), err)
		}
	}
	if o.lit && !o.off && !o.b.paused {
		r := &o.b.req
		r.Reset()
		o.crtc.set(&o.b.req, "GAMMA_LUT", uint64(blob))
		if err := o.test(kdrm.AtomicNonblock); err != nil {
			return errors.Join(fmt.Errorf("drm: %s refuses the gamma LUT: %w", o.Name(), err), o.destroyBlob(blob))
		}
	}
	err := o.dropPendingGamma()
	o.gamma.next, o.gamma.dirty = blob, true
	return errors.Join(err, o.flushPlanes())
}

func (o *Output) destroyBlob(blob uint32) error {
	if blob == 0 {
		return nil
	}
	return o.b.dev.DestroyBlob(blob)
}

func (o *Output) dropPendingGamma() error {
	if !o.gamma.dirty {
		return nil
	}
	o.gamma.dirty = false
	return o.destroyBlob(o.gamma.next)
}

func (o *Output) addGamma() {
	switch {
	case o.gamma.dirty:
		o.crtc.set(&o.b.req, "GAMMA_LUT", uint64(o.gamma.next))
	case o.modeset:
		o.crtc.set(&o.b.req, "GAMMA_LUT", uint64(o.gamma.current))
	}
}

func (o *Output) gammaSent() error {
	if !o.gamma.dirty {
		return nil
	}
	old := o.gamma.current
	o.gamma.current, o.gamma.dirty = o.gamma.next, false
	return o.destroyBlob(old)
}

func (o *Output) releaseGamma() error {
	err := errors.Join(o.dropPendingGamma(), o.destroyBlob(o.gamma.current))
	o.gamma = gamma{}
	return err
}
