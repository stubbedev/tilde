// Package soft is the CPU renderer over premultiplied ARGB8888 buffers.
package soft

import (
	"errors"
	"fmt"
	"image"
	"math"
	"slices"

	"github.com/stubbedev/tilde/internal/render"
)

type Buffer struct {
	Pix    []byte
	Stride int
	W, H   int
}

func NewBuffer(w, h int) *Buffer {
	return &Buffer{Pix: make([]byte, w*h*4), Stride: w * 4, W: w, H: h}
}

func (b *Buffer) Size() image.Point {
	return image.Pt(b.W, b.H)
}

func (b *Buffer) Read(pix []byte, stride int) error {
	if err := render.CheckPixels(b.Size(), stride, len(pix)); err != nil {
		return err
	}
	for y := range b.H {
		copy(pix[y*stride:y*stride+b.W*4], b.Pix[y*b.Stride:])
	}
	return nil
}

func (b *Buffer) Destroy() error {
	b.Pix, b.W, b.H = nil, 0, 0
	return nil
}

func (b *Buffer) bounds() image.Rectangle {
	return image.Rect(0, 0, b.W, b.H)
}

type texture struct {
	owner  *Renderer
	pix    []byte
	stride int
	size   image.Point
	opaque bool
}

func (t *texture) Size() image.Point {
	return t.size
}

func (t *texture) Update(stride int, data []byte, damage render.Region) error {
	if t.pix == nil {
		return errors.New("soft: update of a destroyed texture")
	}
	if err := render.CheckPixels(t.size, stride, len(data)); err != nil {
		return err
	}
	bounds := image.Rectangle{Max: t.size}
	for _, r := range damage {
		r = r.Intersect(bounds)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			row := t.pix[y*t.stride+r.Min.X*4 : y*t.stride+r.Max.X*4]
			copy(row, data[y*stride+r.Min.X*4:])
			if t.opaque {
				for i := 3; i < len(row); i += 4 {
					row[i] = 0xff
				}
			}
		}
	}
	return nil
}

func (t *texture) Destroy() error {
	t.pix = nil
	return nil
}

type Renderer struct {
	pass pass
}

func New() *Renderer {
	r := &Renderer{}
	r.pass.owner = r
	return r
}

func (r *Renderer) Formats() []render.Format {
	return []render.Format{render.ARGB8888, render.XRGB8888}
}

func (r *Renderer) NewTexture(format render.Format, size image.Point, stride int, data []byte) (render.Texture, error) {
	if !slices.Contains(r.Formats(), format) {
		return nil, fmt.Errorf("soft: unsupported texture format %d", format)
	}
	if err := render.CheckPixels(size, stride, len(data)); err != nil {
		return nil, err
	}
	t := &texture{owner: r, pix: make([]byte, size.X*size.Y*4), stride: size.X * 4, size: size, opaque: format == render.XRGB8888}
	if err := t.Update(stride, data, render.Region{image.Rectangle{Max: size}}); err != nil {
		return nil, err
	}
	return t, nil
}

func (r *Renderer) NewTarget(size image.Point) (render.ReadTarget, error) {
	if size.X <= 0 || size.Y <= 0 {
		return nil, fmt.Errorf("soft: cannot make a %v target", size)
	}
	return NewBuffer(size.X, size.Y), nil
}

func (r *Renderer) Begin(target render.Target, damage render.Region) (render.Pass, error) {
	buf, ok := target.(*Buffer)
	if !ok {
		return nil, fmt.Errorf("soft: cannot render into %T", target)
	}
	if r.pass.active {
		return nil, errors.New("soft: Begin while a pass is active")
	}
	p := &r.pass
	p.active, p.buf, p.err = true, buf, nil
	p.damage = p.damage[:0]
	for _, d := range damage {
		if d = d.Intersect(buf.bounds()); !d.Empty() {
			p.damage = append(p.damage, d)
		}
	}
	return p, nil
}

type pass struct {
	owner  *Renderer
	active bool
	buf    *Buffer
	damage []image.Rectangle
	err    error
	cols   []int
	rows   []int
}

func (p *pass) fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

func (p *pass) End() error {
	if !p.active {
		return errors.New("soft: End without an active pass")
	}
	p.active, p.buf = false, nil
	return p.err
}

func over(dst []byte, b, g, r, a byte) {
	if a == 0xff {
		dst[0], dst[1], dst[2], dst[3] = b, g, r, a
		return
	}
	inv := uint32(0xff - a)
	dst[0] = b + byte((uint32(dst[0])*inv+127)/255)
	dst[1] = g + byte((uint32(dst[1])*inv+127)/255)
	dst[2] = r + byte((uint32(dst[2])*inv+127)/255)
	dst[3] = a + byte((uint32(dst[3])*inv+127)/255)
}

func scale(c byte, a uint32) byte {
	return byte((uint32(c)*a + 127) / 255)
}

func (p *pass) Clear(c render.Color) {
	if !p.active {
		p.fail(errors.New("soft: Clear outside a pass"))
		return
	}
	if !c.Premultiplied() {
		p.fail(fmt.Errorf("soft: color %+v is not premultiplied", c))
		return
	}
	for _, d := range p.damage {
		for y := d.Min.Y; y < d.Max.Y; y++ {
			row := p.buf.Pix[y*p.buf.Stride:]
			for x := d.Min.X; x < d.Max.X; x++ {
				row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = c.B, c.G, c.R, c.A
			}
		}
	}
}

func (p *pass) FillRect(r image.Rectangle, c render.Color) {
	if !p.active {
		p.fail(errors.New("soft: FillRect outside a pass"))
		return
	}
	if !c.Premultiplied() {
		p.fail(fmt.Errorf("soft: color %+v is not premultiplied", c))
		return
	}
	for _, d := range p.damage {
		clip := r.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			row := p.buf.Pix[y*p.buf.Stride:]
			for x := clip.Min.X; x < clip.Max.X; x++ {
				over(row[x*4:x*4+4], c.B, c.G, c.R, c.A)
			}
		}
	}
}

func (p *pass) DrawTexture(t render.Texture, src render.FRect, dst image.Rectangle, transform render.Transform, alpha float32) {
	tex, ok := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("soft: DrawTexture outside a pass"))
		return
	case !ok || tex.owner != p.owner:
		p.fail(fmt.Errorf("soft: texture %T belongs to another renderer", t))
		return
	case tex.pix == nil:
		p.fail(errors.New("soft: texture used after Destroy"))
		return
	case dst.Empty():
		return
	}
	if err := render.CheckDraw(tex.size, src, transform, alpha); err != nil {
		p.fail(err)
		return
	}
	a := uint32(math.Round(float64(alpha) * 255))
	if a == 0 {
		return
	}
	horizontal, vertical := transform.Axes()
	p.cols = sampleMap(p.cols[:0], dst.Dx(), horizontal, src, tex.size)
	p.rows = sampleMap(p.rows[:0], dst.Dy(), vertical, src, tex.size)
	swap := transform.SwapsAxes()
	for _, d := range p.damage {
		clip := dst.Intersect(d)
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			out := p.buf.Pix[y*p.buf.Stride:]
			row := p.rows[y-dst.Min.Y]
			for x := clip.Min.X; x < clip.Max.X; x++ {
				col := p.cols[x-dst.Min.X]
				sx, sy := col, row
				if swap {
					sx, sy = row, col
				}
				px := tex.pix[sy*tex.stride+sx*4:]
				if a == 0xff {
					over(out[x*4:x*4+4], px[0], px[1], px[2], px[3])
				} else {
					over(out[x*4:x*4+4], scale(px[0], a), scale(px[1], a), scale(px[2], a), scale(px[3], a))
				}
			}
		}
	}
}

func sampleMap(out []int, n int, a render.Axis, src render.FRect, size image.Point) []int {
	origin, extent, limit := src.X, src.W, size.X
	if a.Source == 1 {
		origin, extent, limit = src.Y, src.H, size.Y
	}
	for i := range n {
		f := (float64(i) + 0.5) / float64(n)
		if a.Reversed {
			f = 1 - f
		}
		s := int(math.Floor(origin + f*extent))
		out = append(out, min(max(s, 0), limit-1))
	}
	return out
}
