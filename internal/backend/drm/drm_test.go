package drm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend"
	kdrm "github.com/stubbedev/tilde/internal/drm"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/soft"
)

const (
	propType = 100 + iota
	propFB
	propPlaneCRTC
	propSrcX
	propSrcY
	propSrcW
	propSrcH
	propCRTCX
	propCRTCY
	propCRTCW
	propCRTCH
	propInFormats
	propActive
	propModeID
	propConnCRTC
	inFormatsBlob = 500
)

type commit struct {
	flags    uint32
	userData uint64
	values   map[[2]uint32]uint64
}

type fakeDevice struct {
	events, write int
	connected     bool
	commits       []commit
	blobs         uint32
	destroyed     []uint32
}

var modes = []kdrm.Mode{
	{Clock: 148500, HDisplay: 1920, HTotal: 2200, VDisplay: 1080, VTotal: 1125, Type: kdrm.ModeTypePreferred},
	{Clock: 74250, HDisplay: 1280, HTotal: 1650, VDisplay: 720, VTotal: 750},
}

func (f *fakeDevice) FD() int { return f.events }

func (f *fakeDevice) Resources() (kdrm.Resources, error) {
	return kdrm.Resources{CRTCs: []uint32{10, 11}, Connectors: []uint32{20}, Encoders: []uint32{30}}, nil
}

func (f *fakeDevice) Connector(id uint32) (kdrm.Connector, error) {
	c := kdrm.Connector{ID: id, Type: 10, TypeID: 1, Encoders: []uint32{30}, Connection: kdrm.Disconnected}
	if f.connected {
		c.Connection, c.Modes = kdrm.Connected, modes
	}
	return c, nil
}

func (f *fakeDevice) Encoder(id uint32) (kdrm.Encoder, error) {
	return kdrm.Encoder{ID: id, PossibleCRTCs: 0b11}, nil
}

func (f *fakeDevice) Planes() ([]uint32, error) { return []uint32{40, 41, 42}, nil }

func (f *fakeDevice) Plane(id uint32) (kdrm.Plane, error) {
	return kdrm.Plane{ID: id, PossibleCRTCs: map[uint32]uint32{40: 0b01, 41: 0b10, 42: 0b11}[id], Formats: []uint32{scanoutFormat}}, nil
}

func (f *fakeDevice) Properties(object, kind uint32) (map[string]kdrm.Property, error) {
	switch kind {
	case kdrm.ObjectPlane:
		typ := uint64(1)
		if object == 42 {
			typ = 0
		}
		return map[string]kdrm.Property{
			"type": {ID: propType, Value: typ}, "FB_ID": {ID: propFB}, "CRTC_ID": {ID: propPlaneCRTC},
			"SRC_X": {ID: propSrcX}, "SRC_Y": {ID: propSrcY}, "SRC_W": {ID: propSrcW}, "SRC_H": {ID: propSrcH},
			"CRTC_X": {ID: propCRTCX}, "CRTC_Y": {ID: propCRTCY}, "CRTC_W": {ID: propCRTCW}, "CRTC_H": {ID: propCRTCH},
			"IN_FORMATS": {ID: propInFormats, Value: inFormatsBlob},
		}, nil
	case kdrm.ObjectCRTC:
		return map[string]kdrm.Property{"ACTIVE": {ID: propActive}, "MODE_ID": {ID: propModeID}}, nil
	case kdrm.ObjectConnector:
		return map[string]kdrm.Property{"CRTC_ID": {ID: propConnCRTC}}, nil
	}
	return nil, fmt.Errorf("unknown object type %#x", kind)
}

func (f *fakeDevice) PropertyInfo(uint32) (kdrm.PropertyInfo, error) {
	return kdrm.PropertyInfo{Name: "type", Enums: map[string]uint64{"Overlay": 0, "Primary": 1, "Cursor": 2}}, nil
}

func (f *fakeDevice) Blob(id uint32) ([]byte, error) {
	if id != inFormatsBlob {
		return nil, fmt.Errorf("no blob %d", id)
	}
	b := binary.NativeEndian.AppendUint32(nil, 1)
	for _, v := range []uint32{0, 1, 24, 2, 32} {
		b = binary.NativeEndian.AppendUint32(b, v)
	}
	b = binary.NativeEndian.AppendUint32(b, scanoutFormat)
	b = binary.NativeEndian.AppendUint32(b, 0)
	for _, m := range []uint64{0, 7} {
		b = binary.NativeEndian.AppendUint64(b, 1)
		b = binary.NativeEndian.AppendUint64(b, 0)
		b = binary.NativeEndian.AppendUint64(b, m)
	}
	return b, nil
}

func (f *fakeDevice) CreateBlob([]byte) (uint32, error) {
	f.blobs++
	return 600 + f.blobs, nil
}

func (f *fakeDevice) DestroyBlob(id uint32) error {
	f.destroyed = append(f.destroyed, id)
	return nil
}

func (f *fakeDevice) Commit(r *kdrm.Request, flags uint32, userData uint64) error {
	c := commit{flags: flags, userData: userData, values: map[[2]uint32]uint64{}}
	r.Each(func(object, property uint32, value uint64) { c.values[[2]uint32{object, property}] = value })
	f.commits = append(f.commits, c)
	return nil
}

func (f *fakeDevice) flip(t *testing.T, crtc uint32, seq uint32) {
	t.Helper()
	ev := make([]byte, 32)
	binary.NativeEndian.PutUint32(ev, kdrm.EventFlipComplete)
	binary.NativeEndian.PutUint32(ev[4:], 32)
	binary.NativeEndian.PutUint64(ev[8:], uint64(crtc))
	binary.NativeEndian.PutUint32(ev[16:], 1)
	binary.NativeEndian.PutUint32(ev[24:], seq)
	binary.NativeEndian.PutUint32(ev[28:], crtc)
	if _, err := unix.Write(f.write, ev); err != nil {
		t.Fatal(err)
	}
}

type fakeAlloc struct {
	next  uint32
	freed int
	soft  *soft.Renderer
}

func (a *fakeAlloc) renderer() render.Renderer { return a.soft }

func (a *fakeAlloc) renderable(uint32) ([]uint64, error) { return []uint64{0, 7, 9}, nil }

func (a *fakeAlloc) allocate(size image.Point, _ uint32, modifiers []uint64) (*scanout, error) {
	if !slices.Equal(modifiers, []uint64{0, 7}) {
		return nil, fmt.Errorf("allocated with modifiers %v", modifiers)
	}
	a.next++
	return &scanout{target: soft.NewBuffer(size.X, size.Y), fb: 900 + a.next, free: func() error { a.freed++; return nil }}, nil
}

func (a *fakeAlloc) finish() error { return nil }
func (a *fakeAlloc) close() error  { return nil }

type recorder struct {
	log      []string
	outputs  []backend.Output
	presents []backend.Presentation
}

func (r *recorder) OutputAdded(o backend.Output) error {
	r.outputs = append(r.outputs, o)
	r.log = append(r.log, "added "+o.Name())
	return nil
}

func (r *recorder) OutputRemoved(o backend.Output) error {
	r.log = append(r.log, "removed "+o.Name())
	return nil
}

func (r *recorder) OutputChanged(backend.Output) error { return nil }

func (r *recorder) Frame(o backend.Output) error {
	r.log = append(r.log, "frame")
	f, err := o.BeginFrame()
	if err != nil {
		return err
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rect(0, 0, 16, 16)})
	if err != nil {
		return err
	}
	p.FillRect(image.Rect(0, 0, 16, 16), render.Color{A: 0xff})
	return errors.Join(p.End(), o.Present(nil))
}

func (r *recorder) Presented(_ backend.Output, p backend.Presentation) error {
	r.presents = append(r.presents, p)
	r.log = append(r.log, fmt.Sprintf("presented %d", p.Seq))
	return nil
}

func (r *recorder) DeviceAdded(backend.Device) error          { return nil }
func (r *recorder) DeviceRemoved(backend.Device) error        { return nil }
func (r *recorder) Input(backend.Device, backend.Event) error { return nil }

type rig struct {
	t   *testing.T
	l   *loop.Loop
	dev *fakeDevice
	b   *Backend
	rec *recorder
}

func start(t *testing.T) *rig {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = unix.Close(p[0]), unix.Close(p[1]) })
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	r := &rig{t: t, l: l, dev: &fakeDevice{events: p[0], write: p[1], connected: true}, rec: &recorder{}}
	if r.b, err = newBackend(l, r.dev, &fakeAlloc{soft: soft.New()}); err != nil {
		t.Fatal(err)
	}
	if err := r.b.Start(r.rec); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) settle() {
	r.t.Helper()
	timer, err := r.l.AddTimer(func() error {
		r.l.Stop()
		return nil
	})
	if err != nil {
		r.t.Fatal(err)
	}
	defer timer.Remove()
	if err := timer.Arm(20 * time.Millisecond); err != nil {
		r.t.Fatal(err)
	}
	if err := r.l.Run(); err != nil {
		r.t.Fatal(err)
	}
}

func TestOutputsPickACRTCAndPrimaryPlane(t *testing.T) {
	r := start(t)
	if len(r.rec.outputs) != 1 {
		t.Fatalf("outputs %v", r.rec.log)
	}
	o := r.b.Outputs()[0]
	if o.Name() != "DP-1" || o.crtc.id != 10 || o.primary.id != 40 || len(o.buffers) != buffersPerOut || !slices.Equal(o.primary.modifiers, []uint64{0, 7}) {
		t.Fatalf("%s on CRTC %d plane %d, %d buffers, modifiers %v", o.Name(), o.crtc.id, o.primary.id, len(o.buffers), o.primary.modifiers)
	}
	if s := o.State(); !s.Enabled || s.Mode.Size != image.Pt(1920, 1080) || s.Mode.RefreshMHz != 60000 || len(o.Modes()) != 2 {
		t.Fatalf("state %+v, modes %v", s, o.Modes())
	}
}

func TestFramesCommitAndFlip(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.dev.commits) != 2 {
		t.Fatalf("%d commits after the first frame", len(r.dev.commits))
	}
	test, real := r.dev.commits[0], r.dev.commits[1]
	if test.flags != kdrm.AtomicTestOnly|kdrm.AtomicAllowModeset || real.flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent|kdrm.AtomicAllowModeset || real.userData != 10 {
		t.Fatalf("flags %#x then %#x, user data %d", test.flags, real.flags, real.userData)
	}
	want := map[[2]uint32]uint64{
		{20, propConnCRTC}: 10, {10, propActive}: 1, {10, propModeID}: 601,
		{40, propFB}: 901, {40, propPlaneCRTC}: 10, {40, propSrcW}: 1920 << 16, {40, propSrcH}: 1080 << 16,
		{40, propCRTCW}: 1920, {40, propCRTCH}: 1080, {40, propSrcX}: 0, {40, propSrcY}: 0, {40, propCRTCX}: 0, {40, propCRTCY}: 0,
	}
	for k, v := range want {
		if got, ok := real.values[k]; !ok || got != v {
			t.Errorf("object %d property %d = %d (%v), want %d", k[0], k[1], got, ok, v)
		}
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.dev.commits) != 2 {
		t.Fatal("a frame was drawn while the previous flip was pending")
	}
	r.dev.flip(t, 10, 77)
	r.settle()
	if len(r.rec.presents) != 1 || r.rec.presents[0].Seq != 77 || r.rec.presents[0].Time == 0 {
		t.Fatalf("presents %+v", r.rec.presents)
	}
	if len(r.dev.commits) != 4 {
		t.Fatalf("%d commits after the flip released the queued frame", len(r.dev.commits))
	}
	second := r.dev.commits[3]
	if second.flags&kdrm.AtomicAllowModeset != 0 || second.values[[2]uint32{40, propFB}] != 902 {
		t.Fatalf("the second frame: flags %#x fb %d", second.flags, second.values[[2]uint32{40, propFB}])
	}
	if _, ok := second.values[[2]uint32{10, propModeID}]; ok {
		t.Fatal("the second frame repeated the modeset")
	}
}

func TestModeChangesAndDisabling(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.Commit(backend.OutputState{Enabled: true, Mode: modeOf(modes[1])}); err != nil {
		t.Fatal(err)
	}
	r.settle()
	last := r.dev.commits[len(r.dev.commits)-1]
	if last.flags&kdrm.AtomicAllowModeset == 0 || last.values[[2]uint32{40, propCRTCW}] != 1280 || !slices.Contains(r.dev.destroyed, 601) {
		t.Fatalf("mode change commit %+v, destroyed blobs %v", last, r.dev.destroyed)
	}
	if err := o.Commit(backend.OutputState{Enabled: true, Mode: backend.Mode{Size: image.Pt(1, 1), RefreshMHz: 1}}); err == nil {
		t.Fatal("a mode the connector lacks was accepted")
	}
	if err := o.Commit(backend.OutputState{}); err != nil {
		t.Fatal(err)
	}
	off := r.dev.commits[len(r.dev.commits)-1]
	if off.flags != kdrm.AtomicAllowModeset || off.values[[2]uint32{10, propActive}] != 0 || off.values[[2]uint32{40, propFB}] != 0 || o.State().Enabled || len(o.buffers) != 0 {
		t.Fatalf("disable commit %+v, state %+v", off, o.State())
	}
}

func TestHotplugAndSessionPause(t *testing.T) {
	r := start(t)
	r.dev.connected = false
	if err := r.b.Hotplug(); err != nil {
		t.Fatal(err)
	}
	r.dev.connected = true
	if err := r.b.Hotplug(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.rec.log, []string{"added DP-1", "removed DP-1", "added DP-1"}) {
		t.Fatalf("log %v", r.rec.log)
	}
	o := r.b.Outputs()[0]
	r.b.Pause()
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.dev.commits) != 0 {
		t.Fatalf("%d commits while paused", len(r.dev.commits))
	}
	if err := r.b.Resume(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.dev.commits) != 2 || r.dev.commits[1].flags&kdrm.AtomicAllowModeset == 0 {
		t.Fatalf("after resume: %+v", r.dev.commits)
	}
}
