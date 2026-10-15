package compositor

import (
	"errors"
	"fmt"
	"image"
	"maps"
	"slices"
	"strings"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/outputmanagement"
)

type OutputRule struct {
	Name        string
	Description string
	Mode        ModePolicy
	Position    *image.Point
	Scale       output.Scale
	Disabled    bool
}

const refreshToleranceMHz = 1000

func (r OutputRule) matches(b backend.Output) bool {
	if r.Description != "" {
		return strings.HasPrefix(b.Description(), r.Description)
	}
	return r.Name == b.Name()
}

func (r OutputRule) scale() output.Scale {
	if r.Scale == 0 {
		return output.Unit
	}
	return r.Scale
}

func internalPanel(name string) bool {
	return strings.HasPrefix(name, "eDP")
}

func (c *Compositor) configuredRule(b backend.Output) OutputRule {
	if rule, ok := c.outputOverrides[b.Name()]; ok {
		return rule
	}
	if i := slices.IndexFunc(c.outputRules, func(r OutputRule) bool { return r.matches(b) }); i >= 0 {
		return c.outputRules[i]
	}
	return OutputRule{}
}

func (c *Compositor) outputRule(b backend.Output) OutputRule {
	rule := c.configuredRule(b)
	if c.lidClosed && internalPanel(b.Name()) {
		rule.Disabled = true
	}
	return rule
}

type ModePolicy interface {
	choose(modes []backend.Mode, current backend.Mode) (backend.Mode, error)
}

type (
	PreferredMode struct{}
	HighResMode   struct{}
	ExactMode     struct {
		Size       image.Point
		RefreshMHz int32
	}
)

func (PreferredMode) choose(modes []backend.Mode, current backend.Mode) (backend.Mode, error) {
	if i := slices.IndexFunc(modes, func(m backend.Mode) bool { return m.Preferred }); i >= 0 {
		return modes[i], nil
	}
	return current, nil
}

func (HighResMode) choose(modes []backend.Mode, current backend.Mode) (backend.Mode, error) {
	best := current
	for _, m := range modes {
		area, bestArea := m.Size.X*m.Size.Y, best.Size.X*best.Size.Y
		if area > bestArea || area == bestArea && m.RefreshMHz > best.RefreshMHz {
			best = m
		}
	}
	return best, nil
}

func (e ExactMode) choose(modes []backend.Mode, _ backend.Mode) (backend.Mode, error) {
	var best backend.Mode
	found := false
	for _, m := range modes {
		if m.Size != e.Size {
			continue
		}
		if e.RefreshMHz == 0 {
			if !found || m.RefreshMHz > best.RefreshMHz {
				best, found = m, true
			}
			continue
		}
		off := abs(m.RefreshMHz - e.RefreshMHz)
		if off <= refreshToleranceMHz && (!found || off < abs(best.RefreshMHz-e.RefreshMHz)) {
			best, found = m, true
		}
	}
	if !found {
		return backend.Mode{}, fmt.Errorf("no mode %dx%d@%dmHz among %v", e.Size.X, e.Size.Y, e.RefreshMHz, modes)
	}
	return best, nil
}

func (r OutputRule) mode() ModePolicy {
	if r.Mode == nil {
		return PreferredMode{}
	}
	return r.Mode
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func (c *Compositor) commitOutput(b backend.Output, rule OutputRule) (bool, error) {
	current := b.State()
	next := current
	switch {
	case rule.Disabled:
		next.Power = backend.Disabled
	case !current.Enabled():
		next.Power = backend.PoweredOn
	}
	if next.Enabled() {
		m, err := rule.mode().choose(b.Modes(), current.Mode)
		if err != nil {
			return false, fmt.Errorf("compositor: output %s: %w", b.Name(), err)
		}
		next.Mode = m
	}
	if next == current {
		return false, nil
	}
	if err := b.Commit(next); err != nil {
		return false, fmt.Errorf("compositor: output %s: %w", b.Name(), err)
	}
	return true, nil
}

func (c *Compositor) relayout() error {
	right := 0
	for _, o := range c.outputs {
		if at := c.outputRule(o.Backend()).Position; at != nil {
			right = max(right, at.X+o.LogicalSize().X)
		}
	}
	var errs []error
	for _, o := range c.outputs {
		at := image.Pt(right, 0)
		if fixed := c.outputRule(o.Backend()).Position; fixed != nil {
			at = *fixed
		} else {
			right += o.LogicalSize().X
		}
		if o.Position() != at {
			errs = append(errs, o.Update(at, o.Scale()))
		}
	}
	return errors.Join(append(errs, c.shell.ReconfigureLock(), c.confirmLock(), c.outputConfig.Changed())...)
}

func (c *Compositor) output(b backend.Output) *output.Output {
	for _, o := range c.outputs {
		if o.Backend() == b {
			return o
		}
	}
	return nil
}

func (c *Compositor) knownOutput(b backend.Output, event string) (*output.Output, error) {
	if o := c.output(b); o != nil {
		return o, nil
	}
	return nil, fmt.Errorf("compositor: %s unknown output %s", event, b.Name())
}

func (c *Compositor) outputLive(o *output.Output) bool {
	return o != nil && c.output(o.Backend()) == o
}

func (c *Compositor) OutputAdded(b backend.Output) error {
	rule := c.outputRule(b)
	if _, err := c.commitOutput(b, rule); err != nil {
		c.report(err)
	}
	if !b.State().Enabled() {
		c.disabled = append(c.disabled, b)
		return c.outputConfig.Changed()
	}
	return c.enable(b, rule)
}

func (c *Compositor) enable(b backend.Output, rule OutputRule) error {
	o, err := c.layout.Add(b, image.Point{}, rule.scale())
	if err != nil {
		return err
	}
	c.outputs = append(c.outputs, o)
	c.addCursorPlane(o)
	if err := c.relayout(); err != nil {
		return err
	}
	if len(c.outputs) == 1 {
		center := o.Logical().Min.Add(o.LogicalSize().Div(2))
		c.cursor = point{float64(center.X), float64(center.Y)}
	}
	if _, err := c.arrange(o); err != nil {
		return err
	}
	if err := c.update(c.wm.AddOutput(b.Name(), o.Logical(), c.usable[o].Add(o.Position()))); err != nil {
		return err
	}
	return errors.Join(c.syncAreas(), c.scheduleAll())
}

func (c *Compositor) OutputRemoved(b backend.Output) error {
	if i := slices.Index(c.disabled, b); i >= 0 {
		c.disabled = slices.Delete(c.disabled, i, i+1)
		return c.outputConfig.Changed()
	}
	o, err := c.knownOutput(b, "removal of")
	if err != nil {
		return err
	}
	return c.disable(o)
}

func (c *Compositor) disable(o *output.Output) error {
	b := o.Backend()
	c.outputs = slices.DeleteFunc(c.outputs, func(x *output.Output) bool { return x == o })
	delete(c.shown, o)
	delete(c.hwCursors, o)
	delete(c.blanked, o)
	errs := []error{c.fill(o, nil, nil), c.dropCaptures(o), c.closeLayers(o), o.Remove()}
	delete(c.usable, o)
	delete(c.scenes, o)
	errs = append(errs, c.dropLabels(o), c.power.Removed(o), c.gamma.Removed(o))
	for _, e := range c.entered {
		if slices.Contains(e.outputs, o) {
			errs = append(errs, e.s.Client().Sent(e.s.Leave(o.Resources(e.s.Client()))))
			e.outputs = slices.DeleteFunc(e.outputs, func(x *output.Output) bool { return x == o })
		}
	}
	for _, w := range c.windows {
		if w.output == o {
			w.output = nil
		}
	}
	s, err := c.wm.RemoveOutput(b.Name())
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	c.wm = s
	errs = append(errs, c.relayout(), c.syncAreas(), c.fifo.Latched(nil))
	return errors.Join(errs...)
}

func (c *Compositor) Heads() []outputmanagement.Head {
	heads := make([]outputmanagement.Head, 0, len(c.outputs)+len(c.disabled))
	for _, o := range c.outputs {
		b := o.Backend()
		heads = append(heads, outputmanagement.Head{Output: b, Enabled: true, Mode: b.State().Mode, Position: o.Position(), Scale: o.Scale()})
	}
	for _, b := range c.disabled {
		heads = append(heads, outputmanagement.Head{Output: b})
	}
	slices.SortFunc(heads, func(a, b outputmanagement.Head) int { return strings.Compare(a.Output.Name(), b.Output.Name()) })
	return heads
}

func (c *Compositor) Configure(heads []outputmanagement.HeadConfig, apply bool) error {
	overrides := maps.Clone(c.outputOverrides)
	if overrides == nil {
		overrides = map[string]OutputRule{}
	}
	lit := false
	for _, h := range heads {
		b := h.Output
		rule := c.configuredRule(b)
		rule.Disabled = !h.Enabled
		if h.Mode != nil {
			rule.Mode = ExactMode{Size: h.Mode.Size, RefreshMHz: h.Mode.RefreshMHz}
		}
		if h.Position != nil {
			at := *h.Position
			rule.Position = &at
		}
		if h.Scale != 0 {
			rule.Scale = h.Scale
		}
		overrides[b.Name()] = rule
		if !h.Enabled {
			continue
		}
		lit = true
		if _, err := rule.mode().choose(b.Modes(), b.State().Mode); err != nil {
			return fmt.Errorf("%w: output %s: %w", outputmanagement.ErrRefused, b.Name(), err)
		}
	}
	if !lit {
		return fmt.Errorf("%w: a configuration that turns off every output would leave nothing on screen", outputmanagement.ErrRefused)
	}
	if !apply {
		return nil
	}
	previous := c.outputOverrides
	c.outputOverrides = overrides
	if err := c.applyOutputRules(); err != nil {
		c.outputOverrides = previous
		return errors.Join(err, c.applyOutputRules())
	}
	return nil
}

func (c *Compositor) OutputDamaged(b backend.Output) error {
	o, err := c.knownOutput(b, "damage on")
	if err != nil {
		return err
	}
	c.sceneOf(o).invalidate()
	return c.schedule(o)
}

func (c *Compositor) OutputChanged(b backend.Output) error {
	if slices.Contains(c.disabled, b) {
		return nil
	}
	o, err := c.knownOutput(b, "change of")
	if err != nil {
		return err
	}
	return c.outputResized(o)
}

func (c *Compositor) outputResized(o *output.Output) error {
	if err := errors.Join(o.Changed(), c.relayout()); err != nil {
		return err
	}
	_, err := c.arrange(o)
	return errors.Join(err, c.syncAreas(), c.schedule(o))
}

func (c *Compositor) SetOutputScale(b backend.Output, scale output.Scale) error {
	o, err := c.knownOutput(b, "scale for")
	if err != nil {
		return err
	}
	if err := o.Update(o.Position(), scale); err != nil {
		return err
	}
	return c.outputResized(o)
}

func (c *Compositor) applyOutputRules() error {
	var errs []error
	for _, o := range slices.Clone(c.outputs) {
		b := o.Backend()
		rule := c.outputRule(b)
		if rule.Disabled {
			if _, err := c.commitOutput(b, rule); err != nil {
				errs = append(errs, err)
				continue
			}
			c.disabled = append(c.disabled, b)
			errs = append(errs, c.disable(o))
			continue
		}
		changed, err := c.commitOutput(b, rule)
		if err != nil {
			errs = append(errs, err)
		}
		if changed || o.Scale() != rule.scale() {
			errs = append(errs, o.Update(o.Position(), rule.scale()), c.outputResized(o))
		}
	}
	for _, b := range slices.Clone(c.disabled) {
		rule := c.outputRule(b)
		if rule.Disabled {
			continue
		}
		if _, err := c.commitOutput(b, rule); err != nil {
			errs = append(errs, err)
			continue
		}
		c.disabled = slices.DeleteFunc(c.disabled, func(x backend.Output) bool { return x == b })
		errs = append(errs, c.enable(b, rule))
	}
	errs = append(errs, c.relayout(), c.syncAreas())
	return errors.Join(errs...)
}
