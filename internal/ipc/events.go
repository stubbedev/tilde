package ipc

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/stubbedev/tilde/internal/wm"
)

type Event interface {
	Name() string
}

type OutputChange string

const (
	OutputAdded   OutputChange = "added"
	OutputRemoved OutputChange = "removed"
	OutputChanged OutputChange = "changed"
)

type (
	OutputEvent struct {
		Change OutputChange `json:"change"`
		Output Output       `json:"output"`
	}
	WorkspaceEvent struct {
		Output    string         `json:"output"`
		Workspace wm.WorkspaceID `json:"workspace"`
	}
	FocusedOutputEvent struct {
		Output string `json:"output"`
	}
	OpenEvent struct {
		Window Window `json:"window"`
	}
	CloseEvent struct {
		Window wm.WindowID `json:"window"`
	}
	MoveEvent struct {
		Window    wm.WindowID    `json:"window"`
		Workspace wm.WorkspaceID `json:"workspace"`
	}
	TitleEvent struct {
		Window wm.WindowID `json:"window"`
		Title  string      `json:"title"`
	}
	UrgentEvent struct {
		Window wm.WindowID `json:"window"`
		Urgent bool        `json:"urgent"`
	}
	FocusEvent struct {
		Window *wm.WindowID `json:"window"`
	}
	SubmapEvent struct {
		Submap string `json:"submap"`
	}
	LayoutEvent struct {
		Layout Layout `json:"layout"`
	}
	DispatchEvent struct {
		Action string `json:"action"`
	}
)

func (OutputEvent) Name() string        { return "output" }
func (WorkspaceEvent) Name() string     { return "workspace" }
func (FocusedOutputEvent) Name() string { return "focusedoutput" }
func (OpenEvent) Name() string          { return "open" }
func (CloseEvent) Name() string         { return "close" }
func (MoveEvent) Name() string          { return "move" }
func (TitleEvent) Name() string         { return "title" }
func (UrgentEvent) Name() string        { return "urgent" }
func (FocusEvent) Name() string         { return "focus" }
func (SubmapEvent) Name() string        { return "submap" }
func (LayoutEvent) Name() string        { return "layout" }
func (DispatchEvent) Name() string      { return "dispatch" }

func Encode(e Event) ([]byte, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("ipc: encode %s event: %w", e.Name(), err)
	}
	line := fmt.Appendf(nil, `{"event":%q`, e.Name())
	if len(body) > 2 {
		line = append(line, ',')
	}
	line = append(line, body[1:]...)
	return append(line, '\n'), nil
}

func sameOutput(a, b Output) bool {
	return a.Description == b.Description && a.Rect == b.Rect && a.Scale == b.Scale && a.Mode == b.Mode
}

func focusedOutput(s Snapshot) string {
	if i := slices.IndexFunc(s.Outputs, func(o Output) bool { return o.Focused }); i >= 0 {
		return s.Outputs[i].Name
	}
	return ""
}

func Diff(prev, next Snapshot) []Event {
	var out []Event
	prevOutputs := make(map[string]Output, len(prev.Outputs))
	for _, o := range prev.Outputs {
		prevOutputs[o.Name] = o
	}
	for _, o := range prev.Outputs {
		if !slices.ContainsFunc(next.Outputs, func(n Output) bool { return n.Name == o.Name }) {
			out = append(out, OutputEvent{Change: OutputRemoved, Output: o})
		}
	}
	for _, o := range next.Outputs {
		p, ok := prevOutputs[o.Name]
		switch {
		case !ok:
			out = append(out, OutputEvent{Change: OutputAdded, Output: o})
		case !sameOutput(p, o):
			out = append(out, OutputEvent{Change: OutputChanged, Output: o})
		}
		if !ok || p.Workspace != o.Workspace {
			out = append(out, WorkspaceEvent{Output: o.Name, Workspace: o.Workspace})
		}
	}
	if f := focusedOutput(next); f != "" && f != focusedOutput(prev) {
		out = append(out, FocusedOutputEvent{Output: f})
	}
	prevWindows := make(map[wm.WindowID]Window, len(prev.Windows))
	for _, w := range prev.Windows {
		prevWindows[w.ID] = w
	}
	nextWindows := make(map[wm.WindowID]bool, len(next.Windows))
	for _, w := range next.Windows {
		nextWindows[w.ID] = true
	}
	for _, w := range prev.Windows {
		if !nextWindows[w.ID] {
			out = append(out, CloseEvent{Window: w.ID})
		}
	}
	for _, w := range next.Windows {
		p, ok := prevWindows[w.ID]
		if !ok {
			out = append(out, OpenEvent{Window: w})
			continue
		}
		if p.Workspace != w.Workspace {
			out = append(out, MoveEvent{Window: w.ID, Workspace: w.Workspace})
		}
		if p.Title != w.Title {
			out = append(out, TitleEvent{Window: w.ID, Title: w.Title})
		}
		if p.Urgent != w.Urgent {
			out = append(out, UrgentEvent{Window: w.ID, Urgent: w.Urgent})
		}
	}
	was, hadFocus := prev.active()
	is, hasFocus := next.active()
	if hadFocus != hasFocus || was.ID != is.ID {
		e := FocusEvent{}
		if hasFocus {
			e.Window = &is.ID
		}
		out = append(out, e)
	}
	if prev.Submap != next.Submap {
		out = append(out, SubmapEvent{Submap: next.Submap})
	}
	if prev.Layout != next.Layout {
		out = append(out, LayoutEvent{Layout: next.Layout})
	}
	return out
}
