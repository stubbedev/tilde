package session

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/stubbedev/lys-wm/internal/backend"
)

type ClickMethod uint32

const (
	ClickNone        ClickMethod = 0
	ClickButtonAreas ClickMethod = 1 << 0
	ClickFinger      ClickMethod = 1 << 1
)

type ScrollMethod uint32

const (
	ScrollNone         ScrollMethod = 0
	ScrollTwoFinger    ScrollMethod = 1 << 0
	ScrollEdge         ScrollMethod = 1 << 1
	ScrollOnButtonDown ScrollMethod = 1 << 2
)

type AccelProfile uint32

const (
	AccelFlat     AccelProfile = 1 << 0
	AccelAdaptive AccelProfile = 1 << 1
)

type DeviceConfig struct {
	Tap, DragLock, NaturalScroll, DisableWhileTyping, MiddleEmulation *bool
	ClickMethod                                                       *ClickMethod
	ScrollMethod                                                      *ScrollMethod
	AccelProfile                                                      *AccelProfile
	AccelSpeed, ScrollFactor                                          *float64
}

type InputConfig struct {
	Pointer, Touchpad DeviceConfig
	Devices           map[string]DeviceConfig
}

type knob[T any] struct {
	name      string
	available bool
	def       func() T
	set       func(T) int32
}

func (k knob[T]) apply(device string, class, override *T) error {
	if !k.available {
		if override != nil {
			return fmt.Errorf("session: input device %q has no %s setting", device, k.name)
		}
		return nil
	}
	v := k.def()
	switch {
	case override != nil:
		v = *override
	case class != nil:
		v = *class
	}
	if status := k.set(v); status != configStatusSuccess {
		return fmt.Errorf("session: input device %q rejects %s %v (libinput status %d)", device, k.name, v, status)
	}
	return nil
}

func toggle(set func(uintptr, int32) int32, def func(uintptr) int32, h uintptr) (func() bool, func(bool) int32) {
	return func() bool { return def(h) != 0 }, func(on bool) int32 {
		if on {
			return set(h, 1)
		}
		return set(h, 0)
	}
}

func bind[T any](set func(uintptr, T) int32, def func(uintptr) T, h uintptr) (func() T, func(T) int32) {
	return func() T { return def(h) }, func(v T) int32 { return set(h, v) }
}

func (in *Libinput) class(d *InputDevice) DeviceConfig {
	switch {
	case d.touchpad:
		return in.config.Touchpad
	case d.caps&backend.Pointer != 0:
		return in.config.Pointer
	}
	return DeviceConfig{}
}

func (in *Libinput) configure(d *InputDevice) error {
	f, h, c, o := &in.fn, d.h, in.class(d), in.config.Devices[d.name]
	tap, accel := f.tapFingers(h) > 0, f.hasAccel(h) != 0
	toggles := []struct {
		name          string
		available     bool
		set           func(uintptr, int32) int32
		def           func(uintptr) int32
		class, device *bool
	}{
		{"tap", tap, f.tap, f.tapDefault, c.Tap, o.Tap},
		{"drag_lock", tap, f.dragLock, f.dragDefault, c.DragLock, o.DragLock},
		{"natural_scroll", f.hasNatural(h) != 0, f.natural, f.naturalDefault, c.NaturalScroll, o.NaturalScroll},
		{"disable_while_typing", f.hasDWT(h) != 0, f.dwt, f.dwtDefault, c.DisableWhileTyping, o.DisableWhileTyping},
		{"middle_emulation", f.hasMiddle(h) != 0, f.middle, f.middleDefault, c.MiddleEmulation, o.MiddleEmulation},
	}
	errs := make([]error, 0, len(toggles)+5)
	for _, t := range toggles {
		k := knob[bool]{name: t.name, available: t.available}
		k.def, k.set = toggle(t.set, t.def, h)
		errs = append(errs, k.apply(d.name, t.class, t.device))
	}
	click := knob[ClickMethod]{name: "click_method", available: f.clickMethods(h) != ClickNone}
	click.def, click.set = bind(f.clickMethod, f.clickDefault, h)
	scroll := knob[ScrollMethod]{name: "scroll_method", available: f.scrollMethods(h) != ScrollNone}
	scroll.def, scroll.set = bind(f.scrollMethod, f.scrollDefault, h)
	profile := knob[AccelProfile]{name: "accel_profile", available: accel}
	profile.def, profile.set = bind(f.accelProfile, f.profileDefault, h)
	speed := knob[float64]{name: "accel_speed", available: accel}
	speed.def, speed.set = bind(f.accelSpeed, f.speedDefault, h)
	factor := knob[float64]{name: "scroll_factor", available: d.caps&backend.Pointer != 0, def: func() float64 { return 1 }, set: func(v float64) int32 {
		d.scrollFactor = v
		return configStatusSuccess
	}}
	return errors.Join(append(errs,
		click.apply(d.name, c.ClickMethod, o.ClickMethod),
		scroll.apply(d.name, c.ScrollMethod, o.ScrollMethod),
		profile.apply(d.name, c.AccelProfile, o.AccelProfile),
		speed.apply(d.name, c.AccelSpeed, o.AccelSpeed),
		factor.apply(d.name, c.ScrollFactor, o.ScrollFactor),
	)...)
}

func (in *Libinput) Reconfigure(config InputConfig) error {
	in.config = config
	devices := slices.SortedFunc(maps.Values(in.devices), func(a, b *InputDevice) int { return cmp.Compare(a.name, b.name) })
	errs := make([]error, 0, len(devices))
	for _, d := range devices {
		errs = append(errs, in.configure(d))
	}
	return errors.Join(errs...)
}
