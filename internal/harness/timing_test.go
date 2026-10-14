package harness

import (
	"errors"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cpt "github.com/stubbedev/tilde/internal/protocol/client/presentationtime"
	cspb "github.com/stubbedev/tilde/internal/protocol/client/singlepixelbuffer"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cxa "github.com/stubbedev/tilde/internal/protocol/client/xdgactivation"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wire"
)

type presentationEvents struct{ clock uint32 }

func (p *presentationEvents) ClockID(_ *cpt.WpPresentation, id uint32) error {
	p.clock = id
	return nil
}

type feedbackEvents struct {
	log     []string
	synced  int
	refresh uint32
	flags   cpt.WpPresentationFeedbackKind
	seq     uint64
}

func (f *feedbackEvents) SyncOutput(*cpt.WpPresentationFeedback, *cw.WlOutput) error {
	f.synced++
	return nil
}

func (f *feedbackEvents) Presented(_ *cpt.WpPresentationFeedback, _, _, _, refresh, seqHi, seqLo uint32, flags cpt.WpPresentationFeedbackKind) error {
	f.log = append(f.log, "presented")
	f.refresh, f.flags, f.seq = refresh, flags, uint64(seqHi)<<32|uint64(seqLo)
	return nil
}

func (f *feedbackEvents) Discarded(*cpt.WpPresentationFeedback) error {
	f.log = append(f.log, "discarded")
	return nil
}

func TestPresentationFeedback(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	pe := &presentationEvents{}
	p, err := cpt.NewWpPresentation(c.Display, 2, pe)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("wp_presentation", p)
	w := c.Window("timed", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	if pe.clock != 1 {
		t.Fatalf("clock id %d, want CLOCK_MONOTONIC", pe.clock)
	}
	superseded, shown := &feedbackEvents{}, &feedbackEvents{}
	_, err1 := p.Feedback(w.Surface, superseded)
	err2 := w.Surface.Commit()
	_, err3 := p.Feedback(w.Surface, shown)
	if err := errors.Join(err1, err2, err3, w.Surface.Commit()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("both feedbacks answered", func() bool { return len(superseded.log) > 0 && len(shown.log) > 0 })
	if !slices.Equal(superseded.log, []string{"discarded"}) || !slices.Equal(shown.log, []string{"presented"}) {
		t.Fatalf("superseded %v, shown %v", superseded.log, shown.log)
	}
	if shown.synced != 1 || shown.refresh != 16666666 || shown.flags != cpt.WpPresentationFeedbackKindVsync || shown.seq == 0 {
		t.Fatalf("presented on %d outputs, refresh %d ns, flags %d, seq %d", shown.synced, shown.refresh, shown.flags, shown.seq)
	}
}

type releases struct{ n int }

func (r *releases) Release(*cw.WlBuffer) error {
	r.n++
	return nil
}

func TestSinglePixelBufferFillsItsViewport(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	spb, err := cspb.NewWpSinglePixelBufferManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("wp_single_pixel_buffer_manager_v1", spb)
	w := c.Window("solid", quadrants)
	c.DispatchUntil("the window to map", w.Mapped)
	rel := &releases{}
	buf, err := spb.CreateU32RgbaBuffer(rel, 0, 0x80808080, 0xffffffff, 0xffffffff)
	if err != nil {
		t.Fatal(err)
	}
	vp, err := c.Viewporter.GetViewport(w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(vp.SetDestination(160, 100), w.Surface.Attach(buf, 0, 0), w.Surface.Commit(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the single pixel stretched over the window", showing(150, 90, [3]byte{0, 0x80, 0xff}))
	c.DispatchUntil("the buffer released", func() bool { return rel.n > 0 })
}

type tokenEvents struct{ token string }

func (e *tokenEvents) Done(_ *cxa.XdgActivationTokenV1, token string) error {
	e.token = token
	return nil
}

func requestToken(t *testing.T, c *testclient.Client, act *cxa.XdgActivationV1, seat *cw.WlSeat, serial uint32) string {
	t.Helper()
	ev := &tokenEvents{}
	tok, err := act.GetActivationToken(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(tok.SetSerial(serial, seat), tok.SetAppID("tilde.testclient"), tok.Commit()); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the activation token", func() bool { return ev.token != "" })
	return ev.token
}

func TestActivationMarksWindowsUrgent(t *testing.T) {
	h := Start(t, small)
	a := join(t, h, "a")
	aAct, err := cxa.NewXdgActivationV1(a.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	a.c.Bind("xdg_activation_v1", aAct)
	b := join(t, h, "b")
	bAct, err := cxa.NewXdgActivationV1(b.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	b.c.Bind("xdg_activation_v1", bAct)
	b.seen(t, "b focused", func() bool { return slices.Contains(b.in.Log, "kb_enter b") })

	urgent := func(name string) func(*headless.Backend, *compositor.Compositor) bool {
		return func(_ *headless.Backend, c *compositor.Compositor) bool {
			for _, tl := range c.Toplevels() {
				if tl.Title() == name {
					return c.Urgent(tl)
				}
			}
			return false
		}
	}
	stolen := requestToken(t, a.c, aAct, a.seat, 0)
	if err := errors.Join(aAct.Activate(stolen, a.w.Surface), a.c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	h.Do(func(b *headless.Backend, c *compositor.Compositor) error {
		if urgent("a")(b, c) {
			t.Error("a token from a client the user is not using marked a window urgent")
		}
		return nil
	})
	launched := requestToken(t, b.c, bAct, b.seat, 0)
	if err := errors.Join(aAct.Activate(launched, a.w.Surface), a.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("a urgent from the focused client's token", urgent("a"))
	h.Do(func(_ *headless.Backend, c *compositor.Compositor) error {
		if c.Seat().Focus() != c.Toplevels()[1].Surface() {
			t.Error("an activation stole the keyboard")
		}
		return nil
	})
	if err := errors.Join(aAct.Activate(launched, a.w.Surface), a.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(b.w.Destroy(), b.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	entered := len(a.in.Log)
	a.seen(t, "a focused once b is gone", func() bool { return slices.Contains(a.in.Log[entered:], "kb_enter a") })
	h.Eventually("urgency cleared once a is focused", func(b *headless.Backend, c *compositor.Compositor) bool { return !urgent("a")(b, c) })
}

func TestActivationTokenCommittedTwice(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	act, err := cxa.NewXdgActivationV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("xdg_activation_v1", act)
	tok, err := act.GetActivationToken(&tokenEvents{})
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(tok.Commit(), tok.Commit()); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := c.Display.Roundtrip(); !errors.As(err, &pe) || pe.Code != uint32(cxa.XdgActivationTokenV1ErrorAlreadyUsed) {
		t.Fatalf("Roundtrip = %v, want already_used", err)
	}
}
