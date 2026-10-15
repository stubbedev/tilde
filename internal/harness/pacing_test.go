package harness

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	cct "github.com/stubbedev/tilde/internal/protocol/client/committiming"
	cff "github.com/stubbedev/tilde/internal/protocol/client/fifo"
	cpt "github.com/stubbedev/tilde/internal/protocol/client/presentationtime"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/testclient"
)

type paced struct {
	c      *testclient.Client
	p      *cpt.WpPresentation
	fifos  *cff.WpFifoManagerV1
	timing *cct.WpCommitTimingManagerV1
}

func pace(t *testing.T, h *Harness) *paced {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	p, err := cpt.NewWpPresentation(c.Display, 2, &presentationEvents{})
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(p)
	fifos, err := cff.NewWpFifoManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(fifos)
	timing, err := cct.NewWpCommitTimingManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(timing)
	return &paced{c: c, p: p, fifos: fifos, timing: timing}
}

func (pc *paced) window(t *testing.T) *testclient.Window {
	t.Helper()
	w := pc.c.Window("paced", quadrants)
	pc.c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	return w
}

func (pc *paced) update(t *testing.T, s *cw.WlSurface, buffer *cw.WlBuffer, before ...func() error) *feedbackEvents {
	t.Helper()
	fb := &feedbackEvents{}
	_, err := pc.p.Feedback(s, fb)
	errs := []error{err}
	for _, f := range before {
		errs = append(errs, f())
	}
	if buffer != nil {
		errs = append(errs, s.Attach(buffer, 0, 0), s.DamageBuffer(0, 0, 1, 1))
	}
	if err := errors.Join(append(errs, s.Commit())...); err != nil {
		t.Fatal(err)
	}
	return fb
}

func answered(feedbacks ...*feedbackEvents) func() bool {
	return func() bool {
		return !slices.ContainsFunc(feedbacks, func(fb *feedbackEvents) bool { return len(fb.log) == 0 })
	}
}

func TestFifoShowsEveryQueuedUpdateForARefresh(t *testing.T) {
	h := Start(t, small)
	pc := pace(t, h)
	w := pc.window(t)

	unpaced := make([]*feedbackEvents, 3)
	for i := range unpaced {
		unpaced[i] = pc.update(t, w.Surface, pc.c.Buffer(w.Size, i, quadrants))
	}
	pc.c.DispatchUntil("the unpaced updates answered", answered(unpaced...))
	if !slices.Equal(unpaced[0].log, []string{"discarded"}) {
		t.Fatalf("without fifo the first of three quick updates was %v, want discarded, so this test could not tell fifo apart", unpaced[0].log)
	}

	f, err := pc.fifos.GetFifo(w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	queued := make([]*feedbackEvents, 4)
	for i := range queued {
		queued[i] = pc.update(t, w.Surface, pc.c.Buffer(w.Size, i, quadrants), f.SetBarrier, f.WaitBarrier)
	}
	pc.c.DispatchUntil("the queued updates answered", answered(queued...))
	for i, fb := range queued {
		if !slices.Equal(fb.log, []string{"presented"}) {
			t.Fatalf("queued update %d was %v, want presented", i, fb.log)
		}
		if i > 0 && fb.seq <= queued[i-1].seq {
			t.Fatalf("queued update %d presented at refresh %d, not after update %d at %d", i, fb.seq, i-1, queued[i-1].seq)
		}
	}
}

func TestAFifoBarrierOnAHiddenSurfaceDoesNotStall(t *testing.T) {
	h := Start(t, small)
	pc := pace(t, h)
	s := pc.c.Surface()
	f, err := pc.fifos.GetFifo(s)
	if err != nil {
		t.Fatal(err)
	}
	updates := make([]*feedbackEvents, 3)
	for i := range updates {
		updates[i] = pc.update(t, s, nil, f.SetBarrier, f.WaitBarrier)
	}
	pc.c.DispatchUntil("the first two updates superseded", answered(updates[:2]...))
	for i, fb := range updates[:2] {
		if !slices.Equal(fb.log, []string{"discarded"}) {
			t.Fatalf("update %d of a surface no output shows was %v, want discarded once the next applied", i, fb.log)
		}
	}
}

func timestamp(at time.Duration) (hi, lo, nsec uint32) {
	sec := uint64(at / time.Second)
	return uint32(sec >> 32), uint32(sec), uint32(at % time.Second)
}

func TestCommitTimingHoldsAnUpdateUntilItsTime(t *testing.T) {
	h := Start(t, small)
	pc := pace(t, h)
	w := pc.window(t)
	timer, err := pc.timing.GetTimer(w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	const delay = 300 * time.Millisecond
	target := backend.Now() + delay
	timed := pc.update(t, w.Surface, pc.c.Buffer(w.Size, 0, testclient.Fill(0, 0xff, 0)), func() error { return timer.SetTimestamp(timestamp(target)) })
	after := pc.update(t, w.Surface, pc.c.Buffer(w.Size, 1, testclient.Fill(0xff, 0, 0)))
	pc.c.DispatchUntil("both updates answered", answered(timed, after))
	if !slices.Equal(timed.log, []string{"discarded"}) || !slices.Equal(after.log, []string{"presented"}) {
		t.Fatalf("the timed update was %v and the one after it %v, want the second to apply right behind the first", timed.log, after.log)
	}
	if after.at < target {
		t.Fatalf("an update queued behind a timed one was presented %v before the target", target-after.at)
	}
	if late := after.at - target; late > delay {
		t.Fatalf("the timed update was presented %v after its target", late)
	}
}

func TestPacingProtocolErrors(t *testing.T) {
	cases := map[string]struct {
		code uint32
		run  func(pc *paced, s *cw.WlSurface) error
	}{
		"a second fifo": {uint32(cff.WpFifoManagerV1ErrorAlreadyExists), func(pc *paced, s *cw.WlSurface) error {
			_, err1 := pc.fifos.GetFifo(s)
			_, err2 := pc.fifos.GetFifo(s)
			return errors.Join(err1, err2)
		}},
		"a second commit timer": {uint32(cct.WpCommitTimingManagerV1ErrorCommitTimerExists), func(pc *paced, s *cw.WlSurface) error {
			_, err1 := pc.timing.GetTimer(s)
			_, err2 := pc.timing.GetTimer(s)
			return errors.Join(err1, err2)
		}},
		"a nanosecond count of a second": {uint32(cct.WpCommitTimerV1ErrorInvalidTimestamp), func(pc *paced, s *cw.WlSurface) error {
			timer, err := pc.timing.GetTimer(s)
			return errors.Join(err, timer.SetTimestamp(0, 1, uint32(time.Second)))
		}},
		"two timestamps for one commit": {uint32(cct.WpCommitTimerV1ErrorTimestampExists), func(pc *paced, s *cw.WlSurface) error {
			timer, err := pc.timing.GetTimer(s)
			return errors.Join(err, timer.SetTimestamp(0, 1, 0), timer.SetTimestamp(0, 2, 0))
		}},
		"a barrier after the surface is gone": {uint32(cff.WpFifoV1ErrorSurfaceDestroyed), func(pc *paced, s *cw.WlSurface) error {
			f, err := pc.fifos.GetFifo(s)
			return errors.Join(err, s.Destroy(), f.SetBarrier())
		}},
		"a wait after the surface is gone": {uint32(cff.WpFifoV1ErrorSurfaceDestroyed), func(pc *paced, s *cw.WlSurface) error {
			f, err := pc.fifos.GetFifo(s)
			return errors.Join(err, s.Destroy(), f.WaitBarrier())
		}},
		"a timestamp after the surface is gone": {uint32(cct.WpCommitTimerV1ErrorSurfaceDestroyed), func(pc *paced, s *cw.WlSurface) error {
			timer, err := pc.timing.GetTimer(s)
			return errors.Join(err, s.Destroy(), timer.SetTimestamp(0, 1, 0))
		}},
	}
	h := Start(t, small)
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pc := pace(t, h)
			if err := tc.run(pc, pc.c.Surface()); err != nil {
				t.Fatal(err)
			}
			pc.c.ExpectProtocolError(tc.code)
		})
	}
}

func TestAFifoAndATimerCanBeMadeAgainAfterTheirObjectsAreGone(t *testing.T) {
	h := Start(t, small)
	pc := pace(t, h)
	s := pc.c.Surface()
	f, err := pc.fifos.GetFifo(s)
	if err != nil {
		t.Fatal(err)
	}
	timer, err := pc.timing.GetTimer(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Destroy(), timer.Destroy()); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.fifos.GetFifo(s); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.timing.GetTimer(s); err != nil {
		t.Fatal(err)
	}
	if err := pc.c.Display.Roundtrip(); err != nil {
		t.Fatalf("a fifo and a timer made again after their objects were destroyed: %v", err)
	}
}
