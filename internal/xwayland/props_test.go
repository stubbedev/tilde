package xwayland

import (
	"encoding/binary"
	"image"
	"testing"
)

func words(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.LittleEndian.AppendUint32(b, x)
	}
	return b
}

func TestParseClass(t *testing.T) {
	c, err := parseClass([]byte("steamwebhelper\x00Steam\x00"))
	if err != nil || c != (Class{Instance: "steamwebhelper", Class: "Steam"}) {
		t.Fatalf("%+v, %v", c, err)
	}
	if c, err := parseClass([]byte("caf\xe9\x00Caf\xe9")); err != nil || c.Class != "Café" {
		t.Fatalf("Latin-1 class: %+v, %v", c, err)
	}
	if _, err := parseClass([]byte("lonely")); err == nil {
		t.Fatal("a class without an instance was accepted")
	}
}

func TestParseHints(t *testing.T) {
	for _, c := range []struct {
		data []byte
		want Hints
	}{
		{words(0, 0), Hints{Input: true}},
		{words(hintInput, 0), Hints{Input: false}},
		{words(hintInput|hintUrgency, 1, 0, 0, 0, 0, 0, 0, 0), Hints{Input: true, Urgent: true}},
	} {
		got, err := parseHints(c.data)
		if err != nil || got != c.want {
			t.Errorf("%v: %+v, %v, want %+v", c.data, got, err, c.want)
		}
	}
	if _, err := parseHints(words(1)); err == nil {
		t.Error("a one-word WM_HINTS was accepted")
	}
}

func TestParseSizeHints(t *testing.T) {
	fixed, err := parseSizeHints(words(sizeMin|sizeMax, 0, 0, 0, 0, 300, 200, 300, 200, 0, 0, 0, 0, 0, 0, 0, 0, 0))
	if err != nil || !fixed.Fixed() || fixed.Min != image.Pt(300, 200) {
		t.Fatalf("%+v, %v", fixed, err)
	}
	loose, err := parseSizeHints(words(sizeMin, 0, 0, 0, 0, 300, 200, 900, 900))
	if err != nil || loose.Fixed() || loose.Max != (image.Point{}) {
		t.Fatalf("%+v, %v", loose, err)
	}
	if _, err := parseSizeHints(words(1, 2, 3)); err == nil {
		t.Fatal("a short WM_NORMAL_HINTS was accepted")
	}
}
