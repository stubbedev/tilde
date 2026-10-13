package main

import "testing"

func TestParseDebug(t *testing.T) {
	if d, err := parseDebug(""); err != nil || d.wire {
		t.Fatalf("empty = %+v, %v", d, err)
	}
	if d, err := parseDebug("wire"); err != nil || !d.wire {
		t.Fatalf("wire = %+v, %v", d, err)
	}
	if _, err := parseDebug("wire,nope"); err == nil {
		t.Fatal("unknown category accepted")
	}
}

func TestChooseBackend(t *testing.T) {
	cases := []struct {
		requested, display, want string
	}{
		{"", "wayland-1", "nested"},
		{"", "", "headless"},
		{"headless", "wayland-1", "headless"},
		{"nested", "", "nested"},
	}
	for _, c := range cases {
		if got, err := chooseBackend(c.requested, c.display); err != nil || got != c.want {
			t.Errorf("chooseBackend(%q, %q) = %q, %v; want %q", c.requested, c.display, got, err, c.want)
		}
	}
	if _, err := chooseBackend("drm", ""); err == nil {
		t.Error("an unknown backend was accepted")
	}
}
