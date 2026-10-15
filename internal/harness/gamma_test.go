package harness

import (
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cgc "github.com/stubbedev/tilde/internal/protocol/client/wlrgammacontrol"
	"github.com/stubbedev/tilde/internal/testclient"
)

type gammaEvents struct {
	size   uint32
	failed bool
}

func (g *gammaEvents) GammaSize(_ *cgc.ZwlrGammaControlV1, size uint32) error {
	g.size = size
	return nil
}

func (g *gammaEvents) Failed(*cgc.ZwlrGammaControlV1) error {
	g.failed = true
	return nil
}

func gammaControl(t *testing.T, h *Harness) (*testclient.Client, *cgc.ZwlrGammaControlV1, *gammaEvents) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	m, err := cgc.NewZwlrGammaControlManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("zwlr_gamma_control_manager_v1", m)
	ev := &gammaEvents{}
	g, err := m.GetGammaControl(ev, c.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the gamma size or a failure", func() bool { return ev.size != 0 || ev.failed })
	return c, g, ev
}

func gammaTable(t *testing.T, ramps []uint16, extra int) int {
	t.Helper()
	fd, err := unix.MemfdCreate("gamma", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 0, 2*len(ramps)+extra)
	for _, v := range ramps {
		b = binary.NativeEndian.AppendUint16(b, v)
	}
	b = append(b, make([]byte, extra)...)
	if _, err := unix.Pwrite(fd, b, 0); err != nil {
		t.Fatal(errors.Join(err, unix.Close(fd)))
	}
	return fd
}

func outputGamma(want []uint16) func(*headless.Backend, *compositor.Compositor) bool {
	return func(b *headless.Backend, _ *compositor.Compositor) bool {
		return slices.Equal(b.Outputs()[0].Gamma(), want)
	}
}

func TestGammaControlSetsAnOutputsRamps(t *testing.T) {
	h := Start(t, small)
	if _, _, ev := gammaControl(t, h); !ev.failed {
		t.Fatal("an output without a gamma LUT handed out a gamma control")
	}
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.Outputs()[0].SetGammaSize(4)
		return nil
	})
	c, g, ev := gammaControl(t, h)
	if ev.failed || ev.size != 4 {
		t.Fatalf("gamma size %d, failed %v", ev.size, ev.failed)
	}
	if _, _, other := gammaControl(t, h); !other.failed {
		t.Fatal("a second client took gamma control of an output that already has one")
	}
	table := []uint16{0, 1, 2, 3, 10, 11, 12, 13, 20, 21, 22, 23}
	fd := gammaTable(t, table, 0)
	if err := errors.Join(g.SetGamma(fd), unix.Close(fd), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the ramps on the output", outputGamma(table))

	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	piped := []uint16{3, 2, 1, 0, 3, 2, 1, 0, 3, 2, 1, 0}
	b := make([]byte, 0, 2*len(piped))
	for _, v := range piped {
		b = binary.NativeEndian.AppendUint16(b, v)
	}
	if _, err := unix.Write(p[1], b); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(unix.Close(p[1]), g.SetGamma(p[0]), unix.Close(p[0]), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the piped ramps on the output", outputGamma(piped))

	if err := errors.Join(g.Destroy(), c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	h.Eventually("the identity once the control is gone", outputGamma(nil))
	if _, _, next := gammaControl(t, h); next.failed {
		t.Fatal("the output stayed taken after its gamma control was destroyed")
	}
}

func TestAGammaTableIsReadFromTheStartOfItsFile(t *testing.T) {
	h := Start(t, small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.Outputs()[0].SetGammaSize(4)
		return nil
	})
	c, g, _ := gammaControl(t, h)
	table := []uint16{5, 6, 7, 8, 15, 16, 17, 18, 25, 26, 27, 28}
	fd, err := unix.MemfdCreate("gamma", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	b := make([]byte, 0, 2*len(table))
	for _, v := range table {
		b = binary.NativeEndian.AppendUint16(b, v)
	}
	if _, err := unix.Write(fd, b); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := errors.Join(g.SetGamma(fd), c.Display.Roundtrip()); err != nil {
			t.Fatalf("a table written with write(2), offset left at its end: %v", err)
		}
	}
	h.Eventually("the ramps on the output", outputGamma(table))
}

func TestGammaTablesOfTheWrongSizeAreProtocolErrors(t *testing.T) {
	h := Start(t, small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		b.Outputs()[0].SetGammaSize(4)
		return nil
	})
	ramps := make([]uint16, 12)
	for name, table := range map[string]func() int{
		"short": func() int { return gammaTable(t, ramps[:11], 0) },
		"long":  func() int { return gammaTable(t, ramps, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			c, g, _ := gammaControl(t, h)
			fd := table()
			if err := errors.Join(g.SetGamma(fd), unix.Close(fd)); err != nil {
				t.Fatal(err)
			}
			c.ExpectProtocolError(uint32(cgc.ZwlrGammaControlV1ErrorInvalidGamma))
		})
	}
}
