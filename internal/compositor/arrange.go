package compositor

import (
	"image"
	"math"

	swlr "github.com/stubbedev/tilde/internal/protocol/server/wlrlayershell"
	"github.com/stubbedev/tilde/internal/shell"
)

type arranged struct {
	box image.Rectangle
	ok  bool
}

func centred(lo, span, size int) int {
	return lo + int(math.Round(float64(span)/2-float64(size)/2))
}

func placeAxis(lo, span, size, marginLo, marginHi int, anchorLo, anchorHi bool) (int, int) {
	var start int
	switch {
	case size == 0:
		start = lo
	case anchorLo && anchorHi:
		start = centred(lo, span, size)
	case anchorLo:
		start = lo
	case anchorHi:
		start = lo + span - size
	default:
		start = centred(lo, span, size)
	}
	switch {
	case size == 0:
		return start + marginLo, span - marginLo - marginHi
	case anchorLo && anchorHi:
	case anchorLo:
		start += marginLo
	case anchorHi:
		start -= marginHi
	}
	return start, size
}

func exclusiveEdge(s shell.LayerState) shell.Edges {
	const (
		top    = swlr.ZwlrLayerSurfaceV1AnchorTop
		bottom = swlr.ZwlrLayerSurfaceV1AnchorBottom
		left   = swlr.ZwlrLayerSurfaceV1AnchorLeft
		right  = swlr.ZwlrLayerSurfaceV1AnchorRight
	)
	if s.ExclusiveEdge != 0 {
		return s.ExclusiveEdge
	}
	switch s.Anchor {
	case top, top | left | right:
		return top
	case bottom, bottom | left | right:
		return bottom
	case left, left | top | bottom:
		return left
	case right, right | top | bottom:
		return right
	}
	return 0
}

func reserve(usable image.Rectangle, s shell.LayerState) image.Rectangle {
	if s.ExclusiveZone <= 0 {
		return usable
	}
	m := s.Margin
	switch exclusiveEdge(s) {
	case swlr.ZwlrLayerSurfaceV1AnchorTop:
		if d := s.ExclusiveZone + m.Top; d > 0 {
			usable.Min.Y += d
		}
	case swlr.ZwlrLayerSurfaceV1AnchorBottom:
		if d := s.ExclusiveZone + m.Bottom; d > 0 {
			usable.Max.Y -= d
		}
	case swlr.ZwlrLayerSurfaceV1AnchorLeft:
		if d := s.ExclusiveZone + m.Left; d > 0 {
			usable.Min.X += d
		}
	case swlr.ZwlrLayerSurfaceV1AnchorRight:
		if d := s.ExclusiveZone + m.Right; d > 0 {
			usable.Max.X -= d
		}
	}
	return usable
}

func arrangeLayers(full image.Rectangle, states []shell.LayerState) ([]arranged, image.Rectangle) {
	out := make([]arranged, len(states))
	usable := full
	for _, exclusive := range []bool{true, false} {
		for layer := swlr.ZwlrLayerShellV1LayerBackground; layer <= swlr.ZwlrLayerShellV1LayerOverlay; layer++ {
			for i, s := range states {
				if s.Layer != layer || (s.ExclusiveZone > 0) != exclusive {
					continue
				}
				bounds := usable
				if s.ExclusiveZone == -1 {
					bounds = full
				}
				x, w := placeAxis(bounds.Min.X, bounds.Dx(), s.Size.X, s.Margin.Left, s.Margin.Right,
					s.Anchor.Has(swlr.ZwlrLayerSurfaceV1AnchorLeft), s.Anchor.Has(swlr.ZwlrLayerSurfaceV1AnchorRight))
				y, h := placeAxis(bounds.Min.Y, bounds.Dy(), s.Size.Y, s.Margin.Top, s.Margin.Bottom,
					s.Anchor.Has(swlr.ZwlrLayerSurfaceV1AnchorTop), s.Anchor.Has(swlr.ZwlrLayerSurfaceV1AnchorBottom))
				if w <= 0 || h <= 0 {
					continue
				}
				out[i] = arranged{image.Rect(x, y, x+w, y+h), true}
				usable = reserve(usable, s)
			}
		}
	}
	return out, usable
}
