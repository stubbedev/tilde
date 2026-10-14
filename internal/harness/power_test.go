package harness

import (
	"image"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cwop "github.com/stubbedev/tilde/internal/protocol/client/wlroutputpower"
	"github.com/stubbedev/tilde/internal/testclient"
)

type powerEvents struct {
	modes  []cwop.ZwlrOutputPowerV1Mode
	failed bool
}

func (p *powerEvents) Mode(_ *cwop.ZwlrOutputPowerV1, m cwop.ZwlrOutputPowerV1Mode) error {
	p.modes = append(p.modes, m)
	return nil
}

func (p *powerEvents) Failed(*cwop.ZwlrOutputPowerV1) error {
	p.failed = true
	return nil
}

func powerFor(t *testing.T, c *testclient.Client) (*cwop.ZwlrOutputPowerV1, *powerEvents) {
	t.Helper()
	m, err := cwop.NewZwlrOutputPowerManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind("zwlr_output_power_manager_v1", m)
	ev := &powerEvents{}
	p, err := m.GetOutputPower(ev, c.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the initial power mode", func() bool { return len(ev.modes) == 1 })
	return p, ev
}

func TestOutputPowerProtocol(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	w := c.Window("power", testclient.Fill(0, 0, 0xff))
	c.DispatchUntil("the window to map", w.Mapped)
	w.Stop()
	p, ev := powerFor(t, c)
	other := testclient.Connect(t, h.Socket())
	_, watcher := powerFor(t, other)
	if ev.modes[0] != cwop.ZwlrOutputPowerV1ModeOn {
		t.Fatalf("initial mode %v", ev.modes)
	}
	if err := p.SetMode(cwop.ZwlrOutputPowerV1ModeOff); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("off", func() bool { return len(ev.modes) == 2 })
	other.DispatchUntil("the other client sees off", func() bool { return len(watcher.modes) == 2 })
	var presents uint64
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[0]
		if !o.State().Off || ev.modes[1] != cwop.ZwlrOutputPowerV1ModeOff {
			t.Errorf("state %+v, modes %v", o.State(), ev.modes)
		}
		presents = o.Presents()
		return o.ScheduleFrame()
	})
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		if b.Outputs()[0].Presents() != presents {
			t.Error("an output that is off presented")
		}
		return nil
	})
	if err := p.SetMode(cwop.ZwlrOutputPowerV1ModeOn); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("on", func() bool { return len(ev.modes) == 3 })
	h.Eventually("a full repaint after power on", func(b *headless.Backend, _ *compositor.Compositor) bool {
		o := b.Outputs()[0]
		d := o.LastDamage()
		return o.Presents() > presents && len(d) == 1 && d[0] == image.Rectangle{Max: small.Size}
	})
	if watcher.failed || ev.failed {
		t.Fatal("a power object failed")
	}
}
