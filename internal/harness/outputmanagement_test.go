package harness

import (
	"errors"
	"image"
	"maps"
	"slices"
	"testing"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/output"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	cwom "github.com/stubbedev/tilde/internal/protocol/client/wlroutputmanagement"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/testenv"
	"github.com/stubbedev/tilde/internal/wire"
)

var (
	wideMode  = backend.Mode{Size: image.Pt(320, 200), RefreshMHz: 60000, Preferred: true}
	smallMode = backend.Mode{Size: image.Pt(160, 100), RefreshMHz: 30000}
)

type omMode struct {
	res      *cwom.ZwlrOutputModeV1
	mode     backend.Mode
	finished bool
}

func (m *omMode) Size(_ *cwom.ZwlrOutputModeV1, w, h int32) error {
	m.mode.Size = image.Pt(int(w), int(h))
	return nil
}

func (m *omMode) Refresh(_ *cwom.ZwlrOutputModeV1, r int32) error {
	m.mode.RefreshMHz = r
	return nil
}

func (m *omMode) Preferred(*cwom.ZwlrOutputModeV1) error {
	m.mode.Preferred = true
	return nil
}

func (m *omMode) Finished(*cwom.ZwlrOutputModeV1) error {
	m.finished = true
	return nil
}

type omHead struct {
	res         *cwom.ZwlrOutputHeadV1
	name        string
	description string
	modes       []*omMode
	enabled     bool
	current     *omMode
	position    image.Point
	scale       float64
	transform   cw.WlOutputTransform
	finished    bool
}

func (h *omHead) Name(_ *cwom.ZwlrOutputHeadV1, name string) error {
	h.name = name
	return nil
}

func (h *omHead) Description(_ *cwom.ZwlrOutputHeadV1, d string) error {
	h.description = d
	return nil
}

func (*omHead) PhysicalSize(*cwom.ZwlrOutputHeadV1, int32, int32) error { return nil }

func (h *omHead) Mode(_ *cwom.ZwlrOutputHeadV1, res *cwom.ZwlrOutputModeV1) (cwom.ZwlrOutputModeV1Handler, error) {
	m := &omMode{res: res}
	h.modes = append(h.modes, m)
	return m, nil
}

func (h *omHead) Enabled(_ *cwom.ZwlrOutputHeadV1, on int32) error {
	h.enabled = on != 0
	return nil
}

func (h *omHead) CurrentMode(_ *cwom.ZwlrOutputHeadV1, res *cwom.ZwlrOutputModeV1) error {
	i := slices.IndexFunc(h.modes, func(m *omMode) bool { return m.res == res })
	if i < 0 {
		return errors.New("current mode is not one of the head's modes")
	}
	h.current = h.modes[i]
	return nil
}

func (h *omHead) Position(_ *cwom.ZwlrOutputHeadV1, x, y int32) error {
	h.position = image.Pt(int(x), int(y))
	return nil
}

func (h *omHead) Transform(_ *cwom.ZwlrOutputHeadV1, t cw.WlOutputTransform) error {
	h.transform = t
	return nil
}

func (h *omHead) Scale(_ *cwom.ZwlrOutputHeadV1, s wire.Fixed) error {
	h.scale = s.Float()
	return nil
}

func (h *omHead) Finished(*cwom.ZwlrOutputHeadV1) error {
	h.finished = true
	return nil
}

func (*omHead) Make(*cwom.ZwlrOutputHeadV1, string) error         { return nil }
func (*omHead) Model(*cwom.ZwlrOutputHeadV1, string) error        { return nil }
func (*omHead) SerialNumber(*cwom.ZwlrOutputHeadV1, string) error { return nil }
func (*omHead) AdaptiveSync(*cwom.ZwlrOutputHeadV1, cwom.ZwlrOutputHeadV1AdaptiveSyncState) error {
	return nil
}

func (h *omHead) mode(m backend.Mode) *omMode {
	i := slices.IndexFunc(h.modes, func(x *omMode) bool { return x.mode == m && !x.finished })
	if i < 0 {
		return nil
	}
	return h.modes[i]
}

type omManager struct {
	res      *cwom.ZwlrOutputManagerV1
	heads    []*omHead
	serial   uint32
	dones    int
	finished int
}

func (m *omManager) Head(_ *cwom.ZwlrOutputManagerV1, res *cwom.ZwlrOutputHeadV1) (cwom.ZwlrOutputHeadV1Handler, error) {
	h := &omHead{res: res}
	m.heads = append(m.heads, h)
	return h, nil
}

func (m *omManager) Done(_ *cwom.ZwlrOutputManagerV1, serial uint32) error {
	m.serial = serial
	m.dones++
	return nil
}

func (m *omManager) Finished(*cwom.ZwlrOutputManagerV1) error {
	m.finished++
	return nil
}

func (m *omManager) head(name string) *omHead {
	i := slices.IndexFunc(m.heads, func(h *omHead) bool { return h.name == name && !h.finished })
	if i < 0 {
		return nil
	}
	return m.heads[i]
}

func (m *omManager) live() []*omHead {
	return slices.DeleteFunc(slices.Clone(m.heads), func(h *omHead) bool { return h.finished })
}

type omResult struct {
	succeeded, failed, cancelled bool
	answers                      int
}

func (r *omResult) Succeeded(*cwom.ZwlrOutputConfigurationV1) error {
	r.succeeded = true
	r.answers++
	return nil
}

func (r *omResult) Failed(*cwom.ZwlrOutputConfigurationV1) error {
	r.failed = true
	r.answers++
	return nil
}

func (r *omResult) Cancelled(*cwom.ZwlrOutputConfigurationV1) error {
	r.cancelled = true
	r.answers++
	return nil
}

func (r *omResult) answered() bool { return r.answers > 0 }

func outputManager(t *testing.T, c *testclient.Client) *omManager {
	t.Helper()
	m := &omManager{}
	res, err := cwom.NewZwlrOutputManagerV1(c.Display, 4, m)
	if err != nil {
		t.Fatal(err)
	}
	m.res = res
	c.Bind("zwlr_output_manager_v1", res)
	c.DispatchUntil("the heads", func() bool { return m.dones > 0 })
	return m
}

type headSetting func(*cwom.ZwlrOutputConfigurationHeadV1) error

func configure(t *testing.T, c *testclient.Client, m *omManager, serial uint32, apply bool, heads map[*omHead][]headSetting) *omResult {
	t.Helper()
	r := &omResult{}
	cfg, err := m.res.CreateConfiguration(r, serial)
	if err != nil {
		t.Fatal(err)
	}
	for h, settings := range heads {
		if settings == nil {
			if err := cfg.DisableHead(h.res); err != nil {
				t.Fatal(err)
			}
			continue
		}
		ch, err := cfg.EnableHead(h.res)
		if err != nil {
			t.Fatal(err)
		}
		for _, set := range settings {
			if err := set(ch); err != nil {
				t.Fatal(err)
			}
		}
	}
	finish := cfg.Test
	if apply {
		finish = cfg.Apply
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	c.DispatchUntil("the configuration answered", r.answered)
	if err := errors.Join(cfg.Destroy(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	if r.answers != 1 {
		t.Fatalf("a configuration was answered %d times: %+v", r.answers, *r)
	}
	return r
}

func everyHead(m *omManager, change map[*omHead][]headSetting) map[*omHead][]headSetting {
	all := map[*omHead][]headSetting{}
	for _, h := range m.live() {
		all[h] = []headSetting{}
	}
	maps.Copy(all, change)
	return all
}

func TestOutputManagementListsHeads(t *testing.T) {
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	m := outputManager(t, c)
	heads := m.live()
	if len(heads) != 2 {
		t.Fatalf("%d heads", len(heads))
	}
	for i, hd := range heads {
		wantAt := image.Pt(160*i, 0)
		if !hd.enabled || hd.current == nil || hd.current.mode != small || hd.position != wantAt || hd.scale != 1 || hd.transform != cw.WlOutputTransformNormal || hd.description == "" {
			t.Fatalf("head %d: %+v", i, *hd)
		}
	}

	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddNamedOutput("HEADLESS-9", wideMode, smallMode)
		return err
	})
	c.DispatchUntil("the plugged head", func() bool { return m.head("HEADLESS-9") != nil && m.dones == 2 })
	plugged := m.head("HEADLESS-9")
	if len(plugged.modes) != 2 || plugged.mode(wideMode) == nil || plugged.mode(smallMode) == nil || plugged.current != plugged.mode(wideMode) {
		t.Fatalf("plugged head %+v", *plugged)
	}
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		return b.RemoveOutput(b.Outputs()[2])
	})
	c.DispatchUntil("the unplugged head", func() bool { return plugged.finished && m.dones == 3 })
	if !plugged.modes[0].finished || !plugged.modes[1].finished {
		t.Fatal("the unplugged head's modes were not finished")
	}
}

func TestOutputManagementAppliesAConfiguration(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddNamedOutput("HEADLESS-9", wideMode, smallMode)
		return err
	})
	m := outputManager(t, c)
	plugged := m.head("HEADLESS-9")
	watcher := testclient.Connect(t, h.Socket())
	other := outputManager(t, watcher)

	r := configure(t, c, m, m.serial, true, everyHead(m, map[*omHead][]headSetting{plugged: {
		func(ch *cwom.ZwlrOutputConfigurationHeadV1) error { return ch.SetMode(plugged.mode(smallMode).res) },
		func(ch *cwom.ZwlrOutputConfigurationHeadV1) error { return ch.SetPosition(500, 40) },
		func(ch *cwom.ZwlrOutputConfigurationHeadV1) error { return ch.SetScale(wire.FixedFromFloat(2)) },
	}}))
	if !r.succeeded {
		t.Fatalf("apply answered %+v", *r)
	}
	c.DispatchUntil("the applied state", func() bool { return plugged.current == plugged.mode(smallMode) })
	if plugged.position != image.Pt(500, 40) || plugged.scale != 2 {
		t.Fatalf("applied head %+v", *plugged)
	}
	seen := other.head("HEADLESS-9")
	watcher.DispatchUntil("another client sees the change", func() bool { return seen.current == seen.mode(smallMode) && seen.scale == 2 })
	h.Do(func(b *headless.Backend, comp *compositor.Compositor) error {
		o := b.Outputs()[1]
		if o.State().Mode != smallMode {
			return errors.New("the backend kept its old mode")
		}
		for _, out := range comp.Outputs() {
			if out.Backend() == o && (out.Scale() != 2*output.Unit || out.Position() != image.Pt(500, 40)) {
				return errors.New("the output was not placed and scaled as applied")
			}
		}
		return nil
	})

	r = configure(t, c, m, m.serial, true, everyHead(m, map[*omHead][]headSetting{m.head("HEADLESS-1"): nil}))
	if !r.succeeded {
		t.Fatalf("disabling a head answered %+v", *r)
	}
	first := m.head("HEADLESS-1")
	c.DispatchUntil("the head disabled", func() bool { return !first.enabled })

	cfg := compositor.DefaultConfig(testenv.Keymap(t), testenv.Font(t))
	cfg.Decor.BorderWidth = 0
	h.Do(func(_ *headless.Backend, comp *compositor.Compositor) error {
		return comp.Reconfigure(cfg)
	})
	c.DispatchUntil("a reload brings the config back", func() bool {
		return first.enabled && plugged.current == plugged.mode(wideMode) && plugged.scale == 1
	})
}

func TestAReleasedModeIsNeverNamedCurrent(t *testing.T) {
	h := Start(t, small)
	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddNamedOutput("HEADLESS-9", wideMode, smallMode)
		return err
	})
	c := testclient.Connect(t, h.Socket())
	m := outputManager(t, c)
	watcher := testclient.Connect(t, h.Socket())
	seen := outputManager(t, watcher).head("HEADLESS-9")
	released := seen.mode(smallMode)
	if err := errors.Join(released.res.Release(), watcher.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	plugged := m.head("HEADLESS-9")
	r := configure(t, c, m, m.serial, true, everyHead(m, map[*omHead][]headSetting{plugged: {
		func(ch *cwom.ZwlrOutputConfigurationHeadV1) error { return ch.SetMode(plugged.mode(smallMode).res) },
	}}))
	if !r.succeeded {
		t.Fatalf("apply answered %+v", *r)
	}
	c.DispatchUntil("the applied state", func() bool { return plugged.current == plugged.mode(smallMode) })
	if err := watcher.Display.Roundtrip(); err != nil {
		t.Fatalf("the client that released the mode: %v", err)
	}
	if seen.current == released {
		t.Fatal("a released mode was named the current mode")
	}
}

func TestOutputManagementRefusesWhatItCannotDo(t *testing.T) {
	h := Start(t, small, small)
	c := testclient.Connect(t, h.Socket())
	m := outputManager(t, c)
	first := m.head("HEADLESS-1")
	serial := m.serial
	for _, tc := range []struct {
		what  string
		heads map[*omHead][]headSetting
	}{
		{"a mode the output lacks", everyHead(m, map[*omHead][]headSetting{first: {func(ch *cwom.ZwlrOutputConfigurationHeadV1) error { return ch.SetCustomMode(1024, 768, 0) }}})},
		{"a rotation", everyHead(m, map[*omHead][]headSetting{first: {func(ch *cwom.ZwlrOutputConfigurationHeadV1) error { return ch.SetTransform(cw.WlOutputTransform90) }}})},
		{"adaptive sync", everyHead(m, map[*omHead][]headSetting{first: {func(ch *cwom.ZwlrOutputConfigurationHeadV1) error {
			return ch.SetAdaptiveSync(cwom.ZwlrOutputHeadV1AdaptiveSyncStateEnabled)
		}}})},
		{"a tiny scale", everyHead(m, map[*omHead][]headSetting{first: {func(ch *cwom.ZwlrOutputConfigurationHeadV1) error { return ch.SetScale(wire.FixedFromFloat(0.1)) }}})},
		{"every output off", map[*omHead][]headSetting{first: nil, m.head("HEADLESS-2"): nil}},
	} {
		for _, apply := range []bool{false, true} {
			if r := configure(t, c, m, m.serial, apply, tc.heads); !r.failed {
				t.Errorf("%s (apply %t) answered %+v", tc.what, apply, *r)
			}
		}
	}
	if r := configure(t, c, m, m.serial, false, everyHead(m, nil)); !r.succeeded {
		t.Fatalf("a test of the current state answered %+v", *r)
	}
	if m.serial != serial || !first.enabled || first.current.mode != small {
		t.Fatalf("refused configurations changed the state: serial %d, head %+v", m.serial, *first)
	}

	h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		_, err := b.AddOutput(small)
		return err
	})
	c.DispatchUntil("the new serial", func() bool { return m.serial != serial })
	if r := configure(t, c, m, serial, true, everyHead(m, nil)); !r.cancelled {
		t.Fatalf("a configuration from an old serial answered %+v", *r)
	}
}

func TestOutputManagementProtocolErrors(t *testing.T) {
	for _, tc := range []struct {
		what string
		code uint32
		do   func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error
	}{
		{"a head left out", uint32(cwom.ZwlrOutputConfigurationV1ErrorUnconfiguredHead), func(_ *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			return cfg.Apply()
		}},
		{"a head configured twice", uint32(cwom.ZwlrOutputConfigurationV1ErrorAlreadyConfiguredHead), func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			return errors.Join(cfg.DisableHead(m.heads[0].res), cfg.DisableHead(m.heads[0].res))
		}},
		{"a request after apply", uint32(cwom.ZwlrOutputConfigurationV1ErrorAlreadyUsed), func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			_, err := cfg.EnableHead(m.heads[0].res)
			return errors.Join(err, cfg.Test(), cfg.Test())
		}},
		{"a mode set twice", uint32(cwom.ZwlrOutputConfigurationHeadV1ErrorAlreadySet), func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			ch, err := cfg.EnableHead(m.heads[0].res)
			return errors.Join(err, ch.SetMode(m.heads[0].current.res), ch.SetCustomMode(160, 100, 0))
		}},
		{"a zero scale", uint32(cwom.ZwlrOutputConfigurationHeadV1ErrorInvalidScale), func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			ch, err := cfg.EnableHead(m.heads[0].res)
			return errors.Join(err, ch.SetScale(0))
		}},
		{"a zero custom mode", uint32(cwom.ZwlrOutputConfigurationHeadV1ErrorInvalidCustomMode), func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			ch, err := cfg.EnableHead(m.heads[0].res)
			return errors.Join(err, ch.SetCustomMode(0, 0, 0))
		}},
		{"a transform past the enum", uint32(cwom.ZwlrOutputConfigurationHeadV1ErrorInvalidTransform), func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			ch, err := cfg.EnableHead(m.heads[0].res)
			return errors.Join(err, ch.SetTransform(8))
		}},
		{"an adaptive sync state past the enum", uint32(cwom.ZwlrOutputConfigurationHeadV1ErrorInvalidAdaptiveSyncState), func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			ch, err := cfg.EnableHead(m.heads[0].res)
			return errors.Join(err, ch.SetAdaptiveSync(2))
		}},
		{"a position set twice", uint32(cwom.ZwlrOutputConfigurationHeadV1ErrorAlreadySet), func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			ch, err := cfg.EnableHead(m.heads[0].res)
			return errors.Join(err, ch.SetPosition(0, 0), ch.SetPosition(1, 1))
		}},
		{"a transform set twice", uint32(cwom.ZwlrOutputConfigurationHeadV1ErrorAlreadySet), func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			ch, err := cfg.EnableHead(m.heads[0].res)
			return errors.Join(err, ch.SetTransform(0), ch.SetTransform(0))
		}},
		{"adaptive sync set twice", uint32(cwom.ZwlrOutputConfigurationHeadV1ErrorAlreadySet), func(m *omManager, cfg *cwom.ZwlrOutputConfigurationV1) error {
			ch, err := cfg.EnableHead(m.heads[0].res)
			return errors.Join(err, ch.SetAdaptiveSync(0), ch.SetAdaptiveSync(0))
		}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			h := Start(t, small)
			c := testclient.Connect(t, h.Socket())
			m := outputManager(t, c)
			cfg, err := m.res.CreateConfiguration(&omResult{}, m.serial)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.do(m, cfg); err != nil {
				t.Fatal(err)
			}
			c.ExpectProtocolError(tc.code)
		})
	}
}
