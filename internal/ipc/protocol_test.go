package ipc

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/wm"
)

func TestParseRequest(t *testing.T) {
	r, err := parseRequest([]byte(`{"request":"dispatch","action":"workspace 3"}`))
	if err != nil || r.kind != KindDispatch || r.action != (wm.WorkspaceAction{Target: wm.WorkspaceTarget{ID: 3}}) {
		t.Fatalf("dispatch parsed to %+v, %v", r, err)
	}
	if r, err := parseRequest([]byte(`{"request":"windows"}` + "\n")); err != nil || r.kind != KindWindows {
		t.Fatalf("windows parsed to %+v, %v", r, err)
	}
	for text, want := range map[string]string{
		`{"request":"teleport"}`:                   `unknown request "teleport" (known: version, outputs,`,
		`{"request":"windows","action":"kill"}`:    "only dispatch takes an action",
		`{"request":"dispatch"}`:                   "only dispatch takes an action",
		`{"request":"dispatch","action":"fly up"}`: `unknown action "fly"`,
		`{"request":"windows","verbose":true}`:     `unknown field "verbose"`,
		`not json`:                                 "invalid character",
	} {
		_, err := parseRequest([]byte(text))
		if !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", text, err, want)
		}
	}
}

func TestEncodeFlattensTheEventName(t *testing.T) {
	id := wm.WindowID(4)
	for e, want := range map[Event]string{
		FocusEvent{Window: &id}: `{"event":"focus","window":4}`,
		FocusEvent{}:            `{"event":"focus","window":null}`,
		WorkspaceEvent{Output: "DP-1", Workspace: 2}:          `{"event":"workspace","output":"DP-1","workspace":2}`,
		DispatchEvent{Action: "exec foot"}:                    `{"event":"dispatch","action":"exec foot"}`,
		LayoutEvent{Layout: Layout{Index: 1, Name: "Danish"}}: `{"event":"layout","layout":{"index":1,"name":"Danish"}}`,
	} {
		got, err := Encode(e)
		if err != nil || string(got) != want+"\n" {
			t.Errorf("%T: %s, %v; want %s", e, got, err, want)
		}
	}
}

func names(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		line, err := Encode(e)
		if err != nil {
			panic(err)
		}
		out[i] = strings.TrimSpace(string(line))
	}
	return out
}

func TestDiff(t *testing.T) {
	dp1 := Output{Name: "DP-1", Rect: Rect{Width: 100, Height: 50}, Scale: 1, Workspace: 1, Focused: true}
	hdmi := Output{Name: "HDMI-A-1", Rect: Rect{X: 100, Width: 100, Height: 50}, Scale: 1, Workspace: 2}
	a := Window{ID: 1, AppID: "foot", Title: "a", Workspace: 1, Output: "DP-1", Focused: true}
	b := Window{ID: 2, AppID: "foot", Title: "b", Workspace: 2, Output: "HDMI-A-1"}
	prev := Snapshot{Outputs: []Output{dp1, hdmi}, Windows: []Window{a, b}, Layout: Layout{Name: "English (US)"}}
	if got := Diff(prev, prev); len(got) != 0 {
		t.Fatalf("an unchanged snapshot gave %v", names(got))
	}

	next := prev
	dp1b, hdmib := dp1, hdmi
	dp1b.Focused, dp1b.Scale = false, 2
	hdmib.Focused, hdmib.Workspace = true, 3
	next.Outputs = []Output{dp1b, hdmib}
	a2, b2 := a, b
	a2.Focused, a2.Title, a2.Workspace = false, "a!", 3
	b2.Urgent = true
	c := Window{ID: 3, AppID: "mpv", Workspace: 3, Output: "HDMI-A-1", Focused: true}
	next.Windows = []Window{b2, a2, c}
	next.Submap = "resize"
	next.Layout = Layout{Index: 1, Name: "Danish"}
	want := []string{
		`{"event":"output","change":"changed","output":{"name":"DP-1","description":"","rect":{"x":0,"y":0,"width":100,"height":50},"scale":2,"mode":{"width":0,"height":0,"refresh_mhz":0},"workspace":1,"focused":false,"powered":false}}`,
		`{"event":"workspace","output":"HDMI-A-1","workspace":3}`,
		`{"event":"focusedoutput","output":"HDMI-A-1"}`,
		`{"event":"urgent","window":2,"urgent":true}`,
		`{"event":"move","window":1,"workspace":3}`,
		`{"event":"title","window":1,"title":"a!"}`,
		`{"event":"open","window":{"id":3,"app_id":"mpv","title":"","pid":0,"workspace":3,"output":"HDMI-A-1","rect":{"x":0,"y":0,"width":0,"height":0},"floating":false,"fullscreen":false,"urgent":false,"unresponsive":false,"focused":true}}`,
		`{"event":"focus","window":3}`,
		`{"event":"submap","submap":"resize"}`,
		`{"event":"layout","layout":{"index":1,"name":"Danish"}}`,
	}
	if got := names(Diff(prev, next)); !slices.Equal(got, want) {
		t.Fatalf("diff\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}

	gone := Snapshot{Outputs: []Output{dp1}}
	want = []string{
		`{"event":"output","change":"removed","output":{"name":"HDMI-A-1","description":"","rect":{"x":100,"y":0,"width":100,"height":50},"scale":1,"mode":{"width":0,"height":0,"refresh_mhz":0},"workspace":2,"focused":false,"powered":false}}`,
		`{"event":"close","window":1}`,
		`{"event":"close","window":2}`,
		`{"event":"focus","window":null}`,
		`{"event":"layout","layout":{"index":0,"name":""}}`,
	}
	if got := names(Diff(prev, gone)); !slices.Equal(got, want) {
		t.Fatalf("diff\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

func TestEveryOutputFieldButWorkspaceAndFocusCountsAsAChange(t *testing.T) {
	base := Output{Name: "DP-1", Description: "d", Rect: Rect{Width: 10, Height: 10}, Scale: 1, Mode: Mode{Width: 10, Height: 10, RefreshMHz: 60000}, Workspace: 1, Focused: true, Powered: true}
	changed := func(f func(*Output)) bool {
		o := base
		f(&o)
		return !sameOutput(base, o)
	}
	for name, f := range map[string]func(*Output){
		"description": func(o *Output) { o.Description = "e" },
		"rect":        func(o *Output) { o.Rect.X = 5 },
		"scale":       func(o *Output) { o.Scale = 2 },
		"mode":        func(o *Output) { o.Mode.RefreshMHz = 144000 },
		"powered":     func(o *Output) { o.Powered = false },
	} {
		if !changed(f) {
			t.Errorf("a different %s was not a change", name)
		}
	}
	if changed(func(o *Output) { o.Workspace, o.Focused = 2, false }) {
		t.Error("the workspace and focus, which have their own events, counted as an output change")
	}
}
