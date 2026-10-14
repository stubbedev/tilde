package compositor

import (
	"image"
	"slices"
	"testing"

	swlr "github.com/stubbedev/tilde/internal/protocol/server/wlrlayershell"
	"github.com/stubbedev/tilde/internal/shell"
)

const (
	top    = swlr.ZwlrLayerSurfaceV1AnchorTop
	bottom = swlr.ZwlrLayerSurfaceV1AnchorBottom
	left   = swlr.ZwlrLayerSurfaceV1AnchorLeft
	right  = swlr.ZwlrLayerSurfaceV1AnchorRight
)

func TestArrangeLayers(t *testing.T) {
	full := image.Rect(0, 0, 1000, 600)
	bar := shell.LayerState{Layer: swlr.ZwlrLayerShellV1LayerTop, Size: image.Pt(0, 30), Anchor: top | left | right, ExclusiveZone: 30, Margin: shell.Margin{Top: 5, Left: 10, Right: 10}}
	dock := shell.LayerState{Layer: swlr.ZwlrLayerShellV1LayerBottom, Size: image.Pt(400, 50), Anchor: bottom, ExclusiveZone: 50}
	side := shell.LayerState{Layer: swlr.ZwlrLayerShellV1LayerTop, Size: image.Pt(40, 0), Anchor: left | top | bottom, ExclusiveZone: 40}
	note := shell.LayerState{Layer: swlr.ZwlrLayerShellV1LayerOverlay, Size: image.Pt(200, 80), Anchor: top | right, Margin: shell.Margin{Top: 4, Right: 6}}
	wallpaper := shell.LayerState{Layer: swlr.ZwlrLayerShellV1LayerBackground, Anchor: top | bottom | left | right, ExclusiveZone: -1}
	centred := shell.LayerState{Layer: swlr.ZwlrLayerShellV1LayerOverlay, Size: image.Pt(301, 101)}
	corner := shell.LayerState{Layer: swlr.ZwlrLayerShellV1LayerTop, Size: image.Pt(60, 60), Anchor: bottom | right, ExclusiveZone: 60, ExclusiveEdge: right}
	broken := shell.LayerState{Layer: swlr.ZwlrLayerShellV1LayerTop, Anchor: top | bottom | left | right, Margin: shell.Margin{Left: 600, Right: 600}}

	got, usable := arrangeLayers(full, []shell.LayerState{note, bar, dock, side, wallpaper, centred, corner, broken})
	want := []arranged{
		{image.Rect(734, 39, 934, 119), true},
		{image.Rect(10, 5, 990, 35), true},
		{image.Rect(300, 550, 700, 600), true},
		{image.Rect(0, 35, 40, 550), true},
		{image.Rect(0, 0, 1000, 600), true},
		{image.Rect(340, 242, 641, 343), true},
		{image.Rect(940, 490, 1000, 550), true},
		{},
	}
	if !slices.Equal(got, want) {
		t.Errorf("arranged\n%v\nwant\n%v", got, want)
	}
	if want := image.Rect(40, 35, 940, 550); usable != want {
		t.Errorf("usable area %v, want %v", usable, want)
	}
}
