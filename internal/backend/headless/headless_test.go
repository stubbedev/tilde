package headless

import (
	"errors"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/fourcc"
	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
)

var _ backend.Backend = (*Backend)(nil)

var mode100 = backend.Mode{Size: image.Pt(64, 48), RefreshMHz: 100000, Preferred: true}

type recorder struct {
	log       []string
	presented []backend.Presentation
	inputs    []backend.Event
	onFrame   func(backend.Output)
	err       error
}

func (r *recorder) note(s string) error {
	r.log = append(r.log, s)
	return nil
}

func (r *recorder) OutputAdded(o backend.Output) error   { return r.note("output+ " + o.Name()) }
func (r *recorder) OutputRemoved(o backend.Output) error { return r.note("output- " + o.Name()) }
func (r *recorder) OutputChanged(o backend.Output) error { return r.note("output~ " + o.Name()) }
func (r *recorder) OutputDamaged(o backend.Output) error { return r.note("damaged " + o.Name()) }
func (r *recorder) DeviceAdded(d backend.Device) error   { return r.note("device+ " + d.Name()) }
func (r *recorder) DeviceRemoved(d backend.Device) error { return r.note("device- " + d.Name()) }

func (r *recorder) Frame(o backend.Output) error {
	r.log = append(r.log, "frame "+o.Name())
	if r.onFrame != nil {
		r.onFrame(o)
	}
	return r.err
}

func (r *recorder) Presented(o backend.Output, p backend.Presentation) error {
	r.presented = append(r.presented, p)
	return r.note("presented " + o.Name())
}

func (r *recorder) Input(d backend.Device, e backend.Event) error {
	r.inputs = append(r.inputs, e)
	return r.note("input " + d.Name())
}

func setup(t *testing.T) (*loop.Loop, *Backend, *recorder) {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	b := New(l)
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	})
	return l, b, &recorder{}
}

func runFor(t *testing.T, l *loop.Loop, d time.Duration) {
	t.Helper()
	stop, err := l.AddTimer(func() error {
		l.Stop()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop.Remove()
	if err := stop.Arm(d); err != nil {
		t.Fatal(err)
	}
	if err := l.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestStartAnnouncesDevicesThenOutputs(t *testing.T) {
	_, b, r := setup(t)
	if _, err := b.AddOutput(mode100); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(r); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddOutput(mode100); err != nil {
		t.Fatal(err)
	}
	want := []string{"device+ headless-keyboard", "device+ headless-pointer", "device+ headless-touch", "device+ headless-switches", "output+ HEADLESS-1", "output+ HEADLESS-2"}
	if !slices.Equal(r.log, want) {
		t.Fatalf("log = %v, want %v", r.log, want)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.log, "output- HEADLESS-1") || !slices.Contains(r.log, "device- headless-touch") {
		t.Fatalf("Close did not announce removals: %v", r.log)
	}
	if err := b.Start(r); err == nil {
		t.Fatal("second Start succeeded")
	}
}

func TestFramesArePacedByRefresh(t *testing.T) {
	l, b, r := setup(t)
	if err := b.Start(r); err != nil {
		t.Fatal(err)
	}
	o, err := b.AddOutput(mode100)
	if err != nil {
		t.Fatal(err)
	}
	frames := 0
	r.onFrame = func(out backend.Output) {
		frames++
		f, err := out.BeginFrame()
		if err != nil {
			t.Fatal(err)
		}
		if want := min(frames-1, 1); f.Age != want {
			t.Fatalf("frame %d has age %d, want %d", frames, f.Age, want)
		}
		p, err := f.Renderer.Begin(f.Target, render.Region{image.Rect(0, 0, 64, 48)})
		if err != nil {
			t.Fatal(err)
		}
		p.FillRect(image.Rect(0, 0, 64, 48), render.Color{G: byte(frames), A: 0xff})
		if err := p.End(); err != nil {
			t.Fatal(err)
		}
		if err := out.Present(nil); err != nil {
			t.Fatal(err)
		}
		if frames < 6 {
			if err := out.ScheduleFrame(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	runFor(t, l, 150*time.Millisecond)
	if frames != 6 || len(r.presented) != 6 {
		t.Fatalf("%d frames and %d presentations, want 6 each", frames, len(r.presented))
	}
	interval := mode100.Interval()
	for i := 1; i < len(r.presented); i++ {
		prev, cur := r.presented[i-1], r.presented[i]
		if cur.Seq <= prev.Seq || cur.Time-prev.Time < interval*9/10 || cur.Refresh != interval {
			t.Fatalf("presentation %d = %+v after %+v: want a later vblank at least %v apart", i, cur, prev, interval)
		}
	}
	snap, err := o.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.W != 64 || snap.H != 48 || snap.Pix[1] != 6 {
		t.Fatalf("snapshot %dx%d green %d, want the sixth frame's fill", snap.W, snap.H, snap.Pix[1])
	}
}

func TestFramesRequestedBetweenVblanksMakeTheNextOne(t *testing.T) {
	l, b, r := setup(t)
	if err := b.Start(r); err != nil {
		t.Fatal(err)
	}
	o, err := b.AddOutput(mode100)
	if err != nil {
		t.Fatal(err)
	}
	client, err := l.AddTimer(o.ScheduleFrame)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Remove()
	const want = 8
	var renderedAt []time.Duration
	r.onFrame = func(out backend.Output) {
		if len(r.presented) >= want {
			l.Stop()
			return
		}
		f, err := out.BeginFrame()
		if err != nil {
			t.Fatal(err)
		}
		p, err := f.Renderer.Begin(f.Target, nil)
		if err != nil {
			t.Fatal(err)
		}
		renderedAt = append(renderedAt, backend.Now())
		if err := errors.Join(p.End(), out.Present(nil), client.Arm(2*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	runFor(t, l, 5*time.Second)
	if len(r.presented) < want {
		t.Fatalf("%d presentations in 5 s at 100 Hz, want %d", len(r.presented), want)
	}
	// The wall clock decides when a frame renders: under load the
	// client's request can land after the next vblank, and skipping it
	// is then correct. What must hold whatever the load: a frame
	// rendered before the next vblank is presented at exactly that one.
	interval := mode100.Interval()
	onTime := 0
	for i := 1; i < len(r.presented); i++ {
		prev, cur := r.presented[i-1], r.presented[i]
		if renderedAt[i] >= prev.Time+interval {
			if cur.Seq <= prev.Seq || cur.Time < renderedAt[i] {
				t.Fatalf("presentation %d = %+v after %+v: a late frame went backwards", i, cur, prev)
			}
			continue
		}
		onTime++
		if cur.Seq != prev.Seq+1 || cur.Time-prev.Time != interval {
			t.Fatalf("presentation %d = %+v after %+v, rendered %v after it: want the next vblank, %v later",
				i, cur, prev, renderedAt[i]-prev.Time, interval)
		}
	}
	if onTime == 0 {
		t.Fatal("no frame rendered between vblanks: the request never reached the frame clock in time")
	}
}

func TestIdleOutputStaysQuiet(t *testing.T) {
	l, b, r := setup(t)
	if _, err := b.AddOutput(mode100); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(r); err != nil {
		t.Fatal(err)
	}
	before := len(r.log)
	runFor(t, l, 100*time.Millisecond)
	if len(r.log) != before {
		t.Fatalf("an output nobody scheduled produced %v", r.log[before:])
	}
}

func TestCommitResizesAndDisables(t *testing.T) {
	l, b, r := setup(t)
	if err := b.Start(r); err != nil {
		t.Fatal(err)
	}
	small := backend.Mode{Size: image.Pt(32, 16), RefreshMHz: 60000}
	o, err := b.AddOutput(mode100, small)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: small}); err != nil {
		t.Fatal(err)
	}
	if f, err := o.BeginFrame(); err != nil || f.Target.Size() != small.Size || f.Age != 0 {
		t.Fatalf("frame %+v, %v; want a fresh %v target", f, err, small.Size)
	}
	if err := o.Present(nil); err != nil {
		t.Fatal(err)
	}
	if err := o.Commit(backend.OutputState{}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.BeginFrame(); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("BeginFrame on a disabled output = %v", err)
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	before := len(r.log)
	runFor(t, l, 50*time.Millisecond)
	if slices.Contains(r.log[before:], "frame HEADLESS-1") {
		t.Fatal("a disabled output produced a frame")
	}
	if err := o.Commit(backend.OutputState{Power: backend.PoweredOn, Mode: backend.Mode{Size: image.Pt(0, 1), RefreshMHz: 1}}); err == nil {
		t.Fatal("an invalid mode was committed")
	}
}

func TestHandlerErrorsStopTheLoop(t *testing.T) {
	l, b, r := setup(t)
	if err := b.Start(r); err != nil {
		t.Fatal(err)
	}
	o, err := b.AddOutput(mode100)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	r.onFrame = func(backend.Output) { r.err = boom }
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	if err := l.Run(); !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want the frame handler's error", err)
	}
	if err := New(l).Keyboard().Key(1, true); err == nil {
		t.Fatal("input before Start was accepted")
	}
}

func TestOutputValidationAndRemoval(t *testing.T) {
	_, b, r := setup(t)
	if err := b.Start(r); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddOutput(); err == nil {
		t.Fatal("an output without modes was added")
	}
	if _, err := b.AddOutput(backend.Mode{Size: image.Pt(10, 10)}); err == nil {
		t.Fatal("a mode without a refresh rate was accepted")
	}
	o, err := b.AddOutput(mode100)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.RemoveOutput(o); err != nil {
		t.Fatal(err)
	}
	if r.log[len(r.log)-1] != "output- "+o.Name() {
		t.Fatalf("log = %v", r.log)
	}
	if _, err := o.BeginFrame(); err == nil {
		t.Fatal("BeginFrame on a removed output succeeded")
	}
	if err := b.RemoveOutput(o); err == nil {
		t.Fatal("an output was removed twice")
	}
}

func TestInputInjection(t *testing.T) {
	_, b, r := setup(t)
	if err := b.Start(r); err != nil {
		t.Fatal(err)
	}
	o, err := b.AddOutput(mode100)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		b.Keyboard().Key(30, true),
		b.Pointer().Move(1.5, -2),
		b.Pointer().MoveTo(o, 0.25, 0.75),
		b.Pointer().Button(272, true),
		b.Pointer().Axis(backend.Vertical, backend.Wheel, 15, 120),
		b.Pointer().Frame(),
		b.Touch().Down(3, o, 0.5, 0.5),
		b.Touch().Motion(3, 0.6, 0.5),
		b.Touch().Up(3),
		b.Touch().Cancel(4),
		b.Touch().Frame(),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := r.inputs
	if len(got) != 11 {
		t.Fatalf("%d input events, want 11", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp() < got[i-1].Timestamp() {
			t.Fatalf("event %d is timestamped before event %d", i, i-1)
		}
	}
	checks := []bool{
		got[0].(backend.Key).Code == 30 && got[0].(backend.Key).Pressed,
		got[1].(backend.PointerMotion).DX == 1.5 && got[1].(backend.PointerMotion).UnaccelDY == -2,
		got[2].(backend.PointerMotionAbsolute).Output == o && got[2].(backend.PointerMotionAbsolute).Y == 0.75,
		got[3].(backend.PointerButton).Button == 272,
		got[4].(backend.PointerAxis).Value120 == 120,
		got[6].(backend.TouchDown).ID == 3 && got[6].(backend.TouchDown).Output == o,
		got[7].(backend.TouchMotion).X == 0.6,
		got[8].(backend.TouchUp).ID == 3,
		got[9].(backend.TouchCancel).ID == 4,
	}
	for i, ok := range checks {
		if !ok {
			t.Errorf("check %d failed: %#v", i, got)
		}
	}
	wantDevices := []string{"input headless-keyboard", "input headless-pointer", "input headless-touch"}
	for _, d := range wantDevices {
		if !slices.Contains(r.log, d) {
			t.Errorf("no event from %s", d)
		}
	}
	if !b.Keyboard().Capabilities().Has(backend.Keyboard) || b.Pointer().Capabilities().Has(backend.Touch) {
		t.Fatal("device capabilities are wrong")
	}
}

func TestClientBuffersAreReleasedWhenTheOutputStopsShowingThem(t *testing.T) {
	for name, stop := range map[string]func(b *Backend, o *Output) error{
		"removed":  func(b *Backend, o *Output) error { return b.RemoveOutput(o) },
		"disabled": func(_ *Backend, o *Output) error { return o.Commit(backend.OutputState{}) },
		"powered off": func(_ *Backend, o *Output) error {
			return o.Commit(backend.OutputState{Power: backend.PoweredOff, Mode: mode100})
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, b, r := setup(t)
			if err := b.Start(r); err != nil {
				t.Fatal(err)
			}
			o, err := b.AddOutput(mode100)
			if err != nil {
				t.Fatal(err)
			}
			format := gpu.FormatModifier{Format: fourcc.XRGB8888, Modifier: fourcc.Linear}
			o.AllowScanout(gpu.ScanoutTarget{Formats: []gpu.FormatModifier{format}})
			released := 0
			buf := gpu.DMABUF{Size: mode100.Size, Format: format.Format, Modifier: format.Modifier}
			if ok, err := o.Scanout(buf, func() error { released++; return nil }, false); !ok || err != nil {
				t.Fatalf("Scanout = %t, %v", ok, err)
			}
			if err := stop(b, o); err != nil {
				t.Fatal(err)
			}
			if released != 1 {
				t.Fatalf("the client buffer was released %d times", released)
			}
		})
	}
}
