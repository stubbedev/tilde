package seat

import (
	"fmt"

	scs "github.com/stubbedev/tilde/internal/protocol/server/cursorshape"
	"github.com/stubbedev/tilde/internal/protocol/server/tablet"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

const cursorShapeVersion = 2

var shapeNames = map[scs.WpCursorShapeDeviceV1Shape]string{
	scs.WpCursorShapeDeviceV1ShapeDefault:      "default",
	scs.WpCursorShapeDeviceV1ShapeContextMenu:  "context-menu",
	scs.WpCursorShapeDeviceV1ShapeHelp:         "help",
	scs.WpCursorShapeDeviceV1ShapePointer:      "pointer",
	scs.WpCursorShapeDeviceV1ShapeProgress:     "progress",
	scs.WpCursorShapeDeviceV1ShapeWait:         "wait",
	scs.WpCursorShapeDeviceV1ShapeCell:         "cell",
	scs.WpCursorShapeDeviceV1ShapeCrosshair:    "crosshair",
	scs.WpCursorShapeDeviceV1ShapeText:         "text",
	scs.WpCursorShapeDeviceV1ShapeVerticalText: "vertical-text",
	scs.WpCursorShapeDeviceV1ShapeAlias:        "alias",
	scs.WpCursorShapeDeviceV1ShapeCopy:         "copy",
	scs.WpCursorShapeDeviceV1ShapeMove:         "move",
	scs.WpCursorShapeDeviceV1ShapeNoDrop:       "no-drop",
	scs.WpCursorShapeDeviceV1ShapeNotAllowed:   "not-allowed",
	scs.WpCursorShapeDeviceV1ShapeGrab:         "grab",
	scs.WpCursorShapeDeviceV1ShapeGrabbing:     "grabbing",
	scs.WpCursorShapeDeviceV1ShapeEResize:      "e-resize",
	scs.WpCursorShapeDeviceV1ShapeNResize:      "n-resize",
	scs.WpCursorShapeDeviceV1ShapeNeResize:     "ne-resize",
	scs.WpCursorShapeDeviceV1ShapeNwResize:     "nw-resize",
	scs.WpCursorShapeDeviceV1ShapeSResize:      "s-resize",
	scs.WpCursorShapeDeviceV1ShapeSeResize:     "se-resize",
	scs.WpCursorShapeDeviceV1ShapeSwResize:     "sw-resize",
	scs.WpCursorShapeDeviceV1ShapeWResize:      "w-resize",
	scs.WpCursorShapeDeviceV1ShapeEwResize:     "ew-resize",
	scs.WpCursorShapeDeviceV1ShapeNsResize:     "ns-resize",
	scs.WpCursorShapeDeviceV1ShapeNeswResize:   "nesw-resize",
	scs.WpCursorShapeDeviceV1ShapeNwseResize:   "nwse-resize",
	scs.WpCursorShapeDeviceV1ShapeColResize:    "col-resize",
	scs.WpCursorShapeDeviceV1ShapeRowResize:    "row-resize",
	scs.WpCursorShapeDeviceV1ShapeAllScroll:    "all-scroll",
	scs.WpCursorShapeDeviceV1ShapeZoomIn:       "zoom-in",
	scs.WpCursorShapeDeviceV1ShapeZoomOut:      "zoom-out",
	scs.WpCursorShapeDeviceV1ShapeDndAsk:       "dnd-ask",
	scs.WpCursorShapeDeviceV1ShapeAllResize:    "all-resize",
}

func (s *Seat) bindCursorShape(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := scs.NewWpCursorShapeManagerV1(c, id, version, cursorShapeManager{s, c})
	return err
}

type cursorShapeManager struct {
	s *Seat
	c *server.Client
}

func (cursorShapeManager) Destroy(*scs.WpCursorShapeManagerV1) error {
	return nil
}

func (m cursorShapeManager) GetPointer(_ *scs.WpCursorShapeManagerV1, _ *scs.WpCursorShapeDeviceV1, _ *sw.WlPointer) (scs.WpCursorShapeDeviceV1Handler, error) {
	return shapeDevice{m.s, m.c, true}, nil
}

func (m cursorShapeManager) GetTabletToolV2(_ *scs.WpCursorShapeManagerV1, _ *scs.WpCursorShapeDeviceV1, _ *tablet.ZwpTabletToolV2) (scs.WpCursorShapeDeviceV1Handler, error) {
	return shapeDevice{m.s, m.c, false}, nil
}

type shapeDevice struct {
	s       *Seat
	c       *server.Client
	pointer bool
}

func (shapeDevice) Destroy(*scs.WpCursorShapeDeviceV1) error {
	return nil
}

func (d shapeDevice) SetShape(obj *scs.WpCursorShapeDeviceV1, serial uint32, shape scs.WpCursorShapeDeviceV1Shape) error {
	name, ok := shapeNames[shape]
	if !ok || (obj.Base().Version() < 2 && shape > scs.WpCursorShapeDeviceV1ShapeZoomOut) {
		return obj.ProtocolError(scs.WpCursorShapeDeviceV1ErrorInvalidShape, fmt.Sprintf("invalid shape %d", shape))
	}
	p := &d.s.pointer
	if !d.pointer || p.focus == nil || p.focus.Client() != d.c || serial != p.enterSerial {
		return nil
	}
	p.cursor = Cursor{client: d.c, shape: name, set: true}
	return d.s.listener.CursorChanged()
}
