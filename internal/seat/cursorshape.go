package seat

import (
	scs "github.com/stubbedev/tilde/internal/protocol/server/cursorshape"
	"github.com/stubbedev/tilde/internal/protocol/server/tablet"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/xcursor"
)

const cursorShapeVersion = 2

func shapeName(shape scs.WpCursorShapeDeviceV1Shape) (string, bool) {
	if shape < scs.WpCursorShapeDeviceV1ShapeDefault || int(shape) > len(xcursor.Shapes) {
		return "", false
	}
	return xcursor.Shapes[shape-scs.WpCursorShapeDeviceV1ShapeDefault].Name, true
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
	name, ok := shapeName(shape)
	if !ok || !shape.Valid(obj.Base().Version()) {
		return obj.ProtocolError(scs.WpCursorShapeDeviceV1ErrorInvalidShape, "invalid shape %d", shape)
	}
	p := &d.s.pointer
	if !d.pointer || p.focus == nil || p.focus.Client() != d.c || serial != p.enterSerial {
		return nil
	}
	p.cursor = clientCursor{client: d.c, want: ShapeCursor{Shape: name}}
	return d.s.listener.CursorChanged()
}
