package wm

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite docs/actions.md")

var everyAction = []Action{
	FocusAction{Dir: Left},
	FocusAction{Dir: Down},
	MoveAction{Dir: Right},
	MoveAction{Dir: Up},
	WorkspaceAction{Target: WorkspaceTarget{ID: 3}},
	WorkspaceAction{Target: WorkspaceTarget{Relative: 1}},
	WorkspaceAction{Target: WorkspaceTarget{Relative: -2}},
	MoveToWorkspaceAction{Target: WorkspaceTarget{ID: 10}},
	MoveToWorkspaceAction{Target: WorkspaceTarget{Relative: -1}},
	KillAction{},
	ExecAction{Command: "foot -e sh -c 'echo hi'"},
	MakeGroupAction{Layout: SplitV},
	MakeGroupAction{Layout: Tabbed, Ephemeral: true},
	SubmapAction{Name: "resize"},
	SubmapAction{},
}

func TestEveryActionRoundTripsThroughTheParser(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range everyAction {
		got, err := ParseAction(a.String())
		if err != nil {
			t.Fatalf("parse %q: %v", a, err)
		}
		if got != a {
			t.Fatalf("%q parsed to %#v, want %#v", a, got, a)
		}
		name, _, _ := strings.Cut(a.String(), " ")
		seen[name] = true
	}
	for _, spec := range Actions {
		if !seen[spec.Name] {
			t.Errorf("action %s has no round-trip case", spec.Name)
		}
	}
}

func TestBadActionsFailPrecisely(t *testing.T) {
	cases := map[string]string{
		"":                        `unknown action ""`,
		"teleport":                `unknown action "teleport"`,
		"focus":                   "focus <left|right|up|down>: takes 1 argument(s), got 0",
		"focus sideways":          `focus <left|right|up|down>: unknown direction "sideways"`,
		"workspace 0":             `workspace <n|e+n|e-n>: unknown workspace "0"`,
		"workspace e+0":           `workspace <n|e+n|e-n>: unknown workspace "e+0"`,
		"kill now":                "kill: takes 0 argument(s), got 1",
		"exec":                    "exec <command>: needs a command",
		"makegroup diagonal":      `makegroup <h|v|tab> [ephemeral]: unknown layout "diagonal"`,
		"makegroup v forevermore": `makegroup <h|v|tab> [ephemeral]: unknown option "forevermore"`,
	}
	for text, want := range cases {
		_, err := ParseAction(text)
		if !errors.Is(err, ErrBadAction) || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseAction(%q) = %v, want an error containing %q", text, err, want)
		}
	}
}

func TestDispatch(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left))
	s = ok(t)(s.MapWindow(1))
	s = ok(t)(s.MapWindow(2))
	settings := Settings{Collapse: CollapseEmptySplits}
	run := func(text string) []Effect {
		t.Helper()
		a, err := ParseAction(text)
		if err != nil {
			t.Fatal(err)
		}
		var effects []Effect
		if s, effects, err = Dispatch(s, a, settings); err != nil {
			t.Fatal(err)
		}
		return effects
	}
	run("focus left")
	if f, _ := s.Focused(); f != 1 {
		t.Fatalf("focus left landed on %d", f)
	}
	run("move right")
	if got := s.workspace(1).Tree.String(); got != "h[2 *1]" {
		t.Fatalf("after move right: %s", got)
	}
	run("movetoworkspace e+1")
	if w, _ := s.WorkspaceOf(1); w != 1 {
		t.Fatalf("e+1 with one open workspace wraps to itself, window went to %d", w)
	}
	run("movetoworkspace 2")
	run("workspace 2")
	if f, _ := s.Focused(); f != 1 || active(s)[0] != 2 {
		t.Fatalf("workspace 2 shows %v, focus %d", active(s), f)
	}
	if effects := run("kill"); !slices.Equal(effects, []Effect{Close{ID: 1}}) {
		t.Fatalf("kill gave %v", effects)
	}
	if effects := run("exec foot --server"); !slices.Equal(effects, []Effect{Exec{Command: "foot --server"}}) {
		t.Fatalf("exec gave %v", effects)
	}
	run("makegroup tab ephemeral")
	if got := s.workspace(2).Tree.String(); got != "h[*tab~[*1]]" {
		t.Fatalf("after makegroup: %s", got)
	}
	if _, _, err := Dispatch(State{}, WorkspaceAction{Target: WorkspaceTarget{Relative: 1}}, settings); !errors.Is(err, ErrUnknownOutput) {
		t.Fatalf("a relative workspace with no output: %v", err)
	}
}

func TestActionReferenceIsCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "actions.md")
	if *update {
		if err := os.WriteFile(path, []byte(Reference()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	have, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(have) != Reference() {
		t.Fatalf("%s is stale; run go test ./internal/wm -update", path)
	}
}
