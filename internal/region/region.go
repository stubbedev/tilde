// Package region is a set of pixels described by disjoint rectangles.
package region

import (
	"image"
	"slices"
)

type Region struct {
	rects []image.Rectangle
}

func Rect(r image.Rectangle) Region {
	if r.Empty() {
		return Region{}
	}
	return Region{rects: []image.Rectangle{r.Canon()}}
}

func (g Region) Empty() bool {
	return len(g.rects) == 0
}

func (g Region) Rects() []image.Rectangle {
	return slices.Clone(g.rects)
}

func (g Region) Bounds() image.Rectangle {
	var b image.Rectangle
	for _, r := range g.rects {
		b = b.Union(r)
	}
	return b
}

func (g Region) Contains(p image.Point) bool {
	return slices.ContainsFunc(g.rects, p.In)
}

func (g Region) Area() int {
	n := 0
	for _, r := range g.rects {
		n += r.Dx() * r.Dy()
	}
	return n
}

func minus(a, b image.Rectangle, out []image.Rectangle) []image.Rectangle {
	cut := a.Intersect(b)
	if cut.Empty() {
		return append(out, a)
	}
	for _, piece := range []image.Rectangle{
		{Min: a.Min, Max: image.Pt(a.Max.X, cut.Min.Y)},
		{Min: image.Pt(a.Min.X, cut.Max.Y), Max: a.Max},
		{Min: image.Pt(a.Min.X, cut.Min.Y), Max: image.Pt(cut.Min.X, cut.Max.Y)},
		{Min: image.Pt(cut.Max.X, cut.Min.Y), Max: image.Pt(a.Max.X, cut.Max.Y)},
	} {
		if !piece.Empty() {
			out = append(out, piece)
		}
	}
	return out
}

func (g Region) Add(r image.Rectangle) Region {
	r = r.Canon()
	if r.Empty() {
		return g
	}
	pieces := []image.Rectangle{r}
	for _, existing := range g.rects {
		var next []image.Rectangle
		for _, p := range pieces {
			next = minus(p, existing, next)
		}
		pieces = next
	}
	return Region{rects: append(slices.Clone(g.rects), pieces...)}
}

func (g Region) Subtract(r image.Rectangle) Region {
	r = r.Canon()
	if r.Empty() {
		return g
	}
	var out []image.Rectangle
	for _, existing := range g.rects {
		out = minus(existing, r, out)
	}
	return Region{rects: out}
}

func (g Region) Union(o Region) Region {
	for _, r := range o.rects {
		g = g.Add(r)
	}
	return g
}

func (g Region) Intersect(r image.Rectangle) Region {
	var out []image.Rectangle
	for _, existing := range g.rects {
		if c := existing.Intersect(r); !c.Empty() {
			out = append(out, c)
		}
	}
	return Region{rects: out}
}

func (g Region) Translate(d image.Point) Region {
	out := make([]image.Rectangle, len(g.rects))
	for i, r := range g.rects {
		out[i] = r.Add(d)
	}
	return Region{rects: out}
}

func (g Region) Map(f func(image.Rectangle) image.Rectangle) Region {
	var out Region
	for _, r := range g.rects {
		out = out.Add(f(r))
	}
	return out
}
