// Package render defines the renderer interface that the software and GLES
// renderers implement.
package render

import (
	"fmt"
	"image"
	"math"
)

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

func (t Transform) fpoint(x, y float64, size image.Point) (float64, float64) {
	w, h := float64(size.X), float64(size.Y)
	switch t {
	case Rotate90:
		return y, w - x
	case Rotate180:
		return w - x, h - y
	case Rotate270:
		return h - y, x
	case Flipped:
		return w - x, y
	case Flipped90:
		return y, x
	case Flipped180:
		return x, h - y
	case Flipped270:
		return h - y, w - x
	}
	return x, y
}

func (t Transform) FRect(r FRect, size image.Point) FRect {
	x0, y0 := t.fpoint(r.X, r.Y, size)
	x1, y1 := t.fpoint(r.X+r.W, r.Y+r.H, size)
	return FRect{X: min(x0, x1), Y: min(y0, y1), W: math.Abs(x1 - x0), H: math.Abs(y1 - y0)}
}

type Axis struct {
	Source   int
	Reversed bool
}

func (t Transform) Axes() (horizontal, vertical Axis) {
	h, v := Axis{0, false}, Axis{1, false}
	switch t &^ Flipped {
	case Rotate90:
		h, v = Axis{1, false}, Axis{0, true}
	case Rotate180:
		h, v = Axis{0, true}, Axis{1, true}
	case Rotate270:
		h, v = Axis{1, true}, Axis{0, false}
	}
	if t&Flipped != 0 {
		if h.Source == 0 {
			h.Reversed = !h.Reversed
		} else {
			v.Reversed = !v.Reversed
		}
	}
	return h, v
}

type Format uint8

const (
	ARGB8888 Format = iota + 1
	XRGB8888
)

type Color struct {
	R, G, B, A uint8
}

func (c Color) Premultiplied() bool {
	return c.R <= c.A && c.G <= c.A && c.B <= c.A
}

type FRect struct {
	X, Y, W, H float64
}

func CheckDraw(texture image.Point, src FRect, transform Transform, alpha float32) error {
	switch {
	case transform > Flipped270:
		return fmt.Errorf("render: invalid transform %d", transform)
	case !(alpha >= 0 && alpha <= 1):
		return fmt.Errorf("render: alpha %v outside [0, 1]", alpha)
	case !(src.W > 0 && src.H > 0 && src.X >= 0 && src.Y >= 0) || src.X+src.W > float64(texture.X) || src.Y+src.H > float64(texture.Y):
		return fmt.Errorf("render: source %+v outside texture %v", src, texture)
	}
	return nil
}

type Region []image.Rectangle

type Target interface {
	Size() image.Point
}

type Texture interface {
	Size() image.Point
	Update(stride int, data []byte, damage Region) error
	Destroy() error
}

type Renderer interface {
	Formats() []Format
	NewTexture(format Format, size image.Point, stride int, data []byte) (Texture, error)
	Begin(target Target, damage Region) (Pass, error)
}

type Pass interface {
	FillRect(r image.Rectangle, c Color)
	DrawTexture(t Texture, src FRect, dst image.Rectangle, transform Transform, alpha float32)
	End() error
}
