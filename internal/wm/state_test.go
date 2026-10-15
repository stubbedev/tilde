package wm

import (
	"errors"
	"image"
	"slices"
	"testing"
)

var (
	left  = image.Rect(0, 0, 1920, 1080)
	right = image.Rect(1920, 0, 3840, 1080)
)

func ok(t *testing.T) func(State, error) State {
	return func(s State, err error) State {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func layoutOf(s State) map[WorkspaceID]string {
	out := map[WorkspaceID]string{}
	for _, w := range s.Workspaces() {
		out[w.ID] = w.Output
	}
	return out
}

func active(s State) []WorkspaceID {
	var out []WorkspaceID
	for _, o := range s.Outputs() {
		out = append(out, o.Active)
	}
	return out
}

func TestOutputsStartOnTheFirstFreeWorkspace(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.AddOutput("DP-2", right, right))
	if got := active(s); !slices.Equal(got, []WorkspaceID{1, 2}) || s.FocusedOutput() != "DP-1" {
		t.Fatalf("active %v focused %s", got, s.FocusedOutput())
	}
	if _, err := s.AddOutput("DP-1", left, left); err == nil {
		t.Fatal("an output was added twice")
	}
}

func TestSwitchingWorkspacesCreatesAndDropsThem(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.AddOutput("DP-2", right, right))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.SwitchWorkspace(3))
	if got := active(s); !slices.Equal(got, []WorkspaceID{3, 2}) {
		t.Fatalf("active %v after switching to 3", got)
	}
	if _, kept := s.Workspace(1); !kept {
		t.Fatal("workspace 1 with a window was dropped")
	}
	s = ok(t)(s.SwitchWorkspace(1))
	if _, kept := s.Workspace(3); kept {
		t.Fatal("the empty workspace 3 survived being left")
	}
	s = ok(t)(s.SwitchWorkspace(2))
	if s.FocusedOutput() != "DP-2" || !slices.Equal(active(s), []WorkspaceID{1, 2}) {
		t.Fatalf("switching to a workspace on another output: focused %s, active %v", s.FocusedOutput(), active(s))
	}
	if _, err := s.SwitchWorkspace(0); !errors.Is(err, ErrInvalidWorkspace) {
		t.Fatalf("workspace 0: %v", err)
	}
}

func TestRelativeWorkspacesWalkOpenOnesByID(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.AddOutput("DP-2", right, right))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.SwitchWorkspace(5))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	steps := []struct {
		offset int
		want   WorkspaceID
	}{{1, 1}, {1, 2}, {1, 5}, {-1, 2}, {-2, 5}}
	for _, st := range steps {
		s = ok(t)(s.RelativeWorkspace(st.offset))
		o := s.Outputs()
		at := slices.IndexFunc(o, func(x Output) bool { return x.Name == s.FocusedOutput() })
		if o[at].Active != st.want {
			t.Fatalf("e%+d landed on %d, want %d (workspaces %v)", st.offset, o[at].Active, st.want, layoutOf(s))
		}
	}
}

func TestMoveToWorkspaceKeepsFocusHere(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	s = ok(t)(s.MoveToWorkspace(4, Autotile{}))
	if w, _ := s.WorkspaceOf(2); w != 4 {
		t.Fatalf("window 2 on workspace %d, want 4", w)
	}
	if f, _ := s.Focused(); f != 1 || active(s)[0] != 1 {
		t.Fatalf("focus %d on workspace %v after moving 2 away", f, active(s))
	}
	a := s.Arrange(0)
	for _, p := range a.Windows {
		if p.ID == 2 && p.Visible {
			t.Fatal("a window on a hidden workspace is visible")
		}
		if p.ID == 1 && (!p.Visible || p.Rect != left) {
			t.Fatalf("window 1 placed %+v, want the whole output", p)
		}
	}
	s = ok(t)(s.FocusWindow(2))
	if active(s)[0] != 4 {
		t.Fatal("focusing a window did not show its workspace")
	}
}

func TestOutputsComeAndGo(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.AddOutput("HDMI-1", right, right))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.FocusOutput("HDMI-1"))
	s = ok(t)(s.MapWindow(2, Autotile{}))
	s = ok(t)(s.SwitchWorkspace(7))
	s = ok(t)(s.MapWindow(3, Autotile{}))
	s = ok(t)(s.FocusOutput("DP-1"))
	s = ok(t)(s.RemoveOutput("HDMI-1"))
	if got := layoutOf(s); got[2] != "DP-1" || got[7] != "DP-1" || got[1] != "DP-1" {
		t.Fatalf("after unplugging HDMI-1: %v", got)
	}
	s = ok(t)(s.AddOutput("HDMI-1", right, right))
	if got := layoutOf(s); got[2] != "HDMI-1" || got[7] != "HDMI-1" || got[1] != "DP-1" {
		t.Fatalf("after plugging HDMI-1 back: %v", got)
	}
	if got := active(s); !slices.Equal(got, []WorkspaceID{1, 7}) {
		t.Fatalf("active %v, want HDMI-1 back on 7", got)
	}
	s = ok(t)(s.RemoveOutput("HDMI-1"))
	s = ok(t)(s.RemoveOutput("DP-1"))
	s = ok(t)(s.MapWindow(4, Autotile{}))
	s = ok(t)(s.AddOutput("eDP-1", left, left))
	if got := len(s.Arrange(0).Windows); got != 4 {
		t.Fatalf("%d windows survive losing every output, want 4", got)
	}
	if _, err := s.RemoveOutput("nope"); !errors.Is(err, ErrUnknownOutput) {
		t.Fatalf("removing an unknown output: %v", err)
	}
}

func TestAssigningAnEmptyActiveWorkspaceAwayDropsIt(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.AddOutput("DP-2", right, right))
	s = ok(t)(s.AssignWorkspace(1, "DP-2"))
	if got := layoutOf(s); len(got) != 2 || got[1] != "" {
		t.Fatalf("workspaces %v: the empty workspace 1 is neither shown nor holding windows on DP-2", got)
	}
}

func TestAssigningAWorkspaceMovesItToAnotherOutput(t *testing.T) {
	s := ok(t)(State{}.AddOutput("DP-1", left, left))
	s = ok(t)(s.AddOutput("DP-2", right, right))
	s = ok(t)(s.MapWindow(1, Autotile{}))
	s = ok(t)(s.AssignWorkspace(1, "DP-2"))
	if got := layoutOf(s); got[1] != "DP-2" || got[3] != "DP-1" {
		t.Fatalf("workspaces %v after moving 1 to DP-2", got)
	}
	if got := active(s); !slices.Equal(got, []WorkspaceID{3, 2}) {
		t.Fatalf("active %v: DP-1 needs a fresh workspace and DP-2 keeps its own", got)
	}
	s = ok(t)(s.SwitchWorkspace(1))
	if got := active(s); !slices.Equal(got, []WorkspaceID{3, 1}) || s.FocusedOutput() != "DP-2" {
		t.Fatalf("active %v focused %s after activating the moved workspace", got, s.FocusedOutput())
	}
	if same := ok(t)(s.AssignWorkspace(1, "DP-2")); !slices.Equal(active(same), active(s)) {
		t.Fatal("assigning a workspace to its own output changed something")
	}
	if _, err := s.AssignWorkspace(9, "DP-1"); !errors.Is(err, ErrInvalidWorkspace) {
		t.Fatalf("an unknown workspace: %v", err)
	}
	if _, err := s.AssignWorkspace(1, "HDMI-1"); !errors.Is(err, ErrUnknownOutput) {
		t.Fatalf("an unknown output: %v", err)
	}
}
