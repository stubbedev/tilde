package nested

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/harness"
	"github.com/stubbedev/tilde/internal/loop"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

var _ backend.Backend = (*Backend)(nil)

type sway struct {
	dir    string
	socket string
	env    []string
}

func startSway(t *testing.T) *sway {
	t.Helper()
	bin := harness.Require(t, "sway")
	harness.Require(t, "swaymsg")
	harness.Require(t, "grim")
	dir, err := os.MkdirTemp("", "tilde-host-")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte("output * mode 640x480\ndefault_border none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &sway{dir: dir, env: append(os.Environ(),
		"XDG_RUNTIME_DIR="+dir,
		"WLR_BACKENDS=headless",
		"WLR_LIBINPUT_NO_DEVICES=1",
		"WLR_RENDERER=pixman",
		"DBUS_SESSION_BUS_ADDRESS=disabled:",
	)}
	var log bytes.Buffer
	cmd := exec.CommandContext(context.Background(), bin, "-c", cfg)
	cmd.Env, cmd.Stdout, cmd.Stderr = s.env, &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.RemoveAll(dir)
		if t.Failed() {
			t.Logf("sway log:\n%s", log.String())
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		sockets, _ := filepath.Glob(filepath.Join(dir, "wayland-[0-9]*"))
		ipc, _ := filepath.Glob(filepath.Join(dir, "sway-ipc.*.sock"))
		for _, p := range sockets {
			if !strings.HasSuffix(p, ".lock") && len(ipc) > 0 {
				s.socket = p
				s.env = append(s.env, "WAYLAND_DISPLAY="+p, "SWAYSOCK="+ipc[0])
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("sway did not start:\n%s", log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *sway) run(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.Env = s.env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return out
}

func (s *sway) pixel(t *testing.T, x, y int) [3]byte {
	t.Helper()
	ppm := s.run(t, "grim", "-t", "ppm", "-")
	var w, h, maxval int
	var header int
	if _, err := fmt.Sscanf(string(ppm), "P6\n%d %d\n%d\n", &w, &h, &maxval); err != nil {
		t.Fatalf("grim output is not a PPM: %v", err)
	}
	for lines := 0; lines < 3; header++ {
		if ppm[header] == '\n' {
			lines++
		}
	}
	i := header + (y*w+x)*3
	return [3]byte(ppm[i : i+3])
}

func (s *sway) eventuallyShows(t *testing.T, x, y int) {
	t.Helper()
	want := [3]byte{compositor.Background.R, compositor.Background.G, compositor.Background.B}
	deadline := time.Now().Add(10 * time.Second)
	for got := s.pixel(t, x, y); got != want; got = s.pixel(t, x, y) {
		if time.Now().After(deadline) {
			t.Fatalf("host pixel (%d, %d) is %v, want tilde's background %v", x, y, got, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestNestedInSway(t *testing.T) {
	host := startSway(t)
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	b, err := New(l, host.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	runtime, err := os.MkdirTemp("", "tilde-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtime)
	display, err := server.New(l, server.Options{RuntimeDir: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer display.Close()
	if _, err := compositor.New(display, b); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	on := func(f func()) {
		ran := make(chan struct{})
		if err := l.Post(func() error {
			f()
			close(ran)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ran:
		case err := <-done:
			t.Fatalf("loop ended: %v", err)
		}
	}

	host.eventuallyShows(t, 320, 240)

	host.run(t, "swaymsg", "output", "HEADLESS-1", "mode", "800x600")
	deadline := time.Now().Add(10 * time.Second)
	for {
		var size image.Point
		on(func() { size = b.Outputs()[0].State().Mode.Size })
		if size == image.Pt(800, 600) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nested output is %v after the host output grew to 800x600", size)
		}
		time.Sleep(20 * time.Millisecond)
	}
	host.eventuallyShows(t, 790, 590)

	host.run(t, "swaymsg", `[app_id="tilde"]`, "kill")
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Run = %v, want ErrClosed after the host closed the window", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tilde kept running after its last host window closed")
	}
}

type recorder struct {
	events []backend.Event
}

func (r *recorder) OutputAdded(backend.Output) error                     { return nil }
func (r *recorder) OutputRemoved(backend.Output) error                   { return nil }
func (r *recorder) OutputChanged(backend.Output) error                   { return nil }
func (r *recorder) Frame(backend.Output) error                           { return nil }
func (r *recorder) Presented(backend.Output, backend.Presentation) error { return nil }
func (r *recorder) DeviceAdded(backend.Device) error                     { return nil }
func (r *recorder) DeviceRemoved(backend.Device) error                   { return nil }
func (r *recorder) Input(_ backend.Device, e backend.Event) error {
	r.events = append(r.events, e)
	return nil
}

func inputRig() (*Backend, *Output, *recorder) {
	r := &recorder{}
	b := &Backend{handler: r}
	o := &Output{backend: b, surface: &cw.WlSurface{}, size: image.Pt(200, 100)}
	b.outputs = []*Output{o}
	return b, o, r
}

func fixed(v float64) wire.Fixed { return wire.FixedFromFloat(v) }

func TestPointerEventsAreNormalisedToTheOutput(t *testing.T) {
	b, o, r := inputRig()
	p := pointerEvents{&Pointer{backend: b, value120: map[cw.WlPointerAxis]int32{}}}
	for _, err := range []error{
		p.Motion(nil, 0, fixed(10), fixed(10)),
		p.Enter(nil, 0, o.surface, fixed(50), fixed(25)),
		p.Button(nil, 0, 0, 272, cw.WlPointerButtonStatePressed),
		p.AxisSource(nil, cw.WlPointerAxisSourceFinger),
		p.AxisValue120(nil, cw.WlPointerAxisVerticalScroll, 120),
		p.Axis(nil, 0, cw.WlPointerAxisVerticalScroll, fixed(15)),
		p.Frame(nil),
		p.Axis(nil, 0, cw.WlPointerAxisHorizontalScroll, fixed(-3)),
		p.Leave(nil, 0, o.surface),
		p.Motion(nil, 0, fixed(10), fixed(10)),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(r.events) != 5 {
		t.Fatalf("events = %#v, want motion, button, axis, frame, axis", r.events)
	}
	if m := r.events[0].(backend.PointerMotionAbsolute); m.Output != o || m.X != 0.25 || m.Y != 0.25 {
		t.Errorf("enter = %+v, want (0.25, 0.25) on the output", m)
	}
	if bt := r.events[1].(backend.PointerButton); bt.Button != 272 || !bt.Pressed {
		t.Errorf("button = %+v", bt)
	}
	if a := r.events[2].(backend.PointerAxis); a.Source != backend.Finger || a.Value120 != 120 || a.Value != 15 || a.Orientation != backend.Vertical {
		t.Errorf("axis = %+v", a)
	}
	if a := r.events[4].(backend.PointerAxis); a.Source != backend.Wheel || a.Value120 != 0 || a.Orientation != backend.Horizontal {
		t.Errorf("axis after the frame = %+v, want the source and value120 reset", a)
	}
}

func TestKeyboardReleasesHeldKeysOnLeave(t *testing.T) {
	b, _, r := inputRig()
	k := keyboardEvents{&Keyboard{backend: b, pressed: map[uint32]bool{}}}
	for _, err := range []error{
		k.Key(nil, 0, 0, 30, cw.WlKeyboardKeyStatePressed),
		k.Key(nil, 0, 0, 31, cw.WlKeyboardKeyStatePressed),
		k.Key(nil, 0, 0, 31, cw.WlKeyboardKeyStateRepeated),
		k.Key(nil, 0, 0, 31, cw.WlKeyboardKeyStateReleased),
		k.Leave(nil, 0, nil),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, e := range r.events {
		key := e.(backend.Key)
		got = append(got, fmt.Sprintf("%d:%t", key.Code, key.Pressed))
	}
	if want := []string{"30:true", "31:true", "31:false", "30:false"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v: repeats dropped and held keys released on leave", got, want)
	}
}

func TestKeymapIsReadFromTheHost(t *testing.T) {
	b, _, _ := inputRig()
	k := keyboardEvents{&Keyboard{backend: b, pressed: map[uint32]bool{}}}
	fd, err := unix.MemfdCreate("keymap", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	text := "xkb_keymap { };\x00"
	if _, err := unix.Write(fd, []byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := k.Keymap(nil, cw.WlKeyboardKeymapFormatXkbV1, fd, uint32(len(text))); err != nil {
		t.Fatal(err)
	}
	if got := string(k.Keyboard.Keymap()); got != "xkb_keymap { };" {
		t.Fatalf("keymap = %q", got)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
		t.Fatal("the keymap fd was left open")
	}
}

func TestTouchTracksPointsPerOutput(t *testing.T) {
	b, o, r := inputRig()
	tc := touchEvents{&Touch{backend: b, outputs: map[int32]*Output{}}}
	for _, err := range []error{
		tc.Down(nil, 0, 0, o.surface, 1, fixed(100), fixed(50)),
		tc.Down(nil, 0, 0, o.surface, 2, fixed(20), fixed(10)),
		tc.Motion(nil, 0, 1, fixed(150), fixed(50)),
		tc.Up(nil, 0, 0, 1),
		tc.Frame(nil),
		tc.Cancel(nil),
		tc.Motion(nil, 0, 2, fixed(1), fixed(1)),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(r.events) != 6 {
		t.Fatalf("events = %#v", r.events)
	}
	if d := r.events[0].(backend.TouchDown); d.Output != o || d.X != 0.5 || d.Y != 0.5 {
		t.Errorf("down = %+v", d)
	}
	if m := r.events[2].(backend.TouchMotion); m.X != 0.75 {
		t.Errorf("motion = %+v", m)
	}
	if c := r.events[5].(backend.TouchCancel); c.ID != 2 {
		t.Errorf("cancel = %+v, want the remaining point 2", c)
	}
}
