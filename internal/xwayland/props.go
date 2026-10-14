package xwayland

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"strings"
	"unicode/utf8"
)

type Class struct {
	Instance, Class string
}

func parseClass(data []byte) (Class, error) {
	parts := bytes.SplitN(bytes.TrimRight(data, "\x00"), []byte{0}, 2)
	if len(parts) != 2 {
		return Class{}, fmt.Errorf("WM_CLASS %q is not two NUL-terminated strings", data)
	}
	return Class{Instance: latin1(parts[0]), Class: latin1(parts[1])}, nil
}

func latin1(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

func parseUTF8(data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%q is not UTF-8", data)
	}
	return string(data), nil
}

func cardinals(data []byte, what string, min int) ([]uint32, error) {
	if len(data)%4 != 0 || len(data)/4 < min {
		return nil, fmt.Errorf("%s has %d bytes, want at least %d 32-bit values", what, len(data), min)
	}
	out := make([]uint32, len(data)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(data[i*4:])
	}
	return out, nil
}

const (
	hintInput   = 1 << 0
	hintUrgency = 1 << 8
)

type Hints struct {
	Input, Urgent bool
}

func parseHints(data []byte) (Hints, error) {
	v, err := cardinals(data, "WM_HINTS", 2)
	if err != nil {
		return Hints{}, err
	}
	h := Hints{Input: true, Urgent: v[0]&hintUrgency != 0}
	if v[0]&hintInput != 0 {
		h.Input = v[1] != 0
	}
	return h, nil
}

const (
	sizeMin = 1 << 4
	sizeMax = 1 << 5
)

type SizeHints struct {
	Min, Max image.Point
}

func (h SizeHints) Fixed() bool {
	return h.Min != (image.Point{}) && h.Min == h.Max
}

func parseSizeHints(data []byte) (SizeHints, error) {
	v, err := cardinals(data, "WM_NORMAL_HINTS", 9)
	if err != nil {
		return SizeHints{}, err
	}
	var h SizeHints
	if v[0]&sizeMin != 0 {
		h.Min = image.Pt(int(int32(v[5])), int(int32(v[6]))) //nolint:gosec // sizes travel as signed 32-bit values
	}
	if v[0]&sizeMax != 0 {
		h.Max = image.Pt(int(int32(v[7])), int(int32(v[8]))) //nolint:gosec // sizes travel as signed 32-bit values
	}
	return h, nil
}
