// Package outputmanagement implements zwlr_output_manager_v1: display
// configuration tools such as wlr-randr, kanshi and wdisplays list the
// outputs with their modes and change them as one configuration.
package outputmanagement

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	swom "github.com/stubbedev/lys-wm/internal/protocol/server/wlroutputmanagement"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 4

type Head struct {
	Output   backend.Output
	Enabled  bool
	Mode     backend.Mode
	Position image.Point
	Scale    output.Scale
}

type ModeRequest struct {
	Size       image.Point
	RefreshMHz int32
}

type HeadConfig struct {
	Output   backend.Output
	Enabled  bool
	Mode     *ModeRequest
	Position *image.Point
	Scale    *output.Scale
}

type Controller interface {
	Heads() []Head
	Configure(heads []HeadConfig, apply bool) error
}

var ErrRefused = errors.New("outputmanagement: refused")

type Manager struct {
	ctl         Controller
	report      func(error)
	serial      uint32
	heads       []Head
	bindings    []*binding
	configuring bool
	pending     bool
}

func New(d *server.Display, ctl Controller, report func(error)) (*Manager, error) {
	m := &Manager{ctl: ctl, report: report, serial: 1, heads: ctl.Heads()}
	if _, err := d.AddGlobal(swom.ZwlrOutputManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

type binding struct {
	m     *Manager
	c     *server.Client
	res   *swom.ZwlrOutputManagerV1
	heads []*headBinding
}

type headBinding struct {
	b        *binding
	res      *swom.ZwlrOutputHeadV1
	out      backend.Output
	modes    []*modeBinding
	sent     Head
	gone     bool
	released bool
}

type modeBinding struct {
	h        *headBinding
	res      *swom.ZwlrOutputModeV1
	mode     backend.Mode
	released bool
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	b := &binding{m: m, c: c}
	res, err := swom.NewZwlrOutputManagerV1(c, id, v, b)
	if err != nil {
		return err
	}
	b.res = res
	server.Track(res, &m.bindings, b)
	errs := make([]error, 0, len(m.heads)+1)
	for _, h := range m.heads {
		errs = append(errs, b.announce(h))
	}
	return errors.Join(append(errs, res.SendDone(m.serial))...)
}

func (b *binding) CreateConfiguration(_ *swom.ZwlrOutputManagerV1, res *swom.ZwlrOutputConfigurationV1, serial uint32) (swom.ZwlrOutputConfigurationV1Handler, error) {
	return &configuration{b: b, res: res, serial: serial}, nil
}

func (b *binding) Stop(obj *swom.ZwlrOutputManagerV1) error {
	return obj.SendFinished()
}

func scaleFixed(s output.Scale) wire.Fixed {
	return wire.FixedFromFloat(s.Float())
}

func (b *binding) announce(h Head) error {
	hb := &headBinding{b: b, out: h.Output}
	res, err := b.res.SendHead(hb)
	if err != nil {
		return fmt.Errorf("outputmanagement: announce %s: %w", h.Output.Name(), err)
	}
	hb.res = res
	b.heads = append(b.heads, hb)
	o := h.Output
	errs := []error{res.SendName(o.Name()), res.SendDescription(o.Description())}
	if size := o.PhysicalSize(); size.X > 0 && size.Y > 0 {
		errs = append(errs, res.SendPhysicalSize(int32(size.X), int32(size.Y))) //nolint:gosec // millimetres
	}
	for _, mode := range o.Modes() {
		errs = append(errs, hb.addMode(mode))
	}
	if res.Base().Version() >= 2 {
		id := o.Identity()
		for _, send := range []struct {
			value string
			fn    func(string) error
		}{{id.Make, res.SendMake}, {id.Model, res.SendModel}, {id.Serial, res.SendSerialNumber}} {
			if send.value != "" {
				errs = append(errs, send.fn(send.value))
			}
		}
	}
	if res.Base().Version() >= 4 {
		errs = append(errs, res.SendAdaptiveSync(swom.ZwlrOutputHeadV1AdaptiveSyncStateDisabled))
	}
	errs = append(errs, res.SendEnabled(enabled(h.Enabled)))
	if h.Enabled {
		errs = append(errs, hb.sendPlacement(h, Head{}))
	}
	hb.sent = h
	return errors.Join(errs...)
}

func enabled(on bool) int32 {
	if on {
		return 1
	}
	return 0
}

func (hb *headBinding) addMode(mode backend.Mode) error {
	mb := &modeBinding{h: hb, mode: mode}
	res, err := hb.res.SendMode(mb)
	if err != nil {
		return fmt.Errorf("outputmanagement: mode of %s: %w", hb.out.Name(), err)
	}
	mb.res = res
	hb.modes = append(hb.modes, mb)
	errs := []error{res.SendSize(int32(mode.Size.X), int32(mode.Size.Y)), res.SendRefresh(mode.RefreshMHz)} //nolint:gosec // mode sizes fit
	if mode.Preferred {
		errs = append(errs, res.SendPreferred())
	}
	return errors.Join(errs...)
}

func (hb *headBinding) sendPlacement(now, before Head) error {
	var errs []error
	if now.Mode != before.Mode {
		if i := slices.IndexFunc(hb.modes, func(mb *modeBinding) bool { return mb.mode == now.Mode && !mb.released }); i >= 0 {
			errs = append(errs, hb.res.SendCurrentMode(hb.modes[i].res))
		}
	}
	if now.Position != before.Position || !before.Enabled {
		errs = append(errs, hb.res.SendPosition(int32(now.Position.X), int32(now.Position.Y))) //nolint:gosec // layout coordinates fit
	}
	if !before.Enabled {
		errs = append(errs, hb.res.SendTransform(sw.WlOutputTransformNormal))
	}
	if now.Scale != before.Scale {
		errs = append(errs, hb.res.SendScale(scaleFixed(now.Scale)))
	}
	return errors.Join(errs...)
}

func (hb *headBinding) update(now Head) error {
	if hb.released {
		hb.sent = now
		return nil
	}
	var errs []error
	modes := hb.out.Modes()
	for _, mb := range hb.modes {
		if !slices.Contains(modes, mb.mode) && !mb.released {
			errs = append(errs, mb.res.SendFinished())
		}
	}
	hb.modes = slices.DeleteFunc(hb.modes, func(mb *modeBinding) bool { return !slices.Contains(modes, mb.mode) })
	for _, mode := range modes {
		if !slices.ContainsFunc(hb.modes, func(mb *modeBinding) bool { return mb.mode == mode }) {
			errs = append(errs, hb.addMode(mode))
		}
	}
	before := hb.sent
	if now.Enabled != before.Enabled {
		errs = append(errs, hb.res.SendEnabled(enabled(now.Enabled)))
	}
	if now.Enabled {
		if !before.Enabled {
			before = Head{}
		}
		errs = append(errs, hb.sendPlacement(now, before))
	}
	hb.sent = now
	return errors.Join(errs...)
}

func (hb *headBinding) finish() error {
	hb.gone = true
	if hb.released {
		return nil
	}
	errs := make([]error, 0, len(hb.modes)+1)
	for _, mb := range hb.modes {
		if !mb.released {
			errs = append(errs, mb.res.SendFinished())
		}
	}
	return errors.Join(append(errs, hb.res.SendFinished())...)
}

func (hb *headBinding) Release(*swom.ZwlrOutputHeadV1) error {
	hb.released = true
	return nil
}

func (mb *modeBinding) Release(*swom.ZwlrOutputModeV1) error {
	mb.released = true
	return nil
}

func (m *Manager) Changed() error {
	if m.configuring {
		m.pending = true
		return nil
	}
	now := m.ctl.Heads()
	if slices.Equal(now, m.heads) {
		return nil
	}
	m.heads = now
	m.serial++
	var errs []error
	for _, b := range m.bindings {
		errs = append(errs, b.sync(now))
	}
	return errors.Join(errs...)
}

func (b *binding) sync(now []Head) error {
	var errs []error
	for _, hb := range b.heads {
		if !slices.ContainsFunc(now, func(h Head) bool { return h.Output == hb.out }) {
			errs = append(errs, hb.finish())
		}
	}
	b.heads = slices.DeleteFunc(b.heads, func(hb *headBinding) bool { return hb.gone })
	for _, h := range now {
		if i := slices.IndexFunc(b.heads, func(hb *headBinding) bool { return hb.out == h.Output }); i >= 0 {
			errs = append(errs, b.heads[i].update(h))
			continue
		}
		errs = append(errs, b.announce(h))
	}
	return errors.Join(append(errs, b.res.SendDone(b.m.serial))...)
}

type configuration struct {
	b      *binding
	res    *swom.ZwlrOutputConfigurationV1
	serial uint32
	heads  []*configHead
	used   bool
}

type configHead struct {
	head        *headBinding
	enabled     bool
	mode        *ModeRequest
	position    *image.Point
	scale       *output.Scale
	transform   bool
	adaptive    bool
	unsupported []string
}

func headOf(res *swom.ZwlrOutputHeadV1) (*headBinding, error) {
	hb, ok := res.Handler().(*headBinding)
	if !ok {
		return nil, fmt.Errorf("outputmanagement: head@%d has handler %T", res.Base().ID(), res.Handler())
	}
	return hb, nil
}

func (cfg *configuration) add(obj *swom.ZwlrOutputConfigurationV1, head *swom.ZwlrOutputHeadV1, on bool) (*configHead, error) {
	if cfg.used {
		return nil, obj.ProtocolError(swom.ZwlrOutputConfigurationV1ErrorAlreadyUsed, "the configuration was already applied or tested")
	}
	hb, err := headOf(head)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(cfg.heads, func(ch *configHead) bool { return ch.head == hb }) {
		return nil, obj.ProtocolError(swom.ZwlrOutputConfigurationV1ErrorAlreadyConfiguredHead, "head %s is configured twice", hb.out.Name())
	}
	ch := &configHead{head: hb, enabled: on}
	cfg.heads = append(cfg.heads, ch)
	return ch, nil
}

func (cfg *configuration) EnableHead(obj *swom.ZwlrOutputConfigurationV1, _ *swom.ZwlrOutputConfigurationHeadV1, head *swom.ZwlrOutputHeadV1) (swom.ZwlrOutputConfigurationHeadV1Handler, error) {
	return cfg.add(obj, head, true)
}

func (cfg *configuration) DisableHead(obj *swom.ZwlrOutputConfigurationV1, head *swom.ZwlrOutputHeadV1) error {
	_, err := cfg.add(obj, head, false)
	return err
}

func (cfg *configuration) Apply(obj *swom.ZwlrOutputConfigurationV1) error {
	return cfg.finish(obj, true)
}

func (cfg *configuration) Test(obj *swom.ZwlrOutputConfigurationV1) error {
	return cfg.finish(obj, false)
}

func (*configuration) Destroy(*swom.ZwlrOutputConfigurationV1) error {
	return nil
}

func (cfg *configuration) finish(obj *swom.ZwlrOutputConfigurationV1, apply bool) error {
	if cfg.used {
		return obj.ProtocolError(swom.ZwlrOutputConfigurationV1ErrorAlreadyUsed, "the configuration was already applied or tested")
	}
	cfg.used = true
	m := cfg.b.m
	if cfg.serial != m.serial {
		return cfg.res.SendCancelled()
	}
	for _, hb := range cfg.b.heads {
		if !hb.released && !slices.ContainsFunc(cfg.heads, func(ch *configHead) bool { return ch.head == hb }) {
			return obj.ProtocolError(swom.ZwlrOutputConfigurationV1ErrorUnconfiguredHead, "head %s is neither enabled nor disabled", hb.out.Name())
		}
	}
	m.configuring = true
	err := cfg.configure(apply)
	m.configuring = false
	send := cfg.res.SendSucceeded
	if err != nil {
		send = cfg.res.SendFailed
		if !errors.Is(err, ErrRefused) {
			m.report(err)
		}
	}
	sent := send()
	if !m.pending {
		return sent
	}
	m.pending = false
	return errors.Join(sent, m.Changed())
}

func (cfg *configuration) configure(apply bool) error {
	heads := make([]HeadConfig, 0, len(cfg.heads))
	for _, ch := range cfg.heads {
		if len(ch.unsupported) > 0 {
			return fmt.Errorf("%w: %s asks for %v, which lys-wm does not support", ErrRefused, ch.head.out.Name(), ch.unsupported)
		}
		heads = append(heads, HeadConfig{Output: ch.head.out, Enabled: ch.enabled, Mode: ch.mode, Position: ch.position, Scale: ch.scale})
	}
	return cfg.b.m.ctl.Configure(heads, apply)
}

func (ch *configHead) once(obj *swom.ZwlrOutputConfigurationHeadV1, set bool, what string) error {
	if set {
		return obj.ProtocolError(swom.ZwlrOutputConfigurationHeadV1ErrorAlreadySet, "the %s of %s is already set", what, ch.head.out.Name())
	}
	return nil
}

func (ch *configHead) SetMode(obj *swom.ZwlrOutputConfigurationHeadV1, mode *swom.ZwlrOutputModeV1) error {
	if err := ch.once(obj, ch.mode != nil, "mode"); err != nil {
		return err
	}
	mb, ok := mode.Handler().(*modeBinding)
	if !ok || mb.h != ch.head {
		return obj.ProtocolError(swom.ZwlrOutputConfigurationHeadV1ErrorInvalidMode, "mode@%d is not a mode of %s", mode.Base().ID(), ch.head.out.Name())
	}
	ch.mode = &ModeRequest{Size: mb.mode.Size, RefreshMHz: mb.mode.RefreshMHz}
	return nil
}

func (ch *configHead) SetCustomMode(obj *swom.ZwlrOutputConfigurationHeadV1, width, height, refresh int32) error {
	if err := ch.once(obj, ch.mode != nil, "mode"); err != nil {
		return err
	}
	if width <= 0 || height <= 0 || refresh < 0 {
		return obj.ProtocolError(swom.ZwlrOutputConfigurationHeadV1ErrorInvalidCustomMode, "custom mode %dx%d@%dmHz", width, height, refresh)
	}
	ch.mode = &ModeRequest{Size: image.Pt(int(width), int(height)), RefreshMHz: refresh}
	return nil
}

func (ch *configHead) SetPosition(obj *swom.ZwlrOutputConfigurationHeadV1, x, y int32) error {
	if err := ch.once(obj, ch.position != nil, "position"); err != nil {
		return err
	}
	at := image.Pt(int(x), int(y))
	ch.position = &at
	return nil
}

func (ch *configHead) SetTransform(obj *swom.ZwlrOutputConfigurationHeadV1, transform sw.WlOutputTransform) error {
	if err := ch.once(obj, ch.transform, "transform"); err != nil {
		return err
	}
	ch.transform = true
	if transform > sw.WlOutputTransformFlipped270 {
		return obj.ProtocolError(swom.ZwlrOutputConfigurationHeadV1ErrorInvalidTransform, "transform %d", transform)
	}
	if transform != sw.WlOutputTransformNormal {
		ch.unsupported = append(ch.unsupported, fmt.Sprintf("transform %d", transform))
	}
	return nil
}

func (ch *configHead) SetScale(obj *swom.ZwlrOutputConfigurationHeadV1, scale wire.Fixed) error {
	if err := ch.once(obj, ch.scale != nil, "scale"); err != nil {
		return err
	}
	f := scale.Float()
	if f <= 0 {
		return obj.ProtocolError(swom.ZwlrOutputConfigurationHeadV1ErrorInvalidScale, "scale %g", f)
	}
	s, err := output.ParseScale(f)
	if err != nil {
		ch.unsupported = append(ch.unsupported, err.Error())
	}
	ch.scale = &s
	return nil
}

func (ch *configHead) SetAdaptiveSync(obj *swom.ZwlrOutputConfigurationHeadV1, state swom.ZwlrOutputHeadV1AdaptiveSyncState) error {
	if err := ch.once(obj, ch.adaptive, "adaptive sync"); err != nil {
		return err
	}
	ch.adaptive = true
	switch state {
	case swom.ZwlrOutputHeadV1AdaptiveSyncStateDisabled:
		return nil
	case swom.ZwlrOutputHeadV1AdaptiveSyncStateEnabled:
		ch.unsupported = append(ch.unsupported, "adaptive sync")
		return nil
	}
	return obj.ProtocolError(swom.ZwlrOutputConfigurationHeadV1ErrorInvalidAdaptiveSyncState, "adaptive sync state %d", state)
}
