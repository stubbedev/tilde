package render_test

import (
	"image"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/soft"
)

var transforms = []render.Transform{
	render.Normal, render.Rotate90, render.Rotate180, render.Rotate270,
	render.Flipped, render.Flipped90, render.Flipped180, render.Flipped270,
}

func TestRectAgreesWithTheRenderer(t *testing.T) {
	size := image.Pt(5, 3)
	marked := image.Rect(1, 0, 3, 2)
	r := soft.New()
	data := make([]byte, size.X*size.Y*4)
	for y := marked.Min.Y; y < marked.Max.Y; y++ {
		for x := marked.Min.X; x < marked.Max.X; x++ {
			copy(data[(y*size.X+x)*4:], []byte{0xff, 0xff, 0xff, 0xff})
		}
	}
	tex, err := r.NewTexture(render.ARGB8888, size, size.X*4, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range transforms {
		out := tr.Size(size)
		b := soft.NewBuffer(out.X, out.Y)
		p, err := r.Begin(b, render.Region{image.Rectangle{Max: out}})
		if err != nil {
			t.Fatal(err)
		}
		p.DrawTexture(tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, image.Rectangle{Max: out}, tr, 1)
		if err := p.End(); err != nil {
			t.Fatal(err)
		}
		want := tr.Rect(marked, size)
		for y := range out.Y {
			for x := range out.X {
				lit := b.Pix[(y*out.X+x)*4+3] == 0xff
				if lit != image.Pt(x, y).In(want) {
					t.Fatalf("transform %d: pixel (%d, %d) lit %v, but Rect maps the mark to %v", tr, x, y, lit, want)
				}
			}
		}
	}
}

func TestInvertUndoesTheTransform(t *testing.T) {
	size := image.Pt(7, 4)
	r := image.Rect(1, 2, 3, 4)
	for _, tr := range transforms {
		there := tr.Rect(r, size)
		if back := tr.Invert().Rect(there, tr.Size(size)); back != r {
			t.Errorf("transform %d: %v -> %v -> %v", tr, r, there, back)
		}
	}
}

func TestFRectAgreesWithRect(t *testing.T) {
	size := image.Pt(7, 4)
	r := image.Rect(1, 2, 3, 4)
	for _, tr := range transforms {
		got := tr.FRect(render.FRect{X: 1, Y: 2, W: 2, H: 2}, size)
		want := tr.Rect(r, size)
		if got != (render.FRect{X: float64(want.Min.X), Y: float64(want.Min.Y), W: float64(want.Dx()), H: float64(want.Dy())}) {
			t.Errorf("transform %d: FRect %+v, Rect %v", tr, got, want)
		}
	}
}

func TestCheckPixelsNamesTheBrokenRule(t *testing.T) {
	for _, c := range []struct {
		size      image.Point
		stride, n int
		want      string
	}{
		{image.Pt(4, 2), 16, 32, ""},
		{image.Pt(4, 2), 20, 36, ""},
		{image.Pt(0, 2), 16, 32, "not a positive size"},
		{image.Pt(4, 2), 12, 32, "shorter than a row"},
		{image.Pt(4, 2), 17, 64, "not a whole number of pixels"},
		{image.Pt(4, 2), 16, 31, "do not fit 31 bytes"},
	} {
		err := render.CheckPixels(c.size, c.stride, c.n)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("CheckPixels(%v, %d, %d) = %v, want %q", c.size, c.stride, c.n, err, c.want)
		}
	}
}
