package config

import (
	"errors"
	"image"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/binds"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/seat"
	"github.com/stubbedev/tilde/internal/session"
	"github.com/stubbedev/tilde/internal/testenv"
	"github.com/stubbedev/tilde/internal/wm"
)

func TestExampleConfig(t *testing.T) {
	data, err := os.ReadFile("testdata/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse("example.yaml", data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Outputs[1].Description != "LG Electronics 27GL850" {
		t.Errorf("second output %+v", cfg.Outputs[1])
	}
	if o := cfg.Outputs[0]; o.Name != "DP-1" || o.Mode != (compositor.ExactMode{Size: image.Pt(2560, 1440), RefreshMHz: 143912}) || o.Scale != output.Scale(150) {
		t.Errorf("first output %+v", o)
	}
	if o := cfg.Outputs[1]; o.Mode != (compositor.HighResMode{}) || o.Position != nil {
		t.Errorf("second output %+v", o)
	}
	if !cfg.Outputs[2].Disabled {
		t.Error("HDMI-A-1 is not disabled")
	}
	if cfg.Keyboard.Layout != "us,dk,es" || cfg.RepeatRate != 40 || cfg.RepeatDelay != 250 {
		t.Errorf("keyboard %+v repeat %d %d", cfg.Keyboard, cfg.RepeatRate, cfg.RepeatDelay)
	}
	if cfg.Xwayland {
		t.Error("xwayland.enabled: false left Xwayland on")
	}
	in := cfg.Input
	if *in.Pointer.AccelProfile != session.AccelFlat || *in.Pointer.AccelSpeed != -0.5 || *in.Pointer.ScrollFactor != 1.5 || in.Pointer.Tap != nil {
		t.Errorf("pointer %+v", in.Pointer)
	}
	if !*in.Touchpad.Tap || !*in.Touchpad.NaturalScroll || *in.Touchpad.ClickMethod != session.ClickFinger || *in.Touchpad.ScrollMethod != session.ScrollTwoFinger || in.Touchpad.AccelSpeed != nil {
		t.Errorf("touchpad %+v", in.Touchpad)
	}
	if mx := in.Devices["Logitech MX Master 3"]; len(in.Devices) != 1 || *mx.AccelProfile != session.AccelAdaptive || !*mx.NaturalScroll {
		t.Errorf("devices %+v", in.Devices)
	}
	if cfg.Decor.BorderWidth != 2 || cfg.Decor.ActiveBorder != (render.Color{R: 0xff, G: 0x88, A: 0xff}) || cfg.Decor.Tab.Height != 18 || cfg.Decor.Tab.Urgent.Text != (render.Color{A: 0xff}) {
		t.Errorf("decor %+v", cfg.Decor)
	}
	if cfg.Decor.Tab.Padding != compositor.DefaultDecor().Tab.Padding {
		t.Error("an unset tab field lost its default")
	}
	if cfg.WM.Autotile.MinTrigger != 500 || !cfg.WM.Autotile.Enabled {
		t.Errorf("autotile %+v", cfg.WM.Autotile)
	}
	if len(cfg.Binds) != 5 || cfg.Binds[3].Submap != "resize" || !cfg.Binds[3].Flags.Repeating || cfg.Binds[3].Action != (wm.ResizeAction{DX: 10}) {
		t.Errorf("binds %+v", cfg.Binds)
	}
	if d := wm.Decide(cfg.Rules, "steam", "Friends List"); d != (wm.Decision{Mode: wm.RuleFloat, Size: image.Pt(400, 900), Tearing: wm.TearingDeny}) {
		t.Errorf("rules decide %+v", d)
	}
	if d := wm.Decide(cfg.Rules, "cs2", "Counter-Strike 2"); d != (wm.Decision{Tearing: wm.TearingAllow}) {
		t.Errorf("rules decide %+v for cs2", d)
	}
	if !cfg.Tearing {
		t.Error("tearing: true left tearing off")
	}
	if !slices.Equal(cfg.Env, [][2]string{{"XDG_CURRENT_DESKTOP", "Hyprland"}, {"XDG_SESSION_TYPE", "wayland"}}) || !slices.Equal(cfg.Autostart, []string{"waybar"}) || cfg.Font != "/usr/share/fonts/DejaVuSans.ttf" {
		t.Errorf("env %v autostart %v font %q", cfg.Env, cfg.Autostart, cfg.Font)
	}
}

func TestEmptyConfigIsTheDefaults(t *testing.T) {
	cfg, err := Parse("empty.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Env, [][2]string{{"XDG_CURRENT_DESKTOP", "tilde"}, {"XDG_SESSION_TYPE", "wayland"}}) {
		t.Fatalf("session env %v", cfg.Env)
	}
	if !cfg.Xwayland || !cfg.ZeroScaling {
		t.Fatal("Xwayland or its zero scaling is off by default")
	}
	if cfg.Tearing {
		t.Fatal("tearing is on by default")
	}
	if cfg.GPU != "" {
		t.Fatalf("a primary GPU %q chosen without config", cfg.GPU)
	}
	for _, text := range []string{"gpu: card1\n", "gpu: /dev/dri/card1\n"} {
		if c, err := Parse("t.yaml", []byte(text)); err != nil || c.GPU != "card1" {
			t.Fatalf("%q gives gpu %q, %v", text, c.GPU, err)
		}
	}
	if cfg.Input.Devices != nil || cfg.Input.Pointer != (session.DeviceConfig{}) || cfg.Input.Touchpad != (session.DeviceConfig{}) {
		t.Fatalf("input %+v", cfg.Input)
	}
	if cfg.Decor != compositor.DefaultDecor() || cfg.WM != compositor.DefaultSettings() || cfg.RepeatRate != 50 || cfg.RepeatDelay != 300 {
		t.Fatalf("defaults %+v", cfg)
	}
}

func TestErrorsNameFileLineAndKey(t *testing.T) {
	cases := map[string]string{
		"borders:\n  colour: red\n":                                 "t.yaml:2: borders.colour: unknown key (known: active, inactive, width)",
		"binds:\n  - chord: Q\n    acton: kill\n":                   "t.yaml:3: binds[0].acton: unknown key (known: action, chord, flags)",
		"submaps:\n  r:\n    - {chord: Q, action: kill, x: 1}\n":    "t.yaml:3: submaps.r[0].x: unknown key",
		"tabs:\n  urgent:\n    fg: red\n":                           "t.yaml:3: tabs.urgent.fg: unknown key (known: background, text)",
		"rules:\n  - {app_id: a, floating: true}\n":                 "t.yaml:2: rules[0].floating: unknown key",
		"env:\n  TILDE_SOCKET: /x\n":                                "t.yaml:2: env.TILDE_SOCKET: is set by tilde",
		"env:\n  A=B: c\n":                                          "t.yaml:2: env.A=B: is not a valid environment variable",
		"tabs:\n  height: tall\n":                                   "t.yaml: yaml: unmarshal errors:\n  line 2: cannot unmarshal",
		"borders:\n  active: chartreuse\n":                          `t.yaml:2: borders.active: "chartreuse" is neither`,
		"binds:\n  - chord: SUPER + Q\n    action: teleport\n":      `t.yaml:3: binds[0].action: bad action: unknown action "teleport"`,
		"binds:\n  - chord: Q\n    action: kill\n    flags: [x]\n":  `t.yaml:4: binds[0].flags: unknown flag "x"`,
		"binds:\n  - chord: SUPER + R\n    action: submap resise\n": `t.yaml:3: binds[0].action: submap "resise" is not defined under submaps`,
		"rules:\n  - title: '('\n":                                  "t.yaml:2: rules[0].title: error parsing regexp",
		"rules:\n  - app_id: a\n    float: true\n    tile: true\n":  "t.yaml:2: rules[0]: cannot both float and tile",
		"rules:\n  - float: true\n":                                 "t.yaml:2: rules[0]: matches every window",
		"outputs:\n  - match: DP-1\n    mode: big\n":                `t.yaml:3: outputs[0].mode: "big" is not`,
		"outputs:\n  - match: DP-1\n    scale: 20\n":                "t.yaml:3: outputs[0].scale: output: scale 20 is outside",
		"outputs:\n  - match: DP-1\n    scale: .nan\n":              "t.yaml:3: outputs[0].scale: output: scale NaN is outside",
		"outputs:\n  - match: DP-1\n    mode: 1x1@nan\n":            `t.yaml:3: outputs[0].mode: "1x1@nan" is not`,
		"outputs:\n  - match: DP-1\n    mode: 1x1@inf\n":            `t.yaml:3: outputs[0].mode: "1x1@inf" is not`,
		"outputs:\n  - match: DP-1\n    mode: 1x1@1e12\n":           `t.yaml:3: outputs[0].mode: "1x1@1e12" is not`,
		"tabs:\n  height: 0\n":                                      "t.yaml:2: tabs.height: 0 is not a positive number",
		"palette:\n  accent: orange\n":                              `t.yaml:2: palette.accent: "orange" is not #rrggbb[aa]`,
		"input:\n  touchpad:\n    tapping: true\n":                  "t.yaml:3: input.touchpad.tapping: unknown key (known: accel_profile, accel_speed,",
		"input:\n  devices:\n    mouse:\n      speed: 1\n":          `t.yaml:4: input.devices.mouse.speed: unknown key`,
		"input:\n  touchpad:\n    click_method: tap\n":              `t.yaml:3: input.touchpad.click_method: "tap" is not one of button_areas, clickfinger, none`,
		"input:\n  pointer:\n    accel_speed: 2\n":                  "t.yaml:3: input.pointer.accel_speed: 2 is not from -1 to 1",
		"input:\n  pointer:\n    scroll_factor: 0\n":                "t.yaml:3: input.pointer.scroll_factor: 0 is not above 0 to 100",
		"input:\n  devices:\n    mouse:\n      accel_profile: x\n":  `t.yaml:4: input.devices."mouse".accel_profile: "x" is not one of adaptive, flat`,
		"gpu: renderD128\n":                                         `t.yaml:1: gpu: "renderD128" is not a DRM card such as card1 or /dev/dri/card1`,
	}
	for text, want := range cases {
		_, err := Parse("t.yaml", []byte(text))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", text, err, want)
		}
	}
}

type keys struct{}

func (keys) Keycode(name string) (uint32, error) {
	if name == "Q" {
		return 16, nil
	}
	return 0, errors.New("no such key")
}

func (keys) ModMask(string) (uint32, error) { return 64, nil }

func TestChordsResolveWithTheirLine(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("binds:\n  - chord: SUPER + Q\n    action: kill\n  - chord: SUPER + Nope\n    action: kill\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.resolveBinds(keys{})
	var ce *Error
	if !errors.As(err, &ce) || ce.Line != 4 || !errors.Is(err, binds.ErrBadChord) {
		t.Fatalf("an unknown key resolved to %v", err)
	}
}

func TestTwoBindsOnOneChordNameBothLines(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("binds:\n  - chord: SUPER + Q\n    action: kill\n  - chord: SUPER+Q\n    action: reload\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.resolveBinds(keys{})
	var ce *Error
	if !errors.As(err, &ce) || ce.Line != 4 || !strings.Contains(err.Error(), "at line 2") {
		t.Fatalf("a chord bound twice resolved to %v", err)
	}
}

func TestTheBuiltInConfigBuildsACompositorConfig(t *testing.T) {
	file, err := Parse("default.yaml", Default)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := file.Compositor(testenv.Keymap(t), testenv.Font(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Binds) != len(file.Binds) || cfg.Seat.RepeatRate != seat.DefaultRepeatRate || cfg.Decor != compositor.DefaultDecor() {
		t.Fatalf("%d binds of %d, seat %+v", len(cfg.Binds), len(file.Binds), cfg.Seat)
	}
	if _, err := binds.NewMap(cfg.Binds, 0); err != nil {
		t.Fatal(err)
	}
}
