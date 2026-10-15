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
	"github.com/stubbedev/tilde/internal/gpu"
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
	propInFence
	propHotspotX
	propHotspotY
	propGammaLUT
	propGammaSize
	inFormatsBlob = 500
	fakeNumber    = 0xe200
)

type commit struct {
	flags    uint32
	userData uint64
	values   map[[2]uint32]uint64
}

type fakeDevice struct {
	events, write int
	connected     bool
	refuseCursor  bool
	refuseGamma   bool
	refuseAsync   bool
	asyncFlips    bool
	commits       []commit
	blobs         uint32
	blobData      map[uint32][]byte
	destroyed     []uint32
	dumbs, fbs    uint32
	live          map[uint32]bool
	pix           map[uint32][]byte
	scanouts      map[uint32]uint32
	removedFBs    int
}

var modes = []kdrm.Mode{
	{Clock: 148500, HDisplay: 1920, HTotal: 2200, VDisplay: 1080, VTotal: 1125, Type: kdrm.ModeTypePreferred},
	{Clock: 74250, HDisplay: 1280, HTotal: 1650, VDisplay: 720, VTotal: 750},
}

func (f *fakeDevice) FD() int { return f.events }

func (f *fakeDevice) Number() (uint64, error) { return fakeNumber, nil }

func (f *fakeDevice) Cap(c uint64) (uint64, error) {
	switch c {
	case kdrm.CapCursorWidth, kdrm.CapCursorHeight:
		return 64, nil
	case kdrm.CapAddFB2Modifiers:
		return 1, nil
	case kdrm.CapAtomicAsyncFlip:
		if f.asyncFlips {
			return 1, nil
		}
		return 0, unix.EINVAL
	}
	return 0, fmt.Errorf("unknown cap %#x", c)
}

func (f *fakeDevice) PrimeFDToHandle(fd int) (uint32, error) {
	return uint32(primeHandles + fd), nil
}

func (f *fakeDevice) CloseHandle(uint32) error { return nil }

const primeHandles = 1000

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

func (f *fakeDevice) Planes() ([]uint32, error) { return []uint32{40, 41, 42, 43}, nil }

func (f *fakeDevice) Plane(id uint32) (kdrm.Plane, error) {
	format := scanoutFormat
	if id == 43 {
		format = cursorFormat
	}
	return kdrm.Plane{ID: id, PossibleCRTCs: map[uint32]uint32{40: 0b01, 41: 0b10, 42: 0b11, 43: 0b01}[id], Formats: []uint32{format}}, nil
}

func (f *fakeDevice) Properties(object, kind uint32) (map[string]kdrm.Property, error) {
	switch kind {
	case kdrm.ObjectPlane:
		typ, formats := map[uint32]uint64{42: 0, 43: 2}[object], uint64(0)
		if object == 40 || object == 41 {
			typ, formats = 1, inFormatsBlob
		}
		props := map[string]kdrm.Property{
			"type": {ID: propType, Value: typ}, "FB_ID": {ID: propFB}, "CRTC_ID": {ID: propPlaneCRTC},
			"SRC_X": {ID: propSrcX}, "SRC_Y": {ID: propSrcY}, "SRC_W": {ID: propSrcW}, "SRC_H": {ID: propSrcH},
			"CRTC_X": {ID: propCRTCX}, "CRTC_Y": {ID: propCRTCY}, "CRTC_W": {ID: propCRTCW}, "CRTC_H": {ID: propCRTCH},
			"IN_FORMATS": {ID: propInFormats, Value: formats}, "IN_FENCE_FD": {ID: propInFence},
		}
		if object == 43 {
			props["HOTSPOT_X"], props["HOTSPOT_Y"] = kdrm.Property{ID: propHotspotX}, kdrm.Property{ID: propHotspotY}
		}
		return props, nil
	case kdrm.ObjectCRTC:
		return map[string]kdrm.Property{"ACTIVE": {ID: propActive}, "MODE_ID": {ID: propModeID}, "GAMMA_LUT": {ID: propGammaLUT}, "GAMMA_LUT_SIZE": {ID: propGammaSize, Value: 4}}, nil
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

func (f *fakeDevice) CreateBlob(data []byte) (uint32, error) {
	f.blobs++
	if f.blobData == nil {
		f.blobData = map[uint32][]byte{}
	}
	f.blobData[600+f.blobs] = slices.Clone(data)
	return 600 + f.blobs, nil
}

func (f *fakeDevice) DestroyBlob(id uint32) error {
	f.destroyed = append(f.destroyed, id)
	return nil
}

func (f *fakeDevice) CreateDumb(size image.Point) (kdrm.Dumb, error) {
	f.dumbs++
	if f.live == nil {
		f.live = map[uint32]bool{}
	}
	f.live[f.dumbs] = true
	pix := make([]byte, size.X*size.Y*4)
	if f.pix == nil {
		f.pix = map[uint32][]byte{}
	}
	f.pix[f.dumbs] = pix
	return kdrm.Dumb{Handle: f.dumbs, Pitch: uint32(size.X * 4), Pix: pix}, nil
}

func (f *fakeDevice) DestroyDumb(b kdrm.Dumb) error {
	if !f.live[b.Handle] {
		return fmt.Errorf("dumb buffer %d destroyed twice", b.Handle)
	}
	delete(f.live, b.Handle)
	return nil
}

func (f *fakeDevice) AddFB2(fb kdrm.FB) (uint32, error) {
	prime := fb.Handles[0] >= primeHandles && fb.Format == scanoutFormat
	if !prime && ((fb.Format != cursorFormat && fb.Format != scanoutFormat) || fb.WithModifier || !f.live[fb.Handles[0]]) {
		return 0, fmt.Errorf("unexpected framebuffer %+v", fb)
	}
	f.fbs++
	if f.scanouts == nil {
		f.scanouts = map[uint32]uint32{}
	}
	f.scanouts[700+f.fbs] = fb.Handles[0]
	return 700 + f.fbs, nil
}

func (f *fakeDevice) RemoveFB(uint32) error {
	f.removedFBs++
	return nil
}

func (f *fakeDevice) Commit(r *kdrm.Request, flags uint32, userData uint64) error {
	c := commit{flags: flags, userData: userData, values: map[[2]uint32]uint64{}}
	r.Each(func(object, property uint32, value uint64) { c.values[[2]uint32{object, property}] = value })
	f.commits = append(f.commits, c)
	if f.refuseCursor && c.values[[2]uint32{43, propFB}] != 0 || f.refuseGamma && c.values[[2]uint32{10, propGammaLUT}] != 0 || f.refuseAsync && flags&kdrm.AtomicPageFlipAsync != 0 {
		return unix.EINVAL
	}
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
	next   uint32
	freed  int
	soft   *soft.Renderer
	fences []int
}

func (a *fakeAlloc) renderer() render.Renderer { return a.soft }

func (a *fakeAlloc) renderable(uint32) ([]uint64, error) { return []uint64{0, 7, 9}, nil }

func (a *fakeAlloc) allocate(size image.Point, _ uint32, modifiers []uint64) (*scanout, error) {
	if !slices.Equal(modifiers, []uint64{0, 7}) {
		return nil, fmt.Errorf("allocated with modifiers %v", modifiers)
	}
	a.next++
	return &scanout{target: soft.NewBuffer(size.X, size.Y), fb: 900 + a.next, free: func() error { a.freed++; return nil }, drop: func() error { return nil }}, nil
}

func (a *fakeAlloc) fence() (int, error) {
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		return -1, err
	}
	a.fences = append(a.fences, fd)
	return fd, nil
}
func (a *fakeAlloc) close() error { return nil }

type recorder struct {
	during   func(o backend.Output) error
	idle     bool
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

func (r *recorder) OutputDamaged(o backend.Output) error {
	r.log = append(r.log, "damaged "+o.Name())
	return o.ScheduleFrame()
}

func (r *recorder) Frame(o backend.Output) error {
	r.log = append(r.log, "frame")
	if r.during != nil {
		if err := r.during(o); err != nil {
			return err
		}
	}
	if r.idle {
		return nil
	}
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
	t     *testing.T
	l     *loop.Loop
	dev   *fakeDevice
	alloc *fakeAlloc
	b     *Backend
	rec   *recorder
}

func start(t *testing.T) *rig {
	t.Helper()
	return boot(t, func(*fakeDevice) allocator { return &fakeAlloc{soft: soft.New()} })
}

func boot(t *testing.T, alloc func(*fakeDevice) allocator) *rig {
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
	a := alloc(r.dev)
	r.alloc, _ = a.(*fakeAlloc)
	if r.b, err = newBackend(l, r.dev, a, nil); err != nil {
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
	fences := r.alloc.fences
	if len(fences) != 1 || r.dev.commits[0].values[[2]uint32{40, propInFence}] != uint64(fences[0]) || r.dev.commits[1].values[[2]uint32{40, propInFence}] != uint64(fences[0]) {
		t.Fatalf("fences %v, committed %+v", fences, r.dev.commits[1].values)
	}
	if _, err := unix.FcntlInt(uintptr(fences[0]), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("the fence fd stayed open after the commit: %v", err)
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

func TestFramesThatPresentNothingWaitForTheNextRefresh(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	limit := int(20*time.Millisecond/o.State().Mode.Interval()) + 2
	frames := 0
	r.rec.idle = true
	r.rec.during = func(o backend.Output) error {
		if frames++; frames > limit {
			return nil
		}
		return o.ScheduleFrame()
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if frames > limit {
		t.Fatalf("more than %d frames that presented nothing in 20 ms", limit)
	}
	if len(r.dev.commits) != 0 {
		t.Fatalf("%d commits for frames that drew nothing", len(r.dev.commits))
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

func TestPowerAndWake(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	state := o.State()
	state.Off = true
	if err := o.Commit(state); err != nil {
		t.Fatal(err)
	}
	off := r.dev.commits[len(r.dev.commits)-1]
	if off.flags != kdrm.AtomicAllowModeset || off.values[[2]uint32{10, propActive}] != 0 || !o.State().Off || len(o.buffers) == 0 {
		t.Fatalf("power off commit %+v, state %+v", off, o.State())
	}
	before := len(r.dev.commits)
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.dev.commits) != before {
		t.Fatal("an output that is off drew a frame")
	}
	state.Off = false
	if err := o.Commit(state); err != nil {
		t.Fatal(err)
	}
	r.settle()
	on := r.dev.commits[len(r.dev.commits)-1]
	if on.flags&kdrm.AtomicAllowModeset == 0 || on.values[[2]uint32{10, propActive}] != 1 || o.State().Off {
		t.Fatalf("power on commit %+v", on)
	}
	r.dev.flip(t, 10, 5)
	r.settle()
	if err := r.b.Wake(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	woke := r.dev.commits[len(r.dev.commits)-1]
	if woke.flags&kdrm.AtomicAllowModeset == 0 || woke.values[[2]uint32{10, propModeID}] == 0 {
		t.Fatalf("waking did not recommit the mode: %+v", woke)
	}
}

func (r *rig) lit() *Output {
	r.t.Helper()
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		r.t.Fatal(err)
	}
	r.settle()
	r.dev.flip(r.t, 10, 1)
	r.settle()
	r.dev.commits = nil
	return o
}

func (r *rig) setCursor(o *Output, hotspot image.Point) error {
	r.t.Helper()
	cp, err := o.CursorPlane()
	if err != nil {
		r.t.Fatal(err)
	}
	f, err := cp.BeginCursor()
	if err != nil {
		return err
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rectangle{Max: cp.CursorSize()}})
	if err != nil {
		r.t.Fatal(err)
	}
	p.Clear(render.Color{})
	p.FillRect(image.Rect(0, 0, 1, 1), render.Color{R: 0xff, A: 0xff})
	if err := p.End(); err != nil {
		r.t.Fatal(err)
	}
	return cp.SetCursor(hotspot)
}

func cursorAt(c commit) (uint64, image.Point, bool) {
	fb, ok := c.values[[2]uint32{43, propFB}]
	return fb, image.Pt(int(int64(c.values[[2]uint32{43, propCRTCX}])), int(int64(c.values[[2]uint32{43, propCRTCY}]))), ok
}

func TestCursorPlaneMovesWithoutFrames(t *testing.T) {
	r := start(t)
	o := r.lit()
	if err := r.setCursor(o, image.Pt(4, 6)); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 3 || r.dev.commits[0].flags != kdrm.AtomicTestOnly || r.dev.commits[2].flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent {
		t.Fatalf("setting the cursor committed %+v", r.dev.commits)
	}
	shown := r.dev.commits[2]
	if _, ok := shown.values[[2]uint32{40, propFB}]; ok {
		t.Fatal("a cursor update touched the primary plane")
	}
	if fb, at, _ := cursorAt(shown); fb == 0 || at != image.Pt(-4, -6) || shown.values[[2]uint32{43, propCRTCW}] != 64 || shown.values[[2]uint32{43, propPlaneCRTC}] != 10 {
		t.Fatalf("cursor fb %d at %v in %+v", fb, at, shown.values)
	}
	if shown.values[[2]uint32{43, propHotspotX}] != 4 || shown.values[[2]uint32{43, propHotspotY}] != 6 {
		t.Fatalf("the cursor plane got hotspot %d,%d, not 4,6", shown.values[[2]uint32{43, propHotspotX}], shown.values[[2]uint32{43, propHotspotY}])
	}
	cp, _ := o.CursorPlane()
	for _, at := range []image.Point{image.Pt(100, 50), image.Pt(120, 70)} {
		if err := cp.MoveCursor(at); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.dev.commits) != 3 {
		t.Fatal("the cursor moved while its last update was still flipping")
	}
	r.dev.flip(t, 10, 2)
	r.settle()
	if len(r.dev.commits) != 5 || len(r.rec.presents) != 1 {
		t.Fatalf("%d commits and %d presents after the flip", len(r.dev.commits), len(r.rec.presents))
	}
	if _, at, _ := cursorAt(r.dev.commits[4]); at != image.Pt(116, 64) {
		t.Fatalf("the cursor landed at %v", at)
	}
	r.dev.flip(t, 10, 3)
	r.settle()
	if err := cp.HideCursor(); err != nil {
		t.Fatal(err)
	}
	if fb, _, ok := cursorAt(r.dev.commits[len(r.dev.commits)-1]); !ok || fb != 0 {
		t.Fatal("hiding the cursor did not clear its plane")
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.dev.flip(t, 10, 4)
	r.settle()
	if fb, _, ok := cursorAt(r.dev.commits[len(r.dev.commits)-1]); !ok || fb != 0 {
		t.Fatal("a frame brought a hidden cursor back")
	}
}

func TestFramesCarryTheCursor(t *testing.T) {
	r := start(t)
	o := r.lit()
	fences := len(r.alloc.fences)
	if err := r.setCursor(o, image.Point{}); err != nil {
		t.Fatal(err)
	}
	cp, _ := o.CursorPlane()
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	if err := cp.MoveCursor(image.Pt(30, 40)); err != nil {
		t.Fatal(err)
	}
	r.dev.flip(t, 10, 2)
	r.settle()
	last := r.dev.commits[len(r.dev.commits)-1]
	if _, at, _ := cursorAt(last); at != image.Pt(30, 40) || last.values[[2]uint32{40, propFB}] == 0 {
		t.Fatalf("the frame after the move: %+v", last.values)
	}
	if n := len(r.alloc.fences) - fences; n != 1 {
		t.Fatalf("%d fences for one frame", n)
	}
	if pix := o.cursor.pending.dumb.Pix; [4]byte(pix[:4]) != [4]byte{0, 0, 0xff, 0xff} || pix[4] != 0 {
		t.Fatalf("the cursor buffer starts %v", pix[:8])
	}
}

func TestCursorChangesDuringAFrameJoinIt(t *testing.T) {
	r := start(t)
	o := r.lit()
	if err := r.setCursor(o, image.Point{}); err != nil {
		t.Fatal(err)
	}
	r.dev.flip(t, 10, 2)
	r.settle()
	r.dev.commits = nil
	cp, _ := o.CursorPlane()
	r.rec.during = func(backend.Output) error { return cp.MoveCursor(image.Pt(9, 9)) }
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if len(r.dev.commits) != 2 || r.dev.commits[1].values[[2]uint32{40, propFB}] == 0 {
		t.Fatalf("a move inside a frame committed %+v", r.dev.commits)
	}
	if _, at, _ := cursorAt(r.dev.commits[1]); at != image.Pt(9, 9) {
		t.Fatalf("the frame put the cursor at %v", at)
	}
}

func TestARefusedCursorFallsBack(t *testing.T) {
	r := start(t)
	o := r.lit()
	r.dev.refuseCursor = true
	if err := r.setCursor(o, image.Point{}); err == nil {
		t.Fatal("a refused cursor image was accepted")
	}
	cp, _ := o.CursorPlane()
	if err := cp.MoveCursor(image.Pt(1, 1)); err == nil {
		t.Fatal("a refused cursor plane moved")
	}
	if _, err := cp.BeginCursor(); err == nil {
		t.Fatal("a refused cursor plane took another image")
	}
}

func TestAFrameDropsACursorItsTestRefuses(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := r.setCursor(o, image.Point{}); err != nil {
		t.Fatalf("an untested cursor before the first frame: %v", err)
	}
	r.dev.refuseCursor = true
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	last := r.dev.commits[len(r.dev.commits)-1]
	if fb, _, _ := cursorAt(last); fb != 0 || last.values[[2]uint32{40, propFB}] == 0 || last.flags&kdrm.AtomicTestOnly != 0 {
		t.Fatalf("the first frame: %+v", last)
	}
	cp, _ := o.CursorPlane()
	if err := cp.MoveCursor(image.Pt(1, 1)); err == nil {
		t.Fatal("the cursor plane stayed in use after the frame dropped it")
	}
}

func TestCursorPlanesBelongToOneCRTC(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if o.cursor == nil || o.cursor.plane.id != 43 || r.b.cursor != image.Pt(64, 64) {
		t.Fatalf("cursor %+v, size %v", o.cursor, r.b.cursor)
	}
	if err := r.setCursor(o, image.Point{}); err != nil || len(r.dev.live) != cursorBuffers {
		t.Fatalf("%d dumb buffers for the cursor: %v", len(r.dev.live), err)
	}
	r.dev.connected = false
	if err := r.b.Hotplug(); err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(r.b.planes, func(p *plane) bool { return p.id == 43 }); r.b.planes[i].taken {
		t.Fatal("the cursor plane stayed taken after its output left")
	}
	if len(r.dev.live) != 0 {
		t.Fatalf("dumb buffers %v outlived their output", r.dev.live)
	}
}

func TestFramebuffersCarryModifiersOnlyWhereTheDeviceTakesThem(t *testing.T) {
	const tiled = 0x0100000000000002
	size := image.Pt(64, 32)
	cases := []struct {
		name          string
		modifier      uint64
		withModifiers bool
		want          kdrm.FB
		fails         bool
	}{
		{"explicit modifier on a modifier device", tiled, true, kdrm.FB{Width: 64, Height: 32, Format: scanoutFormat, Modifier: tiled, WithModifier: true}, false},
		{"implicit layout on a modifier device", gpu.ModifierInvalid, true, kdrm.FB{Width: 64, Height: 32, Format: scanoutFormat}, false},
		{"linear on a device without modifiers", gpu.ModifierLinear, false, kdrm.FB{Width: 64, Height: 32, Format: scanoutFormat}, false},
		{"implicit layout on a device without modifiers", gpu.ModifierInvalid, false, kdrm.FB{Width: 64, Height: 32, Format: scanoutFormat}, false},
		{"tiled on a device without modifiers", tiled, false, kdrm.FB{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fb, err := framebuffer(size, scanoutFormat, tc.modifier, tc.withModifiers)
			if (err != nil) != tc.fails || fb != tc.want {
				t.Fatalf("framebuffer = %+v, %v", fb, err)
			}
		})
	}
}

func TestSecondaryDevicesShowFramesThePrimaryRendered(t *testing.T) {
	primary := soft.New()
	r := boot(t, func(dev *fakeDevice) allocator { return &copyAllocator{dev: dev, primary: primary} })
	o := r.rec.outputs[0]
	f, err := o.BeginFrame()
	if err != nil {
		t.Fatal(err)
	}
	if f.Renderer != render.Renderer(primary) {
		t.Fatalf("a secondary device's frame renders with %T, not the primary renderer", f.Renderer)
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rect(0, 0, 4, 4)})
	if err != nil {
		t.Fatal(err)
	}
	p.FillRect(image.Rect(0, 0, 4, 4), render.Color{R: 0xff, A: 0xff})
	if err := errors.Join(p.End(), o.Present(nil)); err != nil {
		t.Fatal(err)
	}
	fb := r.dev.commits[len(r.dev.commits)-1].values[[2]uint32{40, propFB}]
	pix := r.dev.pix[r.dev.scanouts[uint32(fb)]]
	if pix == nil || pix[2] != 0xff || pix[1] != 0 || pix[0] != 0 {
		t.Fatalf("the scanout buffer behind framebuffer %d starts with %v, not the red the primary drew", fb, pix[:4])
	}
}

func TestUnpluggedDevicesDropTheirOutputsWithoutTouchingTheDevice(t *testing.T) {
	r := boot(t, func(dev *fakeDevice) allocator { return &copyAllocator{dev: dev, primary: soft.New()} })
	r.settle()
	commits, dumbs, fbs := len(r.dev.commits), len(r.dev.live), r.dev.removedFBs
	if err := r.b.Unplug(); err != nil {
		t.Fatal(err)
	}
	if r.rec.log[len(r.rec.log)-1] != "removed "+r.rec.outputs[0].Name() {
		t.Fatalf("log %v: unplugging did not remove the output", r.rec.log)
	}
	if len(r.dev.commits) != commits || len(r.dev.live) != dumbs || r.dev.removedFBs != fbs {
		t.Fatalf("unplugging touched the gone device: %d commits, %d dumb buffers, %d framebuffers removed", len(r.dev.commits)-commits, dumbs-len(r.dev.live), r.dev.removedFBs-fbs)
	}
	if err := r.b.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOutputsOfferThePrimaryPlaneFormatsForScanout(t *testing.T) {
	r := start(t)
	got := r.lit().ScanoutFormats()
	want := gpu.ScanoutTarget{Device: fakeNumber, Formats: []gpu.FormatModifier{{Format: scanoutFormat, Modifier: 0}, {Format: scanoutFormat, Modifier: 7}}}
	if got.Device != want.Device || !slices.Equal(got.Formats, want.Formats) {
		t.Fatalf("scanout formats %+v, want %+v", got, want)
	}
}

func TestClientBuffersScanOutUntilTheNextFlip(t *testing.T) {
	r := start(t)
	o := r.lit()
	released := 0
	client := gpu.DMABUF{Size: image.Pt(1920, 1080), Format: scanoutFormat, Modifier: gpu.ModifierLinear, Planes: []gpu.Plane{{FD: 7, Stride: 1920 * 4}}}
	ok, err := o.Scanout(client, func() error { released++; return nil }, false)
	if err != nil || !ok {
		t.Fatalf("Scanout = %t, %v", ok, err)
	}
	shown := r.dev.commits[len(r.dev.commits)-1]
	fb := shown.values[[2]uint32{40, propFB}]
	if shown.flags&kdrm.AtomicTestOnly != 0 || r.dev.scanouts[uint32(fb)] != primeHandles+7 {
		t.Fatalf("the client buffer went out as framebuffer %d with flags %#x", fb, shown.flags)
	}
	if again, err := o.Scanout(client, func() error { return nil }, false); again || err != nil {
		t.Fatalf("a second scanout during the flip = %t, %v", again, err)
	}
	r.dev.flip(t, 10, 2)
	r.settle()
	if released != 0 {
		t.Fatal("the client buffer was released while on screen")
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	r.dev.flip(t, 10, 3)
	r.settle()
	if released != 1 {
		t.Fatalf("the client buffer was released %d times after a frame replaced it", released)
	}
	if ok, err := o.Scanout(gpu.DMABUF{Size: image.Pt(640, 480), Format: scanoutFormat}, func() error { return nil }, false); ok || err != nil {
		t.Fatalf("a buffer that does not fill the mode scanned out: %t, %v", ok, err)
	}
}

func gammaOf(c commit) (uint64, bool) {
	v, ok := c.values[[2]uint32{10, propGammaLUT}]
	return v, ok
}

func TestGammaLUTsReachTheCRTC(t *testing.T) {
	r := start(t)
	o := r.b.Outputs()[0]
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if lut, ok := gammaOf(r.dev.commits[1]); !ok || lut != 0 {
		t.Fatal("the first modeset kept whatever LUT the CRTC had")
	}
	r.dev.flip(t, 10, 1)
	r.settle()
	r.dev.commits = nil
	if n := o.GammaSize(); n != 4 {
		t.Fatalf("gamma size %d, want the CRTC's GAMMA_LUT_SIZE 4", n)
	}

	if err := o.SetGamma([]uint16{0, 1, 2, 3, 10, 11, 12, 13, 20, 21, 22, 23}); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 2 || r.dev.commits[0].flags != kdrm.AtomicTestOnly || r.dev.commits[1].flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent {
		t.Fatalf("a LUT on an idle output committed %+v, want a test and a commit of its own", r.dev.commits)
	}
	first, ok := gammaOf(r.dev.commits[1])
	if _, primary := r.dev.commits[1].values[[2]uint32{40, propFB}]; !ok || first == 0 || primary {
		t.Fatalf("the gamma commit set GAMMA_LUT %d (%v) and touched the primary plane: %v", first, ok, primary)
	}
	want := []uint16{0, 10, 20, 0, 1, 11, 21, 0, 2, 12, 22, 0, 3, 13, 23, 0}
	got := make([]uint16, len(r.dev.blobData[uint32(first)])/2)
	for i := range got {
		got[i] = binary.NativeEndian.Uint16(r.dev.blobData[uint32(first)][2*i:])
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the LUT blob holds %v, want drm_color_lut entries %v", got, want)
	}

	if err := o.SetGamma([]uint16{3, 2, 1, 0, 3, 2, 1, 0, 3, 2, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if len(r.dev.commits) != 3 {
		t.Fatal("a LUT was committed while the previous commit was flipping")
	}
	if err := o.ScheduleFrame(); err != nil {
		t.Fatal(err)
	}
	r.dev.flip(t, 10, 2)
	r.settle()
	frame := r.dev.commits[len(r.dev.commits)-1]
	second, ok := gammaOf(frame)
	if _, primary := frame.values[[2]uint32{40, propFB}]; !primary || !ok || second == 0 || second == first {
		t.Fatalf("the next frame carried GAMMA_LUT %d (%v), want the new LUT with the frame", second, ok)
	}
	if !slices.Contains(r.dev.destroyed, uint32(first)) {
		t.Fatal("the replaced LUT blob was not destroyed")
	}

	r.dev.flip(t, 10, 3)
	r.settle()
	if err := o.SetGamma(nil); err != nil {
		t.Fatal(err)
	}
	if lut, ok := gammaOf(r.dev.commits[len(r.dev.commits)-1]); !ok || lut != 0 {
		t.Fatalf("resetting committed GAMMA_LUT %d (%v), want the identity 0", lut, ok)
	}

	r.dev.flip(t, 10, 4)
	r.settle()
	r.dev.refuseGamma = true
	before := len(r.dev.commits)
	if err := o.SetGamma([]uint16{0, 1, 2, 3, 0, 1, 2, 3, 0, 1, 2, 3}); err == nil {
		t.Fatal("a LUT the driver refuses was accepted")
	}
	if len(r.dev.commits) != before+1 || !slices.Contains(r.dev.destroyed, 600+r.dev.blobs) {
		t.Fatal("a refused LUT was committed or its blob kept")
	}
	if err := o.SetGamma([]uint16{1, 2, 3}); err == nil {
		t.Fatal("ramps of the wrong size were accepted")
	}
}

func TestTearingScanoutsFlipAsync(t *testing.T) {
	r := boot(t, func(d *fakeDevice) allocator {
		d.asyncFlips = true
		return &fakeAlloc{soft: soft.New()}
	})
	o := r.lit()
	client := func(fd int) gpu.DMABUF {
		return gpu.DMABUF{Size: image.Pt(1920, 1080), Format: scanoutFormat, Modifier: gpu.ModifierLinear, Planes: []gpu.Plane{{FD: fd, Stride: 1920 * 4}}}
	}
	scanout := func(fd int, tear bool) commit {
		t.Helper()
		if ok, err := o.Scanout(client(fd), func() error { return nil }, tear); err != nil || !ok {
			t.Fatalf("Scanout = %t, %v", ok, err)
		}
		return r.dev.commits[len(r.dev.commits)-1]
	}
	torn := scanout(7, true)
	if torn.flags != kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent|kdrm.AtomicPageFlipAsync {
		t.Fatalf("a tearing scanout committed with flags %#x", torn.flags)
	}
	if len(torn.values) != 1 || r.dev.scanouts[uint32(torn.values[[2]uint32{40, propFB}])] != primeHandles+7 {
		t.Fatalf("an async flip carried %v, want the primary plane's FB_ID alone", torn.values)
	}
	r.dev.flip(t, 10, 2)
	r.settle()
	if p := r.rec.presents[len(r.rec.presents)-1]; !p.Torn {
		t.Fatalf("the async flip was presented as %+v, not torn", p)
	}

	if vsync := scanout(8, false); vsync.flags&kdrm.AtomicPageFlipAsync != 0 {
		t.Fatal("a scanout that does not tear flipped async")
	}
	r.dev.flip(t, 10, 3)
	r.settle()
	if p := r.rec.presents[len(r.rec.presents)-1]; p.Torn {
		t.Fatal("a vblank flip was presented as torn")
	}

	r.dev.refuseAsync = true
	if refused := scanout(9, true); refused.flags&kdrm.AtomicPageFlipAsync != 0 || refused.values[[2]uint32{40, propSrcW}] == 0 {
		t.Fatalf("an async flip the driver refuses did not fall back to a full vblank flip: %+v", refused)
	}
}
