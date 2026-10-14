package seat

import (
	"errors"
	"time"

	srp "github.com/stubbedev/tilde/internal/protocol/server/relativepointer"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const relativePointerVersion = 1

func (s *Seat) bindRelativePointer(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := srp.NewZwpRelativePointerManagerV1(c, id, version, relativePointers{s, c})
	return err
}

type relativePointers struct {
	s *Seat
	c *server.Client
}

func (relativePointers) Destroy(*srp.ZwpRelativePointerManagerV1) error { return nil }

func (m relativePointers) GetRelativePointer(_ *srp.ZwpRelativePointerManagerV1, res *srp.ZwpRelativePointerV1, _ *sw.WlPointer) (srp.ZwpRelativePointerV1Handler, error) {
	r := m.s.resources(m.c)
	track(m.s, r, &r.relatives, res)
	return relativePointer{}, nil
}

type relativePointer struct{}

func (relativePointer) Destroy(*srp.ZwpRelativePointerV1) error { return nil }

func (s *Seat) RelativeMotion(at time.Duration, dx, dy, unaccelDX, unaccelDY float64) error {
	if s.pointer.focus == nil {
		return nil
	}
	c := s.pointer.focus.Client()
	r := s.lookup(c)
	if r == nil {
		return nil
	}
	us := uint64(max(at, 0) / time.Microsecond)
	errs := make([]error, 0, len(r.relatives))
	for _, rel := range r.relatives {
		errs = append(errs, c.Sent(rel.SendRelativeMotion(uint32(us>>32), uint32(us), //nolint:gosec // split as the protocol asks
			wire.FixedFromFloat(dx), wire.FixedFromFloat(dy), wire.FixedFromFloat(unaccelDX), wire.FixedFromFloat(unaccelDY))))
	}
	return errors.Join(errs...)
}
