package backend

import (
	"image"
	"testing"
	"time"
)

func TestMode(t *testing.T) {
	m := Mode{Size: image.Pt(1920, 1080), RefreshMHz: 60000}
	if err := m.Valid(); err != nil {
		t.Fatal(err)
	}
	if got := m.Interval(); got != 16666666*time.Nanosecond {
		t.Fatalf("60 Hz interval = %v", got)
	}
	for _, bad := range []Mode{{Size: image.Pt(0, 1), RefreshMHz: 1}, {Size: image.Pt(1, 1)}, {Size: image.Pt(1, -1), RefreshMHz: 1}} {
		if bad.Valid() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestNowIsMonotonic(t *testing.T) {
	a := Now()
	b := Now()
	if a <= 0 || b < a {
		t.Fatalf("Now went %v then %v", a, b)
	}
}

func TestCapability(t *testing.T) {
	c := Keyboard | Touch
	if !c.Has(Keyboard) || !c.Has(Touch) || c.Has(Pointer) || !c.Has(Keyboard|Touch) {
		t.Fatalf("Has is wrong for %b", c)
	}
}
