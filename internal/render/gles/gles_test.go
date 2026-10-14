package gles_test

import (
	"image"
	"strings"
	"testing"

	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/render/gles"
	"github.com/stubbedev/tilde/internal/render/soft"
	"github.com/stubbedev/tilde/internal/testenv"
)

func renderer(t *testing.T) *gles.Renderer {
	t.Helper()
	return rendererOn(t, func(lib *gpu.Library) (*gpu.Display, error) { return lib.Surfaceless() })
}

func rendererOn(t *testing.T, open func(*gpu.Library) (*gpu.Display, error)) *gles.Renderer {
	t.Helper()
	d, err := open(testenv.GPU(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	r, err := gles.New(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func framebuffer(t *testing.T, r *gles.Renderer, size image.Point) *gles.Framebuffer {
	t.Helper()
	f, err := r.NewFramebuffer(size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Destroy(); err != nil {
			t.Error(err)
		}
	})
	return f
}

var (
	texSize = image.Pt(7, 5)
	target  = image.Pt(64, 48)
)

func argb() []byte {
	pix := make([]byte, texSize.X*texSize.Y*4)
	for y := range texSize.Y {
		for x := range texSize.X {
			a := byte(255 - 30*x)
			if y == 0 {
				a = 255
			}
			p := pix[(y*texSize.X+x)*4:]
			p[0], p[1], p[2], p[3] = byte(int(a)*y/texSize.Y), byte(int(a)*x/texSize.X), a/2, a
		}
	}
	return pix
}

func xrgb() []byte {
	pix := argb()
	for i := 3; i < len(pix); i += 4 {
		pix[i] = byte(i)
	}
	return pix
}

type textures struct{ argb, xrgb render.Texture }

func load(t *testing.T, r render.Renderer) textures {
	t.Helper()
	a, err := r.NewTexture(render.ARGB8888, texSize, texSize.X*4, argb())
	if err != nil {
		t.Fatal(err)
	}
	x, err := r.NewTexture(render.XRGB8888, texSize, texSize.X*4, xrgb())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.Destroy()
		x.Destroy()
	})
	return textures{a, x}
}

func paint(t *testing.T, r render.Renderer, target render.Target, damage render.Region, draw func(render.Pass)) {
	t.Helper()
	p, err := r.Begin(target, damage)
	if err != nil {
		t.Fatal(err)
	}
	draw(p)
	if err := p.End(); err != nil {
		t.Fatal(err)
	}
}

func scene(tx textures) func(render.Pass) {
	return func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 64, 48), render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff})
		p.FillRect(image.Rect(4, 30, 40, 46), render.Color{R: 0x40, G: 0x20, B: 0x00, A: 0x80})
		for i := range render.Flipped270 + 1 {
			size := i.Size(texSize)
			at := image.Pt(1+(int(i)%4)*9, 1+int(i)/4*9)
			p.DrawTexture(tx.argb, render.FRect{W: 7, H: 5}, image.Rectangle{Min: at, Max: at.Add(size)}, i, 1)
		}
		p.DrawTexture(tx.xrgb, render.FRect{W: 7, H: 5}, image.Rect(40, 2, 47, 7), render.Normal, 0.5)
		p.DrawTexture(tx.argb, render.FRect{X: 1, Y: 1, W: 4, H: 3}, image.Rect(40, 10, 44, 13), render.Normal, 1)
		p.DrawTexture(tx.xrgb, render.FRect{W: 7, H: 5}, image.Rect(50, 20, 55, 27), render.Rotate90, 1)
	}
}

func compare(t *testing.T, want *soft.Buffer, got *gles.Framebuffer, tolerance int) {
	t.Helper()
	pix := make([]byte, len(want.Pix))
	if err := got.Read(pix, want.Stride); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for y := range want.H {
		for x := range want.W {
			i := y*want.Stride + x*4
			for c := range 4 {
				if d := int(pix[i+c]) - int(want.Pix[i+c]); d < -tolerance || d > tolerance {
					if bad < 10 {
						t.Errorf("(%d,%d): gles %v, soft %v", x, y, pix[i:i+4], want.Pix[i:i+4])
					}
					bad++
					break
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d pixels differ", bad)
	}
}

func TestParityWithTheSoftwareRenderer(t *testing.T) {
	parity(t, renderer(t))
}

func TestParityOnHardware(t *testing.T) {
	parity(t, rendererOn(t, func(lib *gpu.Library) (*gpu.Display, error) {
		devices, err := lib.Devices()
		if err != nil {
			return nil, err
		}
		for _, dev := range devices {
			if dev.RenderNode() != "" && !dev.Software() {
				if d, err := lib.OnDevice(dev); err == nil {
					return d, nil
				}
			}
		}
		t.Skip("no EGL vendor drives a render node here; unset __EGL_VENDOR_LIBRARY_DIRS on a GPU machine to run this")
		return nil, nil
	}))
}

func parity(t *testing.T, g *gles.Renderer) {
	t.Helper()
	s := soft.New()
	gtx, stx := load(t, g), load(t, s)
	fb, buf := framebuffer(t, g, target), soft.NewBuffer(target.X, target.Y)
	full := render.Region{image.Rectangle{Max: target}}
	paint(t, g, fb, full, scene(gtx))
	paint(t, s, buf, full, scene(stx))
	compare(t, buf, fb, 1)

	update := argb()
	for i := range update {
		update[i] = 0x10
	}
	damage := render.Region{image.Rect(2, 1, 5, 3)}
	for _, tx := range []render.Texture{gtx.argb, stx.argb} {
		if err := tx.Update(texSize.X*4, update, damage); err != nil {
			t.Fatal(err)
		}
	}
	partial := render.Region{image.Rect(0, 0, 20, 12), image.Rect(30, 20, 64, 48)}
	paint(t, g, fb, partial, scene(gtx))
	paint(t, s, buf, partial, scene(stx))
	compare(t, buf, fb, 1)
}

func TestScaledDrawsKeepFlatColor(t *testing.T) {
	g := renderer(t)
	pix := make([]byte, 4*4*4)
	for i := range pix {
		pix[i] = []byte{0x20, 0x40, 0x60, 0xff}[i%4]
	}
	tx, err := g.NewTexture(render.ARGB8888, image.Pt(4, 4), 16, pix)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Destroy()
	fb := framebuffer(t, g, image.Pt(20, 20))
	paint(t, g, fb, render.Region{image.Rect(0, 0, 20, 20)}, func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 20, 20), render.Color{A: 0xff})
		p.DrawTexture(tx, render.FRect{X: 0.5, Y: 0.5, W: 3, H: 3}, image.Rect(2, 2, 12, 17), render.Rotate270, 1)
	})
	got := make([]byte, 20*20*4)
	if err := fb.Read(got, 80); err != nil {
		t.Fatal(err)
	}
	for _, at := range []image.Point{{3, 3}, {7, 9}, {11, 16}} {
		if px := got[at.Y*80+at.X*4:][:4]; px[0] != 0x20 || px[1] != 0x40 || px[2] != 0x60 || px[3] != 0xff {
			t.Errorf("%v: %v", at, px)
		}
	}
	if px := got[18*80+18*4:][:4]; px[0] != 0 || px[3] != 0xff {
		t.Errorf("outside the draw: %v", px)
	}
}

func TestMisuseFailsPrecisely(t *testing.T) {
	g := renderer(t)
	other := renderer(t)
	if _, err := g.Begin(soft.NewBuffer(4, 4), nil); err == nil || !strings.Contains(err.Error(), "cannot render into *soft.Buffer") {
		t.Fatalf("a soft target: %v", err)
	}
	if _, err := g.NewTexture(render.ARGB8888, image.Pt(4, 4), 8, make([]byte, 64)); err == nil {
		t.Fatal("a stride shorter than a row was accepted")
	}
	tx := load(t, other).argb
	fb := framebuffer(t, g, image.Pt(8, 8))
	p, err := g.Begin(fb, render.Region{image.Rect(0, 0, 8, 8)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Begin(fb, nil); err == nil {
		t.Fatal("a second Begin was accepted")
	}
	p.DrawTexture(tx, render.FRect{W: 7, H: 5}, image.Rect(0, 0, 7, 5), render.Normal, 1)
	if err := p.End(); err == nil || !strings.Contains(err.Error(), "another renderer") {
		t.Fatalf("a foreign texture: %v", err)
	}
	p, err = g.Begin(fb, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.FillRect(image.Rect(0, 0, 1, 1), render.Color{R: 0xff, A: 0x10})
	p.DrawTexture(load(t, g).argb, render.FRect{W: 8, H: 5}, image.Rect(0, 0, 8, 5), render.Normal, 1)
	if err := p.End(); err == nil || !strings.Contains(err.Error(), "not premultiplied") {
		t.Fatalf("a straight-alpha color: %v", err)
	}
}
