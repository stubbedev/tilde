// Package ipc serves tilde's state, actions and events over a Unix socket as
// JSON lines, for tildectl, bars and scripts.
package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"

	"github.com/stubbedev/tilde/internal/wm"
)

const Version = 1

type Kind string

const (
	KindVersion      Kind = "version"
	KindOutputs      Kind = "outputs"
	KindWorkspaces   Kind = "workspaces"
	KindWindows      Kind = "windows"
	KindActiveWindow Kind = "activewindow"
	KindDispatch     Kind = "dispatch"
	KindWatch        Kind = "watch"
)

var kinds = []Kind{KindVersion, KindOutputs, KindWorkspaces, KindWindows, KindActiveWindow, KindDispatch, KindWatch}

type Request struct {
	Request Kind   `json:"request"`
	Action  string `json:"action,omitempty"`
}

type Response struct {
	Version int             `json:"version"`
	Error   string          `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

var ErrBadRequest = errors.New("ipc: bad request")

type request struct {
	kind   Kind
	action wm.Action
}

func parseRequest(line []byte) (request, error) {
	var raw Request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return request{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	if !slices.Contains(kinds, raw.Request) {
		return request{}, fmt.Errorf("%w: unknown request %q (known: %s)", ErrBadRequest, raw.Request, kindList())
	}
	if (raw.Request == KindDispatch) != (raw.Action != "") {
		return request{}, fmt.Errorf("%w: only dispatch takes an action, and it needs one", ErrBadRequest)
	}
	r := request{kind: raw.Request}
	if raw.Request == KindDispatch {
		a, err := wm.ParseAction(raw.Action)
		if err != nil {
			return request{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
		}
		r.action = a
	}
	return r, nil
}

func kindList() string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func RectOf(r image.Rectangle) Rect {
	return Rect{X: r.Min.X, Y: r.Min.Y, Width: r.Dx(), Height: r.Dy()}
}

type Mode struct {
	Width      int   `json:"width"`
	Height     int   `json:"height"`
	RefreshMHz int32 `json:"refresh_mhz"`
}

type Output struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Rect        Rect           `json:"rect"`
	Scale       float64        `json:"scale"`
	Mode        Mode           `json:"mode"`
	Workspace   wm.WorkspaceID `json:"workspace"`
	Focused     bool           `json:"focused"`
	Powered     bool           `json:"powered"`
}

type Workspace struct {
	ID      wm.WorkspaceID `json:"id"`
	Output  string         `json:"output"`
	Windows int            `json:"windows"`
	Visible bool           `json:"visible"`
	Focused bool           `json:"focused"`
}

type Window struct {
	ID           wm.WindowID    `json:"id"`
	AppID        string         `json:"app_id"`
	Title        string         `json:"title"`
	PID          int32          `json:"pid"`
	Workspace    wm.WorkspaceID `json:"workspace"`
	Output       string         `json:"output"`
	Rect         Rect           `json:"rect"`
	Floating     bool           `json:"floating"`
	Fullscreen   bool           `json:"fullscreen"`
	Urgent       bool           `json:"urgent"`
	Unresponsive bool           `json:"unresponsive"`
	Focused      bool           `json:"focused"`
}

type Layout struct {
	Index uint32 `json:"index"`
	Name  string `json:"name"`
}

type Snapshot struct {
	Outputs    []Output
	Workspaces []Workspace
	Windows    []Window
	Submap     string
	Layout     Layout
}

func (s Snapshot) active() (Window, bool) {
	i := slices.IndexFunc(s.Windows, func(w Window) bool { return w.Focused })
	if i < 0 {
		return Window{}, false
	}
	return s.Windows[i], true
}

func (s Snapshot) answer(k Kind) any {
	switch k {
	case KindVersion:
		return Version
	case KindOutputs:
		return nonNil(s.Outputs)
	case KindWorkspaces:
		return nonNil(s.Workspaces)
	case KindWindows:
		return nonNil(s.Windows)
	case KindActiveWindow:
		if w, ok := s.active(); ok {
			return w
		}
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
