// Package render defines the renderer interface that the software and GLES
// renderers implement.
package render

import "image"

type Transform uint8

const (
	Normal Transform = iota
	Rotate90
	Rotate180
	Rotate270
	Flipped
	Flipped90
	Flipped180
	Flipped270
)

func (t Transform) SwapsAxes() bool {
	return t&1 == 1
}

func (t Transform) Invert() Transform {
	switch t {
	case Rotate90:
		return Rotate270
	case Rotate270:
		return Rotate90
	}
	return t
}

func (t Transform) Size(size image.Point) image.Point {
	if t.SwapsAxes() {
		return image.Pt(size.Y, size.X)
	}
	return size
}

func (t Transform) Point(p, size image.Point) image.Point {
	w, h := size.X, size.Y
	switch t {
	case Rotate90:
		return image.Pt(p.Y, w-p.X)
	case Rotate180:
		return image.Pt(w-p.X, h-p.Y)
	case Rotate270:
		return image.Pt(h-p.Y, p.X)
	case Flipped:
		return image.Pt(w-p.X, p.Y)
	case Flipped90:
		return image.Pt(p.Y, p.X)
	case Flipped180:
		return image.Pt(p.X, h-p.Y)
	case Flipped270:
		return image.Pt(h-p.Y, w-p.X)
	}
	return p
}

func (t Transform) Rect(r image.Rectangle, size image.Point) image.Rectangle {
	return image.Rectangle{Min: t.Point(r.Min, size), Max: t.Point(r.Max, size)}.Canon()
}

type Format uint8

const (
	ARGB8888 Format = iota + 1
	XRGB8888
)

type Color struct {
	R, G, B, A uint8
}

type FRect struct {
	X, Y, W, H float64
}

type Region []image.Rectangle

type Target interface {
	Size() image.Point
}

type Texture interface {
	Size() image.Point
	Destroy()
}

type Renderer interface {
	NewTexture(format Format, size image.Point, stride int, data []byte) (Texture, error)
	Begin(target Target, damage Region) (Pass, error)
}

type Pass interface {
	FillRect(r image.Rectangle, c Color)
	DrawTexture(t Texture, src FRect, dst image.Rectangle, transform Transform, alpha float32)
	End() error
}
