package seat

import (
	"testing"

	scs "github.com/stubbedev/lys-wm/internal/protocol/server/cursorshape"
)

func TestCursorShapesFollowTheProtocolOrder(t *testing.T) {
	for _, tc := range []struct {
		shape scs.WpCursorShapeDeviceV1Shape
		name  string
	}{
		{scs.WpCursorShapeDeviceV1ShapeDefault, "default"},
		{scs.WpCursorShapeDeviceV1ShapeText, "text"},
		{scs.WpCursorShapeDeviceV1ShapeNwseResize, "nwse-resize"},
		{scs.WpCursorShapeDeviceV1ShapeZoomOut, "zoom-out"},
		{scs.WpCursorShapeDeviceV1ShapeDndAsk, "dnd-ask"},
		{scs.WpCursorShapeDeviceV1ShapeAllResize, "all-resize"},
	} {
		if name, ok := shapeName(tc.shape); !ok || name != tc.name {
			t.Errorf("shape %d is %q (%t), want %q", tc.shape, name, ok, tc.name)
		}
	}
	for _, bad := range []scs.WpCursorShapeDeviceV1Shape{0, scs.WpCursorShapeDeviceV1ShapeAllResize + 1} {
		if name, ok := shapeName(bad); ok {
			t.Errorf("shape %d is %q, want none", bad, name)
		}
	}
}
