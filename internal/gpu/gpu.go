// Package gpu binds libEGL, libGLESv2 and libgbm through purego: displays on
// the GBM, device and surfaceless platforms, GLES2 contexts, dmabuf images,
// native fences and buffer allocation.
package gpu

import (
	"fmt"
	"image"

	"github.com/ebitengine/purego"
)

const (
	EGLEnv    = "TILDE_LIBEGL"
	GLESv2Env = "TILDE_LIBGLESV2"
	GBMEnv    = "TILDE_LIBGBM"
)

var (
	defaultEGL    = "libEGL.so.1"
	defaultGLESv2 = "libGLESv2.so.2"
	defaultGBM    = "libgbm.so.1"
)

type Paths struct {
	EGL, GLESv2, GBM string
}

func PathsFromEnv(getenv func(string) string) Paths {
	pick := func(env, fallback string) string {
		if p := getenv(env); p != "" {
			return p
		}
		return fallback
	}
	return Paths{EGL: pick(EGLEnv, defaultEGL), GLESv2: pick(GLESv2Env, defaultGLESv2), GBM: pick(GBMEnv, defaultGBM)}
}

func open(path, env string) (uintptr, error) {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return 0, fmt.Errorf("gpu: load %s (set %s to its path): %w", path, env, err)
	}
	return h, nil
}

type symbol struct {
	fn   any
	name string
}

func bind(lib uintptr, path string, syms []symbol) error {
	for _, s := range syms {
		if _, err := purego.Dlsym(lib, s.name); err != nil {
			return fmt.Errorf("gpu: %s lacks %s: %w", path, s.name, err)
		}
		purego.RegisterLibFunc(s.fn, lib, s.name)
	}
	return nil
}

type Plane struct {
	FD     int
	Offset uint32
	Stride uint32
}

type DMABUF struct {
	Size     image.Point
	Format   uint32
	Modifier uint64
	Planes   []Plane
}

const (
	FormatARGB8888 uint32 = 'A' | 'R'<<8 | '2'<<16 | '4'<<24
	FormatXRGB8888 uint32 = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
	FormatABGR8888 uint32 = 'A' | 'B'<<8 | '2'<<16 | '4'<<24
	FormatXBGR8888 uint32 = 'X' | 'B'<<8 | '2'<<16 | '4'<<24

	ModifierLinear  uint64 = 0
	ModifierInvalid uint64 = 0x00ffffffffffffff
)

type FormatModifier struct {
	Format   uint32
	Modifier uint64
}
