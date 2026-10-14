package surface

import (
	"time"

	"golang.org/x/sys/unix"

	spt "github.com/stubbedev/tilde/internal/protocol/server/presentationtime"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const presentationVersion = 2

type PresentationKind = spt.WpPresentationFeedbackKind

func bindPresentation(c *server.Client, id wire.ObjectID, version uint32) error {
	p, err := spt.NewWpPresentation(c, id, version, presentationRequests{})
	if err != nil {
		return err
	}
	return p.SendClockID(unix.CLOCK_MONOTONIC)
}

type presentationRequests struct{}

func (presentationRequests) Destroy(*spt.WpPresentation) error {
	return nil
}

func (presentationRequests) Feedback(_ *spt.WpPresentation, wl *sw.WlSurface, fb *spt.WpPresentationFeedback) error {
	s, err := From(wl)
	if err != nil {
		return err
	}
	s.pending.feedbacks = append(s.pending.feedbacks, fb)
	return nil
}

func discard(feedbacks []*spt.WpPresentationFeedback) error {
	for _, fb := range feedbacks {
		if err := fb.SendDiscarded(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Surface) Latch() {
	s.latched = append(s.latched, s.current.feedbacks...)
	s.current.feedbacks = nil
}

func (s *Surface) Presented(outputs []*sw.WlOutput, at, refresh time.Duration, seq uint64, kind PresentationKind) error {
	feedbacks := s.latched
	s.latched = nil
	sec, nsec := uint64(at/time.Second), uint32(at%time.Second) //nolint:gosec // a monotonic timestamp is positive and the remainder fits
	for _, fb := range feedbacks {
		for _, o := range outputs {
			if err := fb.SendSyncOutput(o); err != nil {
				return err
			}
		}
		if err := fb.SendPresented(uint32(sec>>32), uint32(sec), nsec, uint32(refresh.Nanoseconds()), uint32(seq>>32), uint32(seq), kind); err != nil { //nolint:gosec // the protocol splits 64-bit values into 32-bit halves
			return err
		}
	}
	return nil
}
