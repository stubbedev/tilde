package session

import (
	"strings"
	"testing"
)

func probe(available bool, def float64, status int32) (*knob[float64], *[]float64) {
	var set []float64
	return &knob[float64]{name: "accel_speed", available: available, def: func() float64 { return def }, set: func(v float64) int32 {
		set = append(set, v)
		return status
	}}, &set
}

func TestKnobLayers(t *testing.T) {
	for _, c := range []struct {
		name            string
		class, override *float64
		want            float64
	}{
		{"default without config", nil, nil, -0.25},
		{"class over default", new(0.5), nil, 0.5},
		{"device over class", new(0.5), new(-1.0), -1},
		{"device alone", nil, new(1.0), 1},
	} {
		k, set := probe(true, -0.25, configStatusSuccess)
		if err := k.apply("mouse", c.class, c.override); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(*set) != 1 || (*set)[0] != c.want {
			t.Fatalf("%s: set %v, want [%g]", c.name, *set, c.want)
		}
	}
}

func TestKnobUnavailable(t *testing.T) {
	k, set := probe(false, 0, configStatusSuccess)
	if err := k.apply("keyboard", new(0.5), nil); err != nil {
		t.Fatalf("a class setting on a device without it: %v", err)
	}
	err := k.apply("keyboard", nil, new(0.5))
	if err == nil || !strings.Contains(err.Error(), `"keyboard" has no accel_speed`) {
		t.Fatalf("a device setting the device lacks: %v", err)
	}
	if len(*set) != 0 {
		t.Fatalf("set %v on a device without the setting", *set)
	}
}

func TestKnobRejected(t *testing.T) {
	k, _ := probe(true, 0, 1)
	err := k.apply("touchpad", new(2.0), nil)
	if err == nil || !strings.Contains(err.Error(), `"touchpad" rejects accel_speed 2`) {
		t.Fatalf("a rejected value: %v", err)
	}
}
