// Package xcursor reads Xcursor files and resolves cursor themes the way
// libXcursor does: XCURSOR_PATH, theme inheritance and nominal sizes.
package xcursor

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	magic      = "Xcur"
	imageType  = 0xfffd0002
	headerSize = 16
	imageSize  = 36
	maxSide    = 0x7fff
)

type Image struct {
	Size    image.Point
	Hotspot image.Point
	Nominal int
	Pixels  []byte
}

func Parse(data []byte) ([]Image, error) {
	le := binary.LittleEndian
	if len(data) < headerSize || string(data[:4]) != magic {
		return nil, errors.New("xcursor: not an Xcursor file")
	}
	header, ntoc := le.Uint32(data[4:]), le.Uint32(data[12:])
	if uint64(header)+uint64(ntoc)*12 > uint64(len(data)) {
		return nil, fmt.Errorf("xcursor: table of contents with %d entries does not fit %d bytes", ntoc, len(data))
	}
	var images []Image
	for i := range int(ntoc) {
		entry := data[int(header)+i*12:]
		if le.Uint32(entry) != imageType {
			continue
		}
		img, err := parseImage(data, le.Uint32(entry[8:]))
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	if len(images) == 0 {
		return nil, errors.New("xcursor: no images")
	}
	return images, nil
}

func parseImage(data []byte, pos uint32) (Image, error) {
	le := binary.LittleEndian
	if uint64(pos)+imageSize > uint64(len(data)) {
		return Image{}, fmt.Errorf("xcursor: image header at %d runs past %d bytes", pos, len(data))
	}
	h := data[pos:]
	if le.Uint32(h) != imageSize || le.Uint32(h[4:]) != imageType {
		return Image{}, fmt.Errorf("xcursor: malformed image header at %d", pos)
	}
	nominal, w, ht, xhot, yhot := le.Uint32(h[8:]), le.Uint32(h[16:]), le.Uint32(h[20:]), le.Uint32(h[24:]), le.Uint32(h[28:])
	if w == 0 || ht == 0 || w > maxSide || ht > maxSide || xhot > w || yhot > ht || nominal > maxSide {
		return Image{}, fmt.Errorf("xcursor: image %dx%d with hotspot %d,%d at %d is invalid", w, ht, xhot, yhot, pos)
	}
	start := int(pos) + imageSize
	end := start + int(w)*int(ht)*4
	if end > len(data) {
		return Image{}, fmt.Errorf("xcursor: %dx%d pixels at %d run past %d bytes", w, ht, pos, len(data))
	}
	return Image{
		Size:    image.Pt(int(w), int(ht)),
		Hotspot: image.Pt(int(xhot), int(yhot)),
		Nominal: int(nominal),
		Pixels:  bytes.Clone(data[start:end]),
	}, nil
}

func SearchPath(getenv func(string) string) []string {
	if p := getenv("XCURSOR_PATH"); p != "" {
		return filepath.SplitList(p)
	}
	var dirs []string
	if home := getenv("HOME"); home != "" {
		data := getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		dirs = append(dirs, filepath.Join(data, "icons"), filepath.Join(home, ".icons"))
	}
	xdg := getenv("XDG_DATA_DIRS")
	if xdg == "" {
		xdg = "/usr/local/share:/usr/share"
	}
	for _, d := range filepath.SplitList(xdg) {
		dirs = append(dirs, filepath.Join(d, "icons"))
	}
	return append(dirs, "/usr/share/pixmaps")
}

type Theme struct {
	name  string
	dirs  []string
	cache map[string][]Image
}

func Open(name string, searchPath []string) (*Theme, error) {
	t := &Theme{name: name, cache: map[string][]Image{}}
	if err := t.resolve(name, searchPath, map[string]bool{}); err != nil {
		return nil, err
	}
	if len(t.dirs) == 0 {
		return nil, fmt.Errorf("xcursor: no cursors for theme %q in %v", name, searchPath)
	}
	return t, nil
}

func (t *Theme) resolve(name string, searchPath []string, visited map[string]bool) error {
	if visited[name] {
		return nil
	}
	visited[name] = true
	var inherits []string
	for _, base := range searchPath {
		dir := filepath.Join(base, name)
		if info, err := os.Stat(filepath.Join(dir, "cursors")); err == nil && info.IsDir() && !slices.Contains(t.dirs, filepath.Join(dir, "cursors")) {
			t.dirs = append(t.dirs, filepath.Join(dir, "cursors"))
		}
		parents, err := readInherits(filepath.Join(dir, "index.theme"))
		if err != nil {
			return err
		}
		inherits = append(inherits, parents...)
	}
	for _, parent := range inherits {
		if err := t.resolve(parent, searchPath, visited); err != nil {
			return err
		}
	}
	return nil
}

func readInherits(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // theme files come from the user's XCURSOR_PATH
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("xcursor: open %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.TrimSpace(key) == "Inherits" {
			var out []string
			for p := range strings.FieldsFuncSeq(value, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
				out = append(out, p)
			}
			return out, nil
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("xcursor: read %s: %w", path, err)
	}
	return nil, nil
}

func (t *Theme) images(name string) ([]Image, error) {
	if imgs, ok := t.cache[name]; ok {
		return imgs, nil
	}
	for _, dir := range t.dirs {
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // theme files come from the user's XCURSOR_PATH
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("xcursor: read %s from theme %q: %w", name, t.name, err)
		}
		imgs, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("xcursor: %s in theme %q: %w", name, t.name, err)
		}
		t.cache[name] = imgs
		return imgs, nil
	}
	return nil, nil
}

func (t *Theme) Load(size int, names ...string) (Image, error) {
	for _, name := range names {
		imgs, err := t.images(name)
		if err != nil {
			return Image{}, err
		}
		if len(imgs) == 0 {
			continue
		}
		best := imgs[0]
		for _, img := range imgs[1:] {
			if dist(img.Nominal, size) < dist(best.Nominal, size) {
				best = img
			}
		}
		return best, nil
	}
	return Image{}, fmt.Errorf("xcursor: theme %q has none of %v", t.name, names)
}

func dist(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
