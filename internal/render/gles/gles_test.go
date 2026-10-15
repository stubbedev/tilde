package gles_test

import (
	"bytes"
	"errors"
	"image"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

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
		if err := errors.Join(a.Destroy(), x.Destroy()); err != nil {
			t.Error(err)
		}
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

func TestOverlappingDamageBlendsOnce(t *testing.T) {
	r := renderer(t)
	fb, err := r.NewFramebuffer(image.Pt(4, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fb.Destroy(); err != nil {
			t.Error(err)
		}
	})
	all := image.Rect(0, 0, 4, 1)
	paint(t, r, fb, render.Region{all}, func(p render.Pass) { p.FillRect(all, render.Color{A: 0xff}) })
	paint(t, r, fb, render.Region{image.Rect(0, 0, 3, 1), image.Rect(1, 0, 4, 1), image.Rect(1, 0, 2, 1)}, func(p render.Pass) {
		p.FillRect(all, render.Color{R: 0x80, A: 0x80})
	})
	pix := make([]byte, 16)
	if err := fb.Read(pix, 16); err != nil {
		t.Fatal(err)
	}
	for x := range 4 {
		if got := pix[4*x : 4*x+4]; got[2] < 0x7f || got[2] > 0x81 || got[3] != 0xff {
			t.Fatalf("pixel %d is %v, want one blend of half-transparent red over black", x, got)
		}
	}
}

func TestParityWithTheSoftwareRenderer(t *testing.T) {
	parity(t, renderer(t))
}

func hardware(t *testing.T) (*gles.Renderer, string) {
	t.Helper()
	var node string
	r := rendererOn(t, func(lib *gpu.Library) (*gpu.Display, error) {
		devices, err := lib.Devices()
		if err != nil {
			return nil, err
		}
		for _, dev := range devices {
			if dev.RenderNode() != "" && !dev.Software() {
				if d, err := lib.OnDevice(dev); err == nil {
					node = dev.RenderNode()
					return d, nil
				}
			}
		}
		t.Skip("no EGL vendor drives a render node here; unset __EGL_VENDOR_LIBRARY_DIRS on a GPU machine to run this")
		return nil, nil
	})
	return r, node
}

func TestParityOnHardware(t *testing.T) {
	r, _ := hardware(t)
	parity(t, r)
}

func TestDMABUFTexturesOnHardware(t *testing.T) {
	g, node := hardware(t)
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	lib, err := gpu.LoadGBM(gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	dev, err := lib.Open(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	bo, err := dev.Allocate(image.Pt(8, 8), gpu.FormatARGB8888, nil, gpu.UseRendering)
	if err != nil {
		t.Fatal(err)
	}
	defer bo.Close()
	client, err := g.ImportFramebuffer(bo.DMABUF)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Destroy()
	paint(t, g, client, render.Region{image.Rect(0, 0, 8, 8)}, func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 8, 8), render.Color{B: 0x80, A: 0x80})
		p.FillRect(image.Rect(0, 0, 4, 8), render.Color{R: 0xff, A: 0xff})
	})
	if err := g.Finish(); err != nil {
		t.Fatal(err)
	}
	tex, err := g.ImportTexture(bo.DMABUF)
	if err != nil {
		t.Fatal(err)
	}
	defer tex.Destroy()
	if err := tex.Update(32, make([]byte, 256), nil); err == nil {
		t.Fatal("a dmabuf texture accepted an upload")
	}
	fb := framebuffer(t, g, image.Pt(16, 8))
	paint(t, g, fb, render.Region{image.Rect(0, 0, 16, 8)}, func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 16, 8), render.Color{G: 0xff, A: 0xff})
		p.DrawTexture(tex, render.FRect{W: 8, H: 8}, image.Rect(8, 0, 16, 8), render.Flipped, 1)
	})
	got := make([]byte, 16*8*4)
	if err := fb.Read(got, 64); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		x    int
		want [4]byte
	}{{2, [4]byte{0, 0xff, 0, 0xff}}, {10, [4]byte{0x80, 0x7f, 0, 0xff}}, {14, [4]byte{0, 0, 0xff, 0xff}}} {
		px := [4]byte(got[4*64+c.x*4:])
		if !near(px, c.want) {
			t.Errorf("x=%d: %v, want %v", c.x, px, c.want)
		}
	}
}

func near(a, b [4]byte) bool {
	for i := range a {
		if d := int(a[i]) - int(b[i]); d < -1 || d > 1 {
			return false
		}
	}
	return true
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

	cleared := render.Region{image.Rect(3, 4, 21, 15)}
	clear := func(p render.Pass) {
		p.Clear(render.Color{B: 0x20, A: 0x40})
		p.FillRect(image.Rect(0, 0, 10, 10), render.Color{R: 0x40, A: 0x80})
	}
	paint(t, g, fb, cleared, clear)
	paint(t, s, buf, cleared, clear)
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

func TestReadKeepsTheStridePadding(t *testing.T) {
	g := renderer(t)
	fb := framebuffer(t, g, image.Pt(5, 3))
	paint(t, g, fb, render.Region{image.Rect(0, 0, 5, 3)}, func(p render.Pass) {
		p.FillRect(image.Rect(0, 0, 5, 3), render.Color{B: 0xff, A: 0xff})
		p.FillRect(image.Rect(1, 0, 2, 1), render.Color{R: 0xff, A: 0xff})
		p.FillRect(image.Rect(4, 2, 5, 3), render.Color{G: 0xff, A: 0xff})
	})
	const stride = 32
	got := bytes.Repeat([]byte{0xaa}, stride*3)
	if err := fb.Read(got, stride); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		at   image.Point
		want [4]byte
	}{{image.Pt(0, 0), [4]byte{0xff, 0, 0, 0xff}}, {image.Pt(1, 0), [4]byte{0, 0, 0xff, 0xff}}, {image.Pt(4, 2), [4]byte{0, 0xff, 0, 0xff}}, {image.Pt(3, 1), [4]byte{0xff, 0, 0, 0xff}}} {
		if px := [4]byte(got[c.at.Y*stride+c.at.X*4:]); px != c.want {
			t.Errorf("%v: %v, want %v", c.at, px, c.want)
		}
	}
	for y := range 3 {
		if pad := got[y*stride+20 : (y+1)*stride]; !bytes.Equal(pad, bytes.Repeat([]byte{0xaa}, len(pad))) {
			t.Errorf("row %d padding was overwritten: %v", y, pad)
		}
	}
	if err := fb.Read(make([]byte, 21*3), 21); err == nil {
		t.Error("a stride that is not a whole number of pixels was accepted")
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

func TestExternalYUVTexturesOnHardware(t *testing.T) {
	g, node := hardware(t)
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	lib, err := gpu.LoadGBM(gpu.PathsFromEnv(os.Getenv))
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	dev, err := lib.Open(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	nv12 := uint32('N') | uint32('V')<<8 | uint32('1')<<16 | uint32('2')<<24
	bo, err := dev.Allocate(image.Pt(16, 16), nv12, nil, gpu.UseLinear)
	if err != nil {
		t.Skipf("%s cannot allocate linear NV12: %v", dev.Backend(), err)
	}
	defer bo.Close()
	for i, p := range bo.Planes {
		rows, value := 16, byte(126)
		if i == 1 {
			rows, value = 8, 128
		}
		size := int(p.Offset) + rows*int(p.Stride)
		m, err := unix.Mmap(p.FD, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			t.Skipf("map NV12 plane %d: %v", i, err)
		}
		for j := int(p.Offset); j < size; j++ {
			m[j] = value
		}
		if err := unix.Munmap(m); err != nil {
			t.Fatal(err)
		}
	}
	tex, err := g.ImportTexture(bo.DMABUF)
	if err != nil {
		t.Fatal(err)
	}
	defer tex.Destroy()
	fb := framebuffer(t, g, image.Pt(16, 16))
	paint(t, g, fb, render.Region{image.Rect(0, 0, 16, 16)}, func(p render.Pass) {
		p.DrawTexture(tex, render.FRect{W: 16, H: 16}, image.Rect(0, 0, 16, 16), render.Normal, 1)
	})
	got := make([]byte, 16*16*4)
	if err := fb.Read(got, 64); err != nil {
		t.Fatal(err)
	}
	px := got[8*64+8*4:][:4]
	for c := range 3 {
		if d := int(px[c]) - 128; d < -4 || d > 4 {
			t.Fatalf("mid-gray NV12 sampled as %v", px)
		}
	}
}
