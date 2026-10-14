// Package config reads tilde's YAML config into validated types. Every
// error names the file, the line and the key.
package config

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"maps"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	gtext "github.com/stubbedev/gelm/render"
	"go.yaml.in/yaml/v3"

	"github.com/stubbedev/tilde/internal/binds"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/ipc"
	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/seat"
	"github.com/stubbedev/tilde/internal/session"
	"github.com/stubbedev/tilde/internal/wm"
	"github.com/stubbedev/tilde/internal/xkb"
)

var reservedEnv = []string{"WAYLAND_DISPLAY", "DISPLAY", ipc.SocketEnv}

var sessionEnv = map[string]string{"XDG_CURRENT_DESKTOP": "tilde", "XDG_SESSION_TYPE": "wayland"}

//go:embed default.yaml
var Default []byte

type At[T any] struct {
	V    T
	Line int
}

func (a *At[T]) UnmarshalYAML(n *yaml.Node) error {
	a.Line = n.Line
	return n.Decode(&a.V)
}

func (*At[T]) located() {}

var locatedType = reflect.TypeFor[interface{ located() }]()

func fieldNames(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for f := range t.Fields() {
		if name, _, _ := strings.Cut(f.Tag.Get("yaml"), ","); name != "" {
			fields[name] = f.Type
		}
	}
	return fields
}

func (p *parser) unknownKeys(n *yaml.Node, t reflect.Type, path string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(locatedType) {
		t = t.Field(0).Type
	}
	join := func(key string) string {
		if path == "" {
			return key
		}
		return path + "." + key
	}
	switch {
	case n.Kind == yaml.DocumentNode:
		for _, c := range n.Content {
			p.unknownKeys(c, t, path)
		}
	case n.Kind == yaml.SequenceNode && t.Kind() == reflect.Slice:
		for i, c := range n.Content {
			p.unknownKeys(c, t.Elem(), fmt.Sprintf("%s[%d]", path, i))
		}
	case n.Kind == yaml.MappingNode && t.Kind() == reflect.Map:
		for i := 0; i+1 < len(n.Content); i += 2 {
			p.unknownKeys(n.Content[i+1], t.Elem(), join(n.Content[i].Value))
		}
	case n.Kind == yaml.MappingNode && t.Kind() == reflect.Struct:
		fields := fieldNames(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			ft, ok := fields[key.Value]
			if !ok {
				p.fail(key.Line, join(key.Value), "unknown key (known: %s)", strings.Join(slices.Sorted(maps.Keys(fields)), ", "))
				continue
			}
			p.unknownKeys(n.Content[i+1], ft, join(key.Value))
		}
	}
}

type rawConfig struct {
	Outputs   []At[rawOutput]       `yaml:"outputs"`
	Input     rawInput              `yaml:"input"`
	Palette   map[string]At[string] `yaml:"palette"`
	Borders   rawBorders            `yaml:"borders"`
	Tabs      rawTabs               `yaml:"tabs"`
	Autotile  rawAutotile           `yaml:"autotile"`
	Binds     []rawBind             `yaml:"binds"`
	Submaps   map[string][]rawBind  `yaml:"submaps"`
	Rules     []At[rawRule]         `yaml:"rules"`
	Env       map[string]At[string] `yaml:"env"`
	Autostart []At[string]          `yaml:"autostart"`
	Xwayland  rawXwayland           `yaml:"xwayland"`
	GPU       *At[string]           `yaml:"gpu"`
	Tearing   bool                  `yaml:"tearing"`
}

type rawXwayland struct {
	Enabled     *bool `yaml:"enabled"`
	ZeroScaling *bool `yaml:"zero_scaling"`
}

type rawOutput struct {
	Match    At[string]   `yaml:"match"`
	Mode     *At[string]  `yaml:"mode"`
	Position *At[string]  `yaml:"position"`
	Scale    *At[float64] `yaml:"scale"`
	Disabled bool         `yaml:"disabled"`
}

type rawInput struct {
	Keyboard rawKeyboard          `yaml:"keyboard"`
	Pointer  rawDevice            `yaml:"pointer"`
	Touchpad rawDevice            `yaml:"touchpad"`
	Devices  map[string]rawDevice `yaml:"devices"`
}

type rawDevice struct {
	Tap                *bool        `yaml:"tap"`
	DragLock           *bool        `yaml:"drag_lock"`
	NaturalScroll      *bool        `yaml:"natural_scroll"`
	DisableWhileTyping *bool        `yaml:"disable_while_typing"`
	MiddleEmulation    *bool        `yaml:"middle_emulation"`
	ClickMethod        *At[string]  `yaml:"click_method"`
	ScrollMethod       *At[string]  `yaml:"scroll_method"`
	AccelProfile       *At[string]  `yaml:"accel_profile"`
	AccelSpeed         *At[float64] `yaml:"accel_speed"`
	ScrollFactor       *At[float64] `yaml:"scroll_factor"`
}

type rawKeyboard struct {
	Rules       string   `yaml:"rules"`
	Model       string   `yaml:"model"`
	Layout      string   `yaml:"layout"`
	Variant     string   `yaml:"variant"`
	Options     string   `yaml:"options"`
	RepeatRate  *At[int] `yaml:"repeat_rate"`
	RepeatDelay *At[int] `yaml:"repeat_delay"`
}

type rawBorders struct {
	Width    *At[int]    `yaml:"width"`
	Active   *At[string] `yaml:"active"`
	Inactive *At[string] `yaml:"inactive"`
}

type rawTabColors struct {
	Background At[string] `yaml:"background"`
	Text       At[string] `yaml:"text"`
}

type rawTabs struct {
	Height      *At[int]      `yaml:"height"`
	Padding     *At[int]      `yaml:"padding"`
	TextHeight  *At[int]      `yaml:"text_height"`
	TextPadding *At[int]      `yaml:"text_padding"`
	Font        *At[string]   `yaml:"font"`
	Active      *rawTabColors `yaml:"active"`
	Inactive    *rawTabColors `yaml:"inactive"`
	Urgent      *rawTabColors `yaml:"urgent"`
}

type rawAutotile struct {
	Enabled    *bool    `yaml:"enabled"`
	Ephemeral  *bool    `yaml:"ephemeral"`
	MinTrigger *At[int] `yaml:"min_trigger"`
	Divisor    *At[int] `yaml:"divisor"`
}

type rawBind struct {
	Chord  At[string]   `yaml:"chord"`
	Action At[string]   `yaml:"action"`
	Flags  []At[string] `yaml:"flags"`
}

type rawRule struct {
	AppID   *At[string] `yaml:"app_id"`
	Title   *At[string] `yaml:"title"`
	Float   bool        `yaml:"float"`
	Tile    bool        `yaml:"tile"`
	Size    *At[string] `yaml:"size"`
	Tearing *bool       `yaml:"tearing"`
}

type Error struct {
	File string
	Line int
	Key  string
	Err  error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s:%d: %s: %v", e.File, e.Line, e.Key, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

type Bind struct {
	Submap string
	Chord  string
	Line   int
	Action wm.Action
	Flags  binds.Flags
}

type Config struct {
	File        string
	Keyboard    xkb.Names
	RepeatRate  int32
	RepeatDelay int32
	Input       session.InputConfig
	Outputs     []compositor.OutputRule
	Decor       compositor.Decor
	Font        string
	WM          wm.Settings
	Binds       []Bind
	Rules       []wm.Rule
	Env         [][2]string
	Autostart   []string
	Xwayland    bool
	ZeroScaling bool
	GPU         string
	Tearing     bool
}

var mocha = map[string]render.Color{
	"rosewater": rgb(0xf5e0dc), "flamingo": rgb(0xf2cdcd), "pink": rgb(0xf5c2e7), "mauve": rgb(0xcba6f7),
	"red": rgb(0xf38ba8), "maroon": rgb(0xeba0ac), "peach": rgb(0xfab387), "yellow": rgb(0xf9e2af),
	"green": rgb(0xa6e3a1), "teal": rgb(0x94e2d5), "sky": rgb(0x89dceb), "sapphire": rgb(0x74c7ec),
	"blue": rgb(0x89b4fa), "lavender": rgb(0xb4befe), "text": rgb(0xcdd6f4), "subtext1": rgb(0xbac2de),
	"subtext0": rgb(0xa6adc8), "overlay2": rgb(0x9399b2), "overlay1": rgb(0x7f849c), "overlay0": rgb(0x6c7086),
	"surface2": rgb(0x585b70), "surface1": rgb(0x45475a), "surface0": rgb(0x313244), "base": rgb(0x1e1e2e),
	"mantle": rgb(0x181825), "crust": rgb(0x11111b),
}

func rgb(v uint32) render.Color {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return render.Color{R: b[1], G: b[2], B: b[3], A: 0xff}
}

type parser struct {
	file    string
	palette map[string]render.Color
	errs    []error
}

func (p *parser) fail(line int, key string, format string, args ...any) {
	p.errs = append(p.errs, &Error{File: p.file, Line: line, Key: key, Err: fmt.Errorf(format, args...)})
}

var cardName = regexp.MustCompile(`^card[0-9]+$`)

func (p *parser) card(a At[string]) string {
	name := strings.TrimPrefix(a.V, "/dev/dri/")
	if !cardName.MatchString(name) {
		p.fail(a.Line, "gpu", "%q is not a DRM card such as card1 or /dev/dri/card1", a.V)
	}
	return name
}

func parseHex(s string) (render.Color, bool) {
	digits, ok := strings.CutPrefix(s, "#")
	if !ok {
		return render.Color{}, false
	}
	b, err := hex.DecodeString(digits)
	switch {
	case err != nil:
		return render.Color{}, false
	case len(b) == 3:
		return render.Color{R: b[0], G: b[1], B: b[2], A: 0xff}, true
	case len(b) == 4:
		return render.Color{R: b[0], G: b[1], B: b[2], A: b[3]}, true
	}
	return render.Color{}, false
}

func (p *parser) color(a At[string], key string, fallback render.Color) render.Color {
	if c, ok := p.palette[a.V]; ok {
		return c
	}
	if c, ok := parseHex(a.V); ok {
		return c
	}
	p.fail(a.Line, key, "%q is neither a palette colour nor #rrggbb[aa]", a.V)
	return fallback
}

func (p *parser) optColor(a *At[string], key string, fallback render.Color) render.Color {
	if a == nil {
		return fallback
	}
	return p.color(*a, key, fallback)
}

func (p *parser) positive(a *At[int], key string, fallback int, allowZero bool) int {
	if a == nil {
		return fallback
	}
	if a.V < 0 || (a.V == 0 && !allowZero) {
		p.fail(a.Line, key, "%d is not a positive number", a.V)
		return fallback
	}
	return a.V
}

func (p *parser) repeat(a *At[int], key string, fallback int32) int32 {
	if a == nil {
		return fallback
	}
	if a.V < 0 || a.V > math.MaxInt32 {
		p.fail(a.Line, key, "%d is outside 0 to %d", a.V, math.MaxInt32)
		return fallback
	}
	return int32(a.V)
}

func parsePair(s string) (image.Point, bool) {
	if len(s) < 3 {
		return image.Point{}, false
	}
	i := strings.IndexByte(s[1:], 'x')
	if i < 0 {
		return image.Point{}, false
	}
	x, errX := strconv.Atoi(s[:i+1])
	y, errY := strconv.Atoi(s[i+2:])
	return image.Pt(x, y), errX == nil && errY == nil
}

func (p *parser) mode(a *At[string], key string) compositor.ModePolicy {
	switch a.V {
	case "preferred":
		return compositor.PreferredMode{}
	case "highres":
		return compositor.HighResMode{}
	}
	size, rate, hasRate := strings.Cut(a.V, "@")
	dims, ok := parsePair(size)
	hz, err := strconv.ParseFloat(rate, 64)
	if !ok || dims.X <= 0 || dims.Y <= 0 || (hasRate && (err != nil || hz <= 0)) {
		p.fail(a.Line, key, "%q is not preferred, highres or <width>x<height>[@<hz>]", a.V)
		return nil
	}
	return compositor.ExactMode{Size: dims, RefreshMHz: int32(math.Round(hz * 1000))}
}

func (p *parser) output(i int, r rawOutput) compositor.OutputRule {
	key := fmt.Sprintf("outputs[%d]", i)
	rule := compositor.OutputRule{Disabled: r.Disabled}
	switch desc, isDesc := strings.CutPrefix(r.Match.V, "desc:"); {
	case r.Match.V == "":
		p.fail(r.Match.Line, key+".match", "needs a connector name or desc:<description>")
	case isDesc:
		rule.Description = desc
	default:
		rule.Name = r.Match.V
	}
	if r.Mode != nil {
		rule.Mode = p.mode(r.Mode, key+".mode")
	}
	if r.Position != nil && r.Position.V != "auto" {
		at, ok := parsePair(r.Position.V)
		if !ok {
			p.fail(r.Position.Line, key+".position", "%q is not <x>x<y>", r.Position.V)
		} else {
			rule.Position = &at
		}
	}
	if r.Scale != nil {
		if r.Scale.V < output.MinScale.Float() || r.Scale.V > output.MaxScale.Float() {
			p.fail(r.Scale.Line, key+".scale", "%g is outside %g to %g", r.Scale.V, output.MinScale.Float(), output.MaxScale.Float())
		} else {
			rule.Scale = output.Scale(math.Round(r.Scale.V * float64(output.Unit)))
		}
	}
	return rule
}

var (
	clickMethods  = map[string]session.ClickMethod{"none": session.ClickNone, "button_areas": session.ClickButtonAreas, "clickfinger": session.ClickFinger}
	scrollMethods = map[string]session.ScrollMethod{"none": session.ScrollNone, "two_finger": session.ScrollTwoFinger, "edge": session.ScrollEdge, "on_button_down": session.ScrollOnButtonDown}
	accelProfiles = map[string]session.AccelProfile{"flat": session.AccelFlat, "adaptive": session.AccelAdaptive}
)

func choice[T any](p *parser, a *At[string], key string, names map[string]T) *T {
	if a == nil {
		return nil
	}
	v, ok := names[a.V]
	if !ok {
		p.fail(a.Line, key, "%q is not one of %s", a.V, strings.Join(slices.Sorted(maps.Keys(names)), ", "))
		return nil
	}
	return &v
}

func (p *parser) between(a *At[float64], key string, lo, hi float64, open bool) *float64 {
	if a == nil {
		return nil
	}
	if a.V > hi || a.V < lo || (open && a.V == lo) || math.IsNaN(a.V) {
		bound := "from"
		if open {
			bound = "above"
		}
		p.fail(a.Line, key, "%g is not %s %g to %g", a.V, bound, lo, hi)
		return nil
	}
	return &a.V
}

func (p *parser) device(r rawDevice, key string) session.DeviceConfig {
	return session.DeviceConfig{
		Tap: r.Tap, DragLock: r.DragLock, NaturalScroll: r.NaturalScroll,
		DisableWhileTyping: r.DisableWhileTyping, MiddleEmulation: r.MiddleEmulation,
		ClickMethod:  choice(p, r.ClickMethod, key+".click_method", clickMethods),
		ScrollMethod: choice(p, r.ScrollMethod, key+".scroll_method", scrollMethods),
		AccelProfile: choice(p, r.AccelProfile, key+".accel_profile", accelProfiles),
		AccelSpeed:   p.between(r.AccelSpeed, key+".accel_speed", -1, 1, false),
		ScrollFactor: p.between(r.ScrollFactor, key+".scroll_factor", 0, 100, true),
	}
}

func (p *parser) input(r rawInput) session.InputConfig {
	c := session.InputConfig{Pointer: p.device(r.Pointer, "input.pointer"), Touchpad: p.device(r.Touchpad, "input.touchpad")}
	if len(r.Devices) > 0 {
		c.Devices = make(map[string]session.DeviceConfig, len(r.Devices))
	}
	for _, name := range slices.Sorted(maps.Keys(r.Devices)) {
		c.Devices[name] = p.device(r.Devices[name], fmt.Sprintf("input.devices.%q", name))
	}
	return c
}

func (p *parser) tabColors(r *rawTabColors, key string, fallback compositor.TabColors) compositor.TabColors {
	if r == nil {
		return fallback
	}
	return compositor.TabColors{Background: p.color(r.Background, key+".background", fallback.Background), Text: p.color(r.Text, key+".text", fallback.Text)}
}

func (p *parser) bind(submap string, i int, r rawBind) Bind {
	key := fmt.Sprintf("binds[%d]", i)
	if submap != "" {
		key = fmt.Sprintf("submaps.%s[%d]", submap, i)
	}
	b := Bind{Submap: submap, Chord: r.Chord.V, Line: r.Chord.Line}
	if r.Chord.V == "" {
		p.fail(r.Chord.Line, key+".chord", "is empty")
	}
	a, err := wm.ParseAction(r.Action.V)
	if err != nil {
		p.fail(r.Action.Line, key+".action", "%v", err)
	}
	b.Action = a
	for _, f := range r.Flags {
		switch f.V {
		case "locked":
			b.Flags.Locked = true
		case "repeating":
			b.Flags.Repeating = true
		default:
			p.fail(f.Line, key+".flags", "unknown flag %q (locked, repeating)", f.V)
		}
	}
	return b
}

func (p *parser) regexp(a *At[string], key string) *regexp.Regexp {
	if a == nil {
		return nil
	}
	re, err := regexp.Compile(a.V)
	if err != nil {
		p.fail(a.Line, key, "%v", err)
	}
	return re
}

func (p *parser) rule(i int, at At[rawRule]) wm.Rule {
	r := at.V
	key := fmt.Sprintf("rules[%d]", i)
	rule := wm.Rule{AppID: p.regexp(r.AppID, key+".app_id"), Title: p.regexp(r.Title, key+".title")}
	switch {
	case r.Float && r.Tile:
		p.fail(at.Line, key, "cannot both float and tile")
	case r.Float:
		rule.Mode = wm.RuleFloat
	case r.Tile:
		rule.Mode = wm.RuleTile
	}
	if r.Size != nil {
		size, ok := parsePair(r.Size.V)
		if !ok || size.X <= 0 || size.Y <= 0 {
			p.fail(r.Size.Line, key+".size", "%q is not <width>x<height>", r.Size.V)
		} else {
			rule.Size = size
		}
	}
	switch {
	case r.Tearing == nil:
	case *r.Tearing:
		rule.Tearing = wm.TearingAllow
	default:
		rule.Tearing = wm.TearingDeny
	}
	if rule.AppID == nil && rule.Title == nil {
		p.fail(at.Line, key, "matches every window; give app_id or title")
	}
	return rule
}

func Parse(file string, data []byte) (Config, error) {
	p := &parser{file: file, palette: map[string]render.Color{}}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Config{}, fmt.Errorf("%s: %w", file, err)
	}
	p.unknownKeys(&doc, reflect.TypeFor[rawConfig](), "")
	if err := errors.Join(p.errs...); err != nil {
		return Config{}, err
	}
	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%s: %w", file, err)
	}
	maps.Copy(p.palette, mocha)
	for _, name := range slices.Sorted(maps.Keys(raw.Palette)) {
		a := raw.Palette[name]
		c, ok := parseHex(a.V)
		if !ok {
			p.fail(a.Line, "palette."+name, "%q is not #rrggbb[aa]", a.V)
		}
		p.palette[name] = c
	}
	def := compositor.DefaultDecor()
	cfg := Config{
		File: file,
		Keyboard: xkb.Names{
			Rules: raw.Input.Keyboard.Rules, Model: raw.Input.Keyboard.Model, Layout: raw.Input.Keyboard.Layout,
			Variant: raw.Input.Keyboard.Variant, Options: raw.Input.Keyboard.Options,
		},
		RepeatRate:  p.repeat(raw.Input.Keyboard.RepeatRate, "input.keyboard.repeat_rate", seat.DefaultRepeatRate),
		RepeatDelay: p.repeat(raw.Input.Keyboard.RepeatDelay, "input.keyboard.repeat_delay", seat.DefaultRepeatDelay),
		Input:       p.input(raw.Input),
		Decor: compositor.Decor{
			BorderWidth:    p.positive(raw.Borders.Width, "borders.width", def.BorderWidth, true),
			ActiveBorder:   p.optColor(raw.Borders.Active, "borders.active", def.ActiveBorder),
			InactiveBorder: p.optColor(raw.Borders.Inactive, "borders.inactive", def.InactiveBorder),
			Tab: compositor.TabStyle{
				Height:      p.positive(raw.Tabs.Height, "tabs.height", def.Tab.Height, false),
				Padding:     p.positive(raw.Tabs.Padding, "tabs.padding", def.Tab.Padding, true),
				TextHeight:  p.positive(raw.Tabs.TextHeight, "tabs.text_height", def.Tab.TextHeight, false),
				TextPadding: p.positive(raw.Tabs.TextPadding, "tabs.text_padding", def.Tab.TextPadding, true),
				Active:      p.tabColors(raw.Tabs.Active, "tabs.active", def.Tab.Active),
				Inactive:    p.tabColors(raw.Tabs.Inactive, "tabs.inactive", def.Tab.Inactive),
				Urgent:      p.tabColors(raw.Tabs.Urgent, "tabs.urgent", def.Tab.Urgent),
			},
		},
		WM:          compositor.DefaultSettings(),
		Xwayland:    raw.Xwayland.Enabled == nil || *raw.Xwayland.Enabled,
		ZeroScaling: raw.Xwayland.ZeroScaling == nil || *raw.Xwayland.ZeroScaling,
		Tearing:     raw.Tearing,
	}
	if raw.Tabs.Font != nil {
		cfg.Font = raw.Tabs.Font.V
	}
	if raw.GPU != nil {
		cfg.GPU = p.card(*raw.GPU)
	}
	at := &cfg.WM.Autotile
	if raw.Autotile.Enabled != nil {
		at.Enabled = *raw.Autotile.Enabled
	}
	if raw.Autotile.Ephemeral != nil {
		at.Ephemeral = *raw.Autotile.Ephemeral
	}
	at.MinTrigger = p.positive(raw.Autotile.MinTrigger, "autotile.min_trigger", at.MinTrigger, true)
	at.Divisor = p.positive(raw.Autotile.Divisor, "autotile.divisor", at.Divisor, false)
	for i, o := range raw.Outputs {
		cfg.Outputs = append(cfg.Outputs, p.output(i, o.V))
	}
	for i, b := range raw.Binds {
		cfg.Binds = append(cfg.Binds, p.bind("", i, b))
	}
	for _, name := range slices.Sorted(maps.Keys(raw.Submaps)) {
		for i, b := range raw.Submaps[name] {
			cfg.Binds = append(cfg.Binds, p.bind(name, i, b))
		}
	}
	for i, r := range raw.Rules {
		cfg.Rules = append(cfg.Rules, p.rule(i, r))
	}
	env := maps.Clone(sessionEnv)
	for _, name := range slices.Sorted(maps.Keys(raw.Env)) {
		value := raw.Env[name]
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value.V, 0) {
			p.fail(value.Line, "env."+name, "is not a valid environment variable")
			continue
		}
		if slices.Contains(reservedEnv, name) {
			p.fail(value.Line, "env."+name, "is set by tilde")
			continue
		}
		env[name] = value.V
	}
	for _, name := range slices.Sorted(maps.Keys(env)) {
		cfg.Env = append(cfg.Env, [2]string{name, env[name]})
	}
	for _, a := range raw.Autostart {
		cfg.Autostart = append(cfg.Autostart, a.V)
	}
	if err := errors.Join(p.errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) resolveBinds(r binds.Resolver) ([]binds.Bind, error) {
	out := make([]binds.Bind, 0, len(c.Binds))
	var errs []error
	for _, b := range c.Binds {
		chord, err := binds.ParseChord(b.Chord, r)
		if err != nil {
			errs = append(errs, &Error{File: c.File, Line: b.Line, Key: "chord", Err: err})
			continue
		}
		out = append(out, binds.Bind{Submap: b.Submap, Chord: chord, Flags: b.Flags, Action: b.Action})
	}
	return out, errors.Join(errs...)
}

func (c Config) Compositor(keymap *xkb.Keymap, font *gtext.Typeface) (compositor.Config, error) {
	b, err := c.resolveBinds(keymap)
	if err != nil {
		return compositor.Config{}, err
	}
	cfg := compositor.DefaultConfig(keymap, font)
	cfg.Seat.RepeatRate, cfg.Seat.RepeatDelay = c.RepeatRate, c.RepeatDelay
	cfg.WM, cfg.Binds, cfg.Decor, cfg.Rules, cfg.Outputs, cfg.Tearing = c.WM, b, c.Decor, c.Rules, c.Outputs, c.Tearing
	return cfg, nil
}
