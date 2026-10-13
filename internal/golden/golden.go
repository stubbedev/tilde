// Package golden compares rendered frames with PNG images under testdata.
package golden

import (
	"bytes"
	"flag"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/tilde/internal/render/soft"
)

var update = flag.Bool("update", false, "rewrite golden images instead of comparing against them")

func Assert(t testing.TB, name string, b *soft.Buffer) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, b.W, b.H))
	for y := range b.H {
		for x := range b.W {
			p := b.Pix[y*b.Stride+x*4 : y*b.Stride+x*4+4]
			if p[3] != 0xff {
				t.Fatalf("golden %s: pixel (%d, %d) is not opaque; goldens hold opaque frames only", name, x, y)
			}
			copy(img.Pix[y*img.Stride+x*4:], []byte{p[2], p[1], p[0], 0xff})
		}
	}
	path := filepath.Join("testdata", name+".png")
	if *update {
		var enc bytes.Buffer
		if err := png.Encode(&enc, img); err != nil {
			t.Fatalf("golden %s: encode: %v", name, err)
		}
		if err := os.WriteFile(path, enc.Bytes(), 0o600); err != nil {
			t.Fatalf("golden %s: %v", name, err)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run go test -update to create it)", name, err)
	}
	defer f.Close()
	decoded, err := png.Decode(f)
	if err != nil {
		t.Fatalf("golden %s: decode %s: %v", name, path, err)
	}
	if decoded.Bounds() != img.Bounds() {
		t.Fatalf("golden %s: golden is %v, frame is %v", name, decoded.Bounds(), img.Bounds())
	}
	want := image.NewNRGBA(decoded.Bounds())
	for y := range b.H {
		for x := range b.W {
			want.Set(x, y, decoded.At(x, y))
		}
	}
	for y := range b.H {
		row := y * img.Stride
		if !bytes.Equal(img.Pix[row:row+b.W*4], want.Pix[row:row+b.W*4]) {
			for x := range b.W {
				if !bytes.Equal(img.Pix[row+x*4:row+x*4+4], want.Pix[row+x*4:row+x*4+4]) {
					t.Fatalf("golden %s: pixel (%d, %d) is %v, golden has %v", name, x, y, img.Pix[row+x*4:row+x*4+4], want.Pix[row+x*4:row+x*4+4])
				}
			}
		}
	}
}
