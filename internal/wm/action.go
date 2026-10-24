package wm

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Action interface {
	fmt.Stringer
	apply(s State, settings Settings) (State, []Effect, error)
}

type Effect interface {
	isEffect()
}

type DragMode uint8

const (
	DragMove DragMode = iota + 1
	DragResize
)

type Close struct{ ID WindowID }

type PowerMode uint8

const (
	PowerOn PowerMode = iota + 1
	PowerOff
	PowerToggle
)

var powerModes = []string{PowerOn: "on", PowerOff: "off", PowerToggle: "toggle"}

func (Close) isEffect()        {}
func (ExecAction) isEffect()   {}
func (SubmapAction) isEffect() {}
func (DragAction) isEffect()   {}
func (ReloadAction) isEffect() {}
func (ExitAction) isEffect()   {}
func (GlobalAction) isEffect() {}
func (DPMSAction) isEffect()   {}

func emit(s State, e Effect) (State, []Effect, error) {
	return s, []Effect{e}, nil
}

type Settings struct {
	Collapse CollapsePolicy
	Autotile Autotile
}

type WorkspaceTarget interface {
	fmt.Stringer
	resolve(s State) (WorkspaceID, error)
}

type RelativeWorkspace int

func (id WorkspaceID) String() string {
	return strconv.Itoa(int(id))
}

func (id WorkspaceID) resolve(State) (WorkspaceID, error) {
	return id, nil
}

func (r RelativeWorkspace) String() string {
	return fmt.Sprintf("e%+d", int(r))
}

func (r RelativeWorkspace) resolve(s State) (WorkspaceID, error) {
	return s.relative(int(r))
}

type (
	FocusAction           struct{ Dir Direction }
	MoveAction            struct{ Dir Direction }
	WorkspaceAction       struct{ Target WorkspaceTarget }
	MoveToWorkspaceAction struct{ Target WorkspaceTarget }
	KillAction            struct{}
	ExecAction            struct{ Command string }
	MakeGroupAction       struct {
		Layout    Layout
		Ephemeral bool
	}
	SubmapAction     struct{ Name string }
	FloatAction      struct{}
	FullscreenAction struct{}
	DragAction       struct{ Mode DragMode }
	ResizeAction     struct{ DX, DY int }
	ReloadAction     struct{}
	ExitAction       struct{}
	GlobalAction     struct{ AppID, ID string }
	DPMSAction       struct {
		Mode   PowerMode
		Output string
	}
)

func (m PowerMode) String() string {
	return enumName(powerModes, m, "PowerMode")
}

func (a DPMSAction) String() string {
	if a.Output == "" {
		return "dpms " + a.Mode.String()
	}
	return "dpms " + a.Mode.String() + " " + a.Output
}

func (a DPMSAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a GlobalAction) String() string { return "global " + a.AppID + ":" + a.ID }

func (a GlobalAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (ReloadAction) String() string { return "reload" }

func (ExitAction) String() string { return "exit" }

func (a ExitAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a ReloadAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a ResizeAction) String() string { return fmt.Sprintf("resizeactive %d %d", a.DX, a.DY) }

func (a ResizeAction) apply(s State, _ Settings) (State, []Effect, error) {
	next, err := s.ResizeFocused(a.DX, a.DY)
	return next, nil, err
}

func (FloatAction) String() string      { return "togglefloating" }
func (FullscreenAction) String() string { return "fullscreen" }
func (a DragAction) String() string {
	if a.Mode == DragResize {
		return "resizewindow"
	}
	return "movewindow"
}

func (FloatAction) apply(s State, settings Settings) (State, []Effect, error) {
	next, err := s.ToggleFloating(settings.Autotile)
	return next, nil, err
}

func (FullscreenAction) apply(s State, _ Settings) (State, []Effect, error) {
	next, err := s.ToggleFullscreen()
	return next, nil, err
}

func (a DragAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a FocusAction) String() string     { return "focus " + a.Dir.String() }
func (a MoveAction) String() string      { return "move " + a.Dir.String() }
func (a WorkspaceAction) String() string { return "workspace " + a.Target.String() }
func (a MoveToWorkspaceAction) String() string {
	return "movetoworkspace " + a.Target.String()
}
func (KillAction) String() string   { return "kill" }
func (a ExecAction) String() string { return "exec " + a.Command }
func (a MakeGroupAction) String() string {
	if a.Ephemeral {
		return "makegroup " + a.Layout.String() + " ephemeral"
	}
	return "makegroup " + a.Layout.String()
}

func (a SubmapAction) String() string {
	if a.Name == "" {
		return "submap reset"
	}
	return "submap " + a.Name
}

func (a SubmapAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a FocusAction) apply(s State, _ Settings) (State, []Effect, error) {
	next, err := s.FocusDirection(a.Dir)
	return next, nil, err
}

func (a MoveAction) apply(s State, settings Settings) (State, []Effect, error) {
	next, err := s.MoveDirection(a.Dir, settings.Collapse)
	return next, nil, err
}

func (a WorkspaceAction) apply(s State, _ Settings) (State, []Effect, error) {
	id, err := a.Target.resolve(s)
	if err != nil {
		return State{}, nil, err
	}
	next, err := s.SwitchWorkspace(id)
	return next, nil, err
}

func (a MoveToWorkspaceAction) apply(s State, settings Settings) (State, []Effect, error) {
	id, err := a.Target.resolve(s)
	if err != nil {
		return State{}, nil, err
	}
	next, err := s.MoveToWorkspace(id, settings.Autotile)
	return next, nil, err
}

func (KillAction) apply(s State, _ Settings) (State, []Effect, error) {
	id, ok := s.Focused()
	if !ok {
		return s, nil, nil
	}
	return s, []Effect{Close{ID: id}}, nil
}

func (a ExecAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func (a MakeGroupAction) apply(s State, _ Settings) (State, []Effect, error) {
	w := s.activeWorkspace()
	if w == nil {
		return s, nil, nil
	}
	id, ok := w.Tree.Focused()
	if !ok {
		return s, nil, nil
	}
	tree, err := w.Tree.Wrap(id, a.Layout, a.Ephemeral)
	if err != nil {
		return State{}, nil, err
	}
	s = s.clone()
	s.activeWorkspace().Tree = tree
	return s, nil, nil
}

func Dispatch(s State, a Action, settings Settings) (State, []Effect, error) {
	next, effects, err := a.apply(s, settings)
	if err != nil {
		return State{}, nil, fmt.Errorf("wm: %s: %w", a, err)
	}
	return next, effects, nil
}

type ActionSpec struct {
	Name    string
	Args    string
	Summary string
	parse   func(spec ActionSpec, args []string, rest string) (Action, error)
}

var ErrBadAction = errors.New("bad action")

func (spec ActionSpec) Usage() string {
	if spec.Args == "" {
		return spec.Name
	}
	return spec.Name + " " + spec.Args
}

func badArgs(spec ActionSpec, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrBadAction, spec.Usage(), fmt.Sprintf(format, args...))
}

func parseDirection(s string) (Direction, bool) {
	if d, ok := parseEnum[Direction](directionNames, s); ok {
		return d, true
	}
	for d, name := range directionNames {
		if name != "" && s == name[:1] {
			return Direction(d), true
		}
	}
	return 0, false
}

func parseWorkspace(s string) (WorkspaceTarget, bool) {
	if rest, ok := strings.CutPrefix(s, "e"); ok && len(rest) > 1 && (rest[0] == '+' || rest[0] == '-') {
		n, err := strconv.Atoi(rest)
		return RelativeWorkspace(n), err == nil && n != 0
	}
	n, err := strconv.Atoi(s)
	return WorkspaceID(n), err == nil && n >= 1
}

func parseLayout(s string) (Layout, bool) {
	return parseEnum[Layout](layoutNames, s)
}

func exactly(spec ActionSpec, args []string, n int) error {
	if len(args) != n {
		return badArgs(spec, "takes %d argument(s), got %d", n, len(args))
	}
	return nil
}

func directional(build func(Direction) Action) func(ActionSpec, []string, string) (Action, error) {
	return func(spec ActionSpec, args []string, _ string) (Action, error) {
		if err := exactly(spec, args, 1); err != nil {
			return nil, err
		}
		d, ok := parseDirection(args[0])
		if !ok {
			return nil, badArgs(spec, "unknown direction %q", args[0])
		}
		return build(d), nil
	}
}

func workspaced(build func(WorkspaceTarget) Action) func(ActionSpec, []string, string) (Action, error) {
	return func(spec ActionSpec, args []string, _ string) (Action, error) {
		if err := exactly(spec, args, 1); err != nil {
			return nil, err
		}
		w, ok := parseWorkspace(args[0])
		if !ok {
			return nil, badArgs(spec, "unknown workspace %q", args[0])
		}
		return build(w), nil
	}
}

var Actions = []ActionSpec{
	{
		Name: "focus", Args: "<left|right|up|down>",
		Summary: "Move focus to the neighbouring window, or to the next output at the edge.",
		parse:   directional(func(d Direction) Action { return FocusAction{Dir: d} }),
	},
	{
		Name: "move", Args: "<left|right|up|down>",
		Summary: "Move the focused window, hy3 style; at the workspace edge nothing happens.",
		parse:   directional(func(d Direction) Action { return MoveAction{Dir: d} }),
	},
	{
		Name: "workspace", Args: "<n|e+n|e-n>",
		Summary: "Show workspace n, or the n-th open workspace after or before the current one.",
		parse:   workspaced(func(w WorkspaceTarget) Action { return WorkspaceAction{Target: w} }),
	},
	{
		Name: "movetoworkspace", Args: "<n|e+n|e-n>",
		Summary: "Move the focused window to a workspace without following it.",
		parse:   workspaced(func(w WorkspaceTarget) Action { return MoveToWorkspaceAction{Target: w} }),
	},
	{
		Name:    "kill",
		Summary: "Ask the focused window to close.",
		parse:   bare(KillAction{}),
	},
	{
		Name: "dpms", Args: "<on|off|toggle> [output]",
		Summary: "Turn outputs on or off, or one output by name; off outputs keep their windows and wake only on dpms on.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if len(args) == 0 || len(args) > 2 {
				return nil, badArgs(spec, "needs on, off or toggle and an optional output")
			}
			mode, ok := parseEnum[PowerMode](powerModes, args[0])
			if !ok {
				return nil, badArgs(spec, "%q is not on, off or toggle", args[0])
			}
			a := DPMSAction{Mode: mode}
			if len(args) == 2 {
				a.Output = args[1]
			}
			return a, nil
		},
	},
	{
		Name:    "reload",
		Summary: "Read the config file again; a bad file keeps the running config and reports the error.",
		parse:   bare(ReloadAction{}),
	},
	{
		Name:    "exit",
		Summary: "End the session: lys-wm closes its clients, Xwayland and IPC socket and exits, as on SIGTERM.",
		parse:   bare(ExitAction{}),
	},
	{
		Name: "global", Args: "<app_id>:<id>",
		Summary: "Press the global shortcut an application registered, and release it with the bind.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if err := exactly(spec, args, 1); err != nil {
				return nil, err
			}
			app, id, ok := strings.Cut(args[0], ":")
			if !ok || app == "" || id == "" {
				return nil, badArgs(spec, "%q is not app_id:id", args[0])
			}
			return GlobalAction{AppID: app, ID: id}, nil
		},
	},
	{
		Name: "exec", Args: "<command>",
		Summary: "Run a shell command with sh -c.",
		parse: func(spec ActionSpec, _ []string, rest string) (Action, error) {
			if rest == "" {
				return nil, badArgs(spec, "needs a command")
			}
			return ExecAction{Command: rest}, nil
		},
	},
	{
		Name: "makegroup", Args: "<h|v|tab> [ephemeral]",
		Summary: "Wrap the focused window in a new group; an ephemeral group dissolves when one window is left.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if len(args) == 0 || len(args) > 2 {
				return nil, badArgs(spec, "takes 1 or 2 arguments, got %d", len(args))
			}
			l, ok := parseLayout(args[0])
			if !ok {
				return nil, badArgs(spec, "unknown layout %q", args[0])
			}
			if len(args) == 2 && args[1] != "ephemeral" {
				return nil, badArgs(spec, "unknown option %q", args[1])
			}
			return MakeGroupAction{Layout: l, Ephemeral: len(args) == 2}, nil
		},
	},
	{
		Name: "submap", Args: "<name|reset>",
		Summary: "Switch binds to the named submap, or back to the default ones.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if err := exactly(spec, args, 1); err != nil {
				return nil, err
			}
			if args[0] == "reset" {
				return SubmapAction{}, nil
			}
			return SubmapAction{Name: args[0]}, nil
		},
	},
	{
		Name:    "togglefloating",
		Summary: "Float the focused window centred at half the output, or tile it again.",
		parse:   bare(FloatAction{}),
	},
	{
		Name:    "fullscreen",
		Summary: "Make the focused window cover its output, above the top layer, or end that.",
		parse:   bare(FullscreenAction{}),
	},
	{
		Name: "resizeactive", Args: "<dx> <dy>",
		Summary: "Grow or shrink the focused window by pixels; tiled windows move their right or bottom edge, else their left or top one.",
		parse: func(spec ActionSpec, args []string, _ string) (Action, error) {
			if err := exactly(spec, args, 2); err != nil {
				return nil, err
			}
			dx, errX := strconv.Atoi(args[0])
			dy, errY := strconv.Atoi(args[1])
			if errX != nil || errY != nil {
				return nil, badArgs(spec, "%q and %q are not whole pixels", args[0], args[1])
			}
			return ResizeAction{DX: dx, DY: dy}, nil
		},
	},
	{
		Name:    "movewindow",
		Summary: "Mouse binds only: drag the window under the pointer; tiled windows drop into the tree where released.",
		parse:   bare(DragAction{Mode: DragMove}),
	},
	{
		Name:    "resizewindow",
		Summary: "Mouse binds only: resize the window under the pointer from its nearest corner; tiled windows change their split ratios.",
		parse:   bare(DragAction{Mode: DragResize}),
	},
}

func bare(a Action) func(ActionSpec, []string, string) (Action, error) {
	return func(spec ActionSpec, args []string, _ string) (Action, error) {
		if err := exactly(spec, args, 0); err != nil {
			return nil, err
		}
		return a, nil
	}
}

func ParseAction(text string) (Action, error) {
	text = strings.TrimSpace(text)
	name, rest, _ := strings.Cut(text, " ")
	rest = strings.TrimSpace(rest)
	for _, spec := range Actions {
		if spec.Name == name {
			return spec.parse(spec, strings.Fields(rest), rest)
		}
	}
	return nil, fmt.Errorf("%w: unknown action %q", ErrBadAction, name)
}

func Reference() string {
	var b strings.Builder
	b.WriteString("# Actions\n\nEvery keybind and `lys-wmctl dispatch` takes one of these. This file is generated from `wm.Actions`; run `go test ./internal/wm -update` after changing them.\n\n| Action | Arguments | Effect |\n| --- | --- | --- |\n")
	for _, spec := range Actions {
		args := spec.Args
		if args != "" {
			args = "`" + strings.ReplaceAll(args, "|", `\|`) + "`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", spec.Name, args, spec.Summary)
	}
	return b.String()
}
