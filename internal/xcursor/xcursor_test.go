package xcursor

import (
	"encoding/binary"
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type entry struct {
	nominal      uint32
	size, hot    image.Point
	fill         byte
	notAnImage   bool
	headerLength uint32
}

func encode(entries ...entry) []byte {
	le := binary.LittleEndian
	out := le.AppendUint32([]byte(magic), headerSize)
	out = le.AppendUint32(out, 0x10000)
	out = le.AppendUint32(out, uint32(len(entries)))
	pos := headerSize + 12*len(entries)
	var body []byte
	for _, e := range entries {
		kind := uint32(imageType)
		if e.notAnImage {
			kind = 0xfffe0001
		}
		out = le.AppendUint32(le.AppendUint32(le.AppendUint32(out, kind), e.nominal), uint32(pos+len(body)))
		length := e.headerLength
		if length == 0 {
			length = imageSize
		}
		chunk := le.AppendUint32(nil, length)
		for _, v := range []uint32{kind, e.nominal, 1, uint32(e.size.X), uint32(e.size.Y), uint32(e.hot.X), uint32(e.hot.Y), 0} {
			chunk = le.AppendUint32(chunk, v)
		}
		if !e.notAnImage {
			for range e.size.X * e.size.Y * 4 {
				chunk = append(chunk, e.fill)
			}
		}
		body = append(body, chunk...)
	}
	return append(out, body...)
}

func TestParseReadsEveryImage(t *testing.T) {
	data := encode(
		entry{nominal: 24, size: image.Pt(2, 3), hot: image.Pt(1, 2), fill: 7},
		entry{nominal: 1, notAnImage: true},
		entry{nominal: 48, size: image.Pt(4, 4), hot: image.Pt(4, 4), fill: 9},
	)
	imgs, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 2 {
		t.Fatalf("%d images, want 2 with the comment skipped", len(imgs))
	}
	first := imgs[0]
	if first.Nominal != 24 || first.Size != image.Pt(2, 3) || first.Hotspot != image.Pt(1, 2) || len(first.Pixels) != 24 || first.Pixels[23] != 7 {
		t.Fatalf("first image %+v", first)
	}
	if imgs[1].Nominal != 48 || imgs[1].Hotspot != image.Pt(4, 4) {
		t.Fatalf("second image %+v", imgs[1])
	}
}

func TestParseRejectsMalformedFiles(t *testing.T) {
	good := encode(entry{nominal: 24, size: image.Pt(2, 2), fill: 1})
	cases := map[string][]byte{
		"empty":            nil,
		"bad magic":        append([]byte("Xcux"), good[4:]...),
		"no images":        encode(entry{nominal: 1, notAnImage: true}),
		"hotspot outside":  encode(entry{nominal: 24, size: image.Pt(2, 2), hot: image.Pt(3, 0)}),
		"zero size":        encode(entry{nominal: 24}),
		"bad image header": encode(entry{nominal: 24, size: image.Pt(1, 1), headerLength: 40}),
	}
	for n := 1; n < len(good); n++ {
		if _, err := Parse(good[:n]); err == nil {
			t.Fatalf("a file cut to %d of %d bytes parsed", n, len(good))
		}
	}
	for name, data := range cases {
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestThemesInheritAndPickTheNearestSize(t *testing.T) {
	user, system := t.TempDir(), t.TempDir()
	write(t, filepath.Join(user, "mine", "index.theme"), []byte("[Icon Theme]\nName=Mine\nInherits=base, mine\n"))
	write(t, filepath.Join(user, "mine", "cursors", "left_ptr"), encode(
		entry{nominal: 24, size: image.Pt(1, 1), fill: 1},
		entry{nominal: 48, size: image.Pt(1, 1), fill: 2},
	))
	write(t, filepath.Join(system, "base", "cursors", "xterm"), encode(entry{nominal: 32, size: image.Pt(1, 1), fill: 3}))
	theme, err := Open("mine", []string{user, system})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		size  int
		names []string
		fill  byte
	}{
		{30, Names("default"), 1},
		{40, Names("default"), 2},
		{24, Names("text"), 3},
	} {
		img, err := theme.Load(c.size, c.names...)
		if err != nil {
			t.Fatal(err)
		}
		if img.Pixels[0] != c.fill {
			t.Errorf("%v at %d: image filled with %d, want %d", c.names, c.size, img.Pixels[0], c.fill)
		}
	}
	if _, err := theme.Load(24, "nonexistent"); err == nil {
		t.Fatal("a missing cursor loaded")
	}
	if _, err := Open("absent", []string{user}); err == nil {
		t.Fatal("a theme without cursors opened")
	}
}

func TestBrokenThemeFilesFailLoudly(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "broken", "cursors", "left_ptr"), []byte("Xcur"))
	theme, err := Open("broken", []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := theme.Load(24, "left_ptr"); err == nil {
		t.Fatal("a truncated cursor file loaded")
	}
}

func TestSearchPath(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	if got := SearchPath(env(map[string]string{"XCURSOR_PATH": "/a:/b", "HOME": "/h"})); !slices.Equal(got, []string{"/a", "/b"}) {
		t.Fatalf("XCURSOR_PATH gives %v", got)
	}
	want := []string{"/h/.local/share/icons", "/h/.icons", "/d/icons", "/usr/share/pixmaps"}
	if got := SearchPath(env(map[string]string{"HOME": "/h", "XDG_DATA_DIRS": "/d"})); !slices.Equal(got, want) {
		t.Fatalf("defaults give %v, want %v", got, want)
	}
}

func TestBuiltinArrowHasItsTipAtTheHotspot(t *testing.T) {
	for _, size := range []int{16, 24, 36, 48} {
		img := Builtin(size)
		if img.Size != image.Pt(size, size) || img.Hotspot != (image.Point{}) || img.Nominal != size {
			t.Fatalf("size %d: %+v", size, img)
		}
		if img.Pixels[3] != 0xff {
			t.Fatalf("size %d: the tip pixel is transparent", size)
		}
	}
}

func TestThemesThatInheritEachOtherStillOpen(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "index.theme"), []byte("[Icon Theme]\nInherits=b\n"))
	write(t, filepath.Join(dir, "b", "index.theme"), []byte("[Icon Theme]\nInherits=a\n"))
	write(t, filepath.Join(dir, "b", "cursors", "left_ptr"), encode(entry{nominal: 24, size: image.Pt(1, 1), fill: 7}))
	theme, err := Open("a", []string{dir})
	if err != nil {
		t.Fatalf("a theme inheriting itself through another: %v", err)
	}
	if img, err := theme.Load(24, Names("default")...); err != nil || img.Pixels[0] != 7 {
		t.Fatalf("Load = %v, %v", img, err)
	}
}
