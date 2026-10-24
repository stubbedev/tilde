// Package singlepixel implements wp_single_pixel_buffer_manager_v1: one-pixel
// buffers of a solid premultiplied color.
package singlepixel

import (
	"image"

	sspb "github.com/stubbedev/lys-wm/internal/protocol/server/singlepixelbuffer"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 1

func New(d *server.Display) error {
	_, err := d.AddGlobal(sspb.WpSinglePixelBufferManagerV1Interface, version, bind)
	return err
}

func bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := sspb.NewWpSinglePixelBufferManagerV1(c, id, v, manager{})
	return err
}

type manager struct{}

func (manager) Destroy(*sspb.WpSinglePixelBufferManagerV1) error {
	return nil
}

func channel(v uint32) byte {
	return byte((uint64(v)*0xff + 0x7fffffff) / 0xffffffff) //nolint:gosec // at most 0xff for any uint32
}

func (manager) CreateU32RgbaBuffer(_ *sspb.WpSinglePixelBufferManagerV1, res *sw.WlBuffer, r, g, b, a uint32) (sw.WlBufferHandler, error) {
	return &Buffer{resource: res, pixel: [4]byte{channel(b), channel(g), channel(r), channel(a)}}, nil
}

type Buffer struct {
	resource *sw.WlBuffer
	pixel    [4]byte
}

func (b *Buffer) Size() image.Point {
	return image.Pt(1, 1)
}

func (b *Buffer) Pixel() []byte {
	return b.pixel[:]
}

func (b *Buffer) Destroy(*sw.WlBuffer) error {
	return nil
}

func (b *Buffer) Release() error {
	return b.resource.SendRelease()
}
