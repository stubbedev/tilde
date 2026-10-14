package soft_test

import (
	"image"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/golden"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/soft"
)

var _ render.Renderer = (*soft.Renderer)(nil)

func px(b, g, r, a byte) [4]byte { return [4]byte{b, g, r, a} }

func newTex(t *testing.T, r *soft.Renderer, format render.Format, w, h int, pixels ...[4]byte) render.Texture {
	t.Helper()
	data := make([]byte, 0, w*h*4)
	for _, p := range pixels {
		data = append(data, p[:]...)
	}
	tex, err := r.NewTexture(format, image.Pt(w, h), w*4, data)
	if err != nil {
		t.Fatal(err)
	}
	return tex
}

func at(b *soft.Buffer, x, y int) [4]byte {
	i := y*b.Stride + x*4
	return [4]byte(b.Pix[i : i+4])
}

func frame(t *testing.T, r *soft.Renderer, b *soft.Buffer, damage render.Region, draw func(render.Pass)) {
	t.Helper()
	p, err := r.Begin(b, damage)
	if err != nil {
		t.Fatal(err)
	}
	draw(p)
	if err := p.End(); err != nil {
		t.Fatal(err)
	}
}

func bounds(b *soft.Buffer) image.Rectangle { return image.Rect(0, 0, b.W, b.H) }

func full(b *soft.Buffer) render.Region { return render.Region{bounds(b)} }

var (
	red   = px(0, 0, 0xff, 0xff)
	green = px(0, 0xff, 0, 0xff)
	blue  = px(0xff, 0, 0, 0xff)
	white = px(0xff, 0xff, 0xff, 0xff)
	black = px(0, 0, 0, 0xff)
)

func TestTransformsMoveCornersCounterClockwise(t *testing.T) {
	r := soft.New()
	tex := newTex(t, r, render.ARGB8888, 2, 2, red, green, blue, white)
	cases := []struct {
		tr  render.Transform
		tl  [4]byte
		tr2 [4]byte
		bl  [4]byte
		br  [4]byte
	}{
		{render.Normal, red, green, blue, white},
		{render.Rotate90, green, white, red, blue},
		{render.Rotate180, white, blue, green, red},
		{render.Rotate270, blue, red, white, green},
		{render.Flipped, green, red, white, blue},
		{render.Flipped90, red, blue, green, white},
		{render.Flipped180, blue, white, red, green},
		{render.Flipped270, white, green, blue, red},
	}
	for _, c := range cases {
		b := soft.NewBuffer(2, 2)
		frame(t, r, b, full(b), func(p render.Pass) {
			p.DrawTexture(tex, render.FRect{W: 2, H: 2}, bounds(b), c.tr, 1)
		})
		got := [4][4]byte{at(b, 0, 0), at(b, 1, 0), at(b, 0, 1), at(b, 1, 1)}
		if want := [4][4]byte{c.tl, c.tr2, c.bl, c.br}; got != want {
			t.Errorf("transform %d: corners %v, want %v", c.tr, got, want)
		}
	}
}

func TestSwappedTransformsFillTheTransposedRect(t *testing.T) {
	r := soft.New()
	tex := newTex(t, r, render.ARGB8888, 3, 1, red, green, blue)
	b := soft.NewBuffer(1, 3)
	frame(t, r, b, full(b), func(p render.Pass) {
		p.DrawTexture(tex, render.FRect{W: 3, H: 1}, bounds(b), render.Rotate90, 1)
	})
	if got := [3][4]byte{at(b, 0, 0), at(b, 0, 1), at(b, 0, 2)}; got != [3][4]byte{blue, green, red} {
		t.Fatalf("a 3x1 row rotated 90 degrees counter-clockwise reads %v top to bottom", got)
	}
}

func TestFillRectBlendsAndClipsToDamage(t *testing.T) {
	r := soft.New()
	b := soft.NewBuffer(4, 1)
	frame(t, r, b, full(b), func(p render.Pass) { p.FillRect(bounds(b), render.Color{A: 0xff}) })
	frame(t, r, b, render.Region{image.Rect(1, 0, 3, 1)}, func(p render.Pass) {
		p.FillRect(bounds(b), render.Color{R: 0x80, A: 0x80})
	})
	want := [4][4]byte{black, px(0, 0, 0x80, 0xff), px(0, 0, 0x80, 0xff), black}
	if got := [4][4]byte{at(b, 0, 0), at(b, 1, 0), at(b, 2, 0), at(b, 3, 0)}; got != want {
		t.Fatalf("pixels %v, want %v: half-transparent red over black inside the damage only", got, want)
	}
}

func TestClearReplacesTheDamageWithoutBlending(t *testing.T) {
	r := soft.New()
	b := soft.NewBuffer(4, 1)
	frame(t, r, b, full(b), func(p render.Pass) { p.FillRect(bounds(b), render.Color{A: 0xff}) })
	frame(t, r, b, render.Region{image.Rect(1, 0, 3, 1)}, func(p render.Pass) { p.Clear(render.Color{G: 0x40, A: 0x40}) })
	want := [4][4]byte{black, px(0, 0x40, 0, 0x40), px(0, 0x40, 0, 0x40), black}
	if got := [4][4]byte{at(b, 0, 0), at(b, 1, 0), at(b, 2, 0), at(b, 3, 0)}; got != want {
		t.Fatalf("pixels %v, want %v: translucent green replacing black inside the damage only", got, want)
	}
}

func TestReadCopiesRowsAtAnyStride(t *testing.T) {
	r := soft.New()
	b := soft.NewBuffer(2, 2)
	frame(t, r, b, render.Region{image.Rect(1, 1, 2, 2)}, func(p render.Pass) { p.Clear(render.Color{B: 0x10, A: 0x20}) })
	pix := make([]byte, 2*12)
	if err := b.Read(pix, 12); err != nil {
		t.Fatal(err)
	}
	if want := px(0x10, 0, 0, 0x20); [4]byte(pix[16:20]) != want || [4]byte(pix[0:4]) != px(0, 0, 0, 0) {
		t.Fatalf("read %v", pix)
	}
	if err := b.Read(pix[:12], 12); err == nil {
		t.Fatal("a short read target was accepted")
	}
}

func TestTextureAlphaAndFormats(t *testing.T) {
	r := soft.New()
	translucent := newTex(t, r, render.ARGB8888, 1, 1, px(0, 0, 0x80, 0x80))
	ignored := newTex(t, r, render.XRGB8888, 1, 1, px(0, 0xff, 0, 0))
	b := soft.NewBuffer(3, 1)
	frame(t, r, b, full(b), func(p render.Pass) {
		p.FillRect(bounds(b), render.Color{B: 0xff, A: 0xff})
		p.DrawTexture(translucent, render.FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), render.Normal, 1)
		p.DrawTexture(ignored, render.FRect{W: 1, H: 1}, image.Rect(1, 0, 2, 1), render.Normal, 1)
		p.DrawTexture(ignored, render.FRect{W: 1, H: 1}, image.Rect(2, 0, 3, 1), render.Normal, 0.5)
	})
	want := [3][4]byte{px(0x7f, 0, 0x80, 0xff), green, px(0x7f, 0x80, 0, 0xff)}
	if got := [3][4]byte{at(b, 0, 0), at(b, 1, 0), at(b, 2, 0)}; got != want {
		t.Fatalf("pixels %v, want %v", got, want)
	}
}

func TestTextureUpdateCopiesOnlyDamage(t *testing.T) {
	r := soft.New()
	tex := newTex(t, r, render.XRGB8888, 2, 1, red, red)
	fresh := []byte{0xff, 0, 0, 0, 0xff, 0, 0, 0}
	if err := tex.Update(8, fresh, render.Region{image.Rect(1, 0, 2, 1)}); err != nil {
		t.Fatal(err)
	}
	b := soft.NewBuffer(2, 1)
	frame(t, r, b, full(b), func(p render.Pass) {
		p.DrawTexture(tex, render.FRect{W: 2, H: 1}, bounds(b), render.Normal, 1)
	})
	if got := [2][4]byte{at(b, 0, 0), at(b, 1, 0)}; got != [2][4]byte{red, blue} {
		t.Fatalf("pixels %v: only the damaged pixel should change, and XRGB stays opaque", got)
	}
	if err := tex.Update(4, fresh, nil); err == nil {
		t.Fatal("an update with a short stride was accepted")
	}
	tex.Destroy()
	if err := tex.Update(8, fresh, nil); err == nil {
		t.Fatal("an update of a destroyed texture was accepted")
	}
}

func TestInvalidDrawsFailAtEnd(t *testing.T) {
	r := soft.New()
	other := newTex(t, soft.New(), render.ARGB8888, 1, 1, red)
	tex := newTex(t, r, render.ARGB8888, 2, 2, red, red, red, red)
	destroyed := newTex(t, r, render.ARGB8888, 1, 1, red)
	destroyed.Destroy()
	cases := map[string]func(render.Pass){
		"source outside texture": func(p render.Pass) {
			p.DrawTexture(tex, render.FRect{X: 1, W: 2, H: 2}, image.Rect(0, 0, 1, 1), render.Normal, 1)
		},
		"other renderer": func(p render.Pass) {
			p.DrawTexture(other, render.FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), render.Normal, 1)
		},
		"destroyed": func(p render.Pass) {
			p.DrawTexture(destroyed, render.FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), render.Normal, 1)
		},
		"alpha above one": func(p render.Pass) {
			p.DrawTexture(tex, render.FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), render.Normal, 2)
		},
		"invalid transform": func(p render.Pass) { p.DrawTexture(tex, render.FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), 9, 1) },
		"not premultiplied": func(p render.Pass) { p.FillRect(image.Rect(0, 0, 1, 1), render.Color{R: 0xff, A: 0x10}) },
	}
	for name, draw := range cases {
		b := soft.NewBuffer(2, 2)
		p, err := r.Begin(b, full(b))
		if err != nil {
			t.Fatal(err)
		}
		draw(p)
		if err := p.End(); err == nil {
			t.Errorf("%s: End succeeded", name)
		}
	}
	b := soft.NewBuffer(1, 1)
	if _, err := r.Begin(b, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Begin(b, nil); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("second Begin = %v, want an active-pass error", err)
	}
	if _, err := r.NewTexture(render.ARGB8888, image.Pt(2, 2), 8, make([]byte, 15)); err == nil {
		t.Fatal("NewTexture accepted too little data")
	}
}

func checker(t *testing.T, r *soft.Renderer, w, h int) render.Texture {
	t.Helper()
	pixels := make([][4]byte, 0, w*h)
	for y := range h {
		for x := range w {
			pixels = append(pixels, px(byte(x*255/(w-1)), byte(y*255/(h-1)), byte((x+y)%2*0xff), 0xff))
		}
	}
	return newTex(t, r, render.ARGB8888, w, h, pixels...)
}

func TestGoldenScene(t *testing.T) {
	r := soft.New()
	b := soft.NewBuffer(96, 64)
	tex := checker(t, r, 8, 6)
	glass := newTex(t, r, render.ARGB8888, 2, 1, px(0, 0x40, 0, 0x40), px(0x40, 0, 0, 0x80))
	frame(t, r, b, full(b), func(p render.Pass) {
		p.FillRect(bounds(b), render.Color{R: 0x20, G: 0x20, B: 0x30, A: 0xff})
		for i, tr := range []render.Transform{render.Normal, render.Rotate90, render.Rotate180, render.Rotate270, render.Flipped, render.Flipped90, render.Flipped180, render.Flipped270} {
			size := image.Pt(16, 12)
			if tr.SwapsAxes() {
				size = image.Pt(12, 16)
			}
			origin := image.Pt(4+(i%4)*22, 4+(i/4)*22)
			p.DrawTexture(tex, render.FRect{W: 8, H: 6}, image.Rectangle{Min: origin, Max: origin.Add(size)}, tr, 1)
		}
		p.DrawTexture(tex, render.FRect{X: 2, Y: 1, W: 3.5, H: 2.5}, image.Rect(4, 48, 32, 60), render.Normal, 1)
		p.DrawTexture(tex, render.FRect{W: 8, H: 6}, image.Rect(36, 48, 52, 60), render.Normal, 0.5)
		p.DrawTexture(glass, render.FRect{W: 2, H: 1}, image.Rect(56, 48, 92, 60), render.Normal, 1)
		p.FillRect(image.Rect(0, 0, 96, 2), render.Color{R: 0x80, G: 0x40, A: 0x80})
	})
	golden.Assert(t, "scene", b)
	frame(t, r, b, render.Region{image.Rect(10, 10, 40, 30), image.Rect(60, 40, 90, 60)}, func(p render.Pass) {
		p.FillRect(bounds(b), render.Color{G: 0x60, A: 0x80})
		p.DrawTexture(tex, render.FRect{W: 8, H: 6}, bounds(b), render.Rotate180, 1)
	})
	golden.Assert(t, "damage", b)
}

func BenchmarkDrawTexture(b *testing.B) {
	r := soft.New()
	data := make([]byte, 1920*1080*4)
	tex, err := r.NewTexture(render.XRGB8888, image.Pt(1920, 1080), 1920*4, data)
	if err != nil {
		b.Fatal(err)
	}
	out := soft.NewBuffer(1920, 1080)
	b.ReportAllocs()
	for b.Loop() {
		p, err := r.Begin(out, full(out))
		if err != nil {
			b.Fatal(err)
		}
		p.DrawTexture(tex, render.FRect{W: 1920, H: 1080}, bounds(out), render.Normal, 1)
		if err := p.End(); err != nil {
			b.Fatal(err)
		}
	}
}
