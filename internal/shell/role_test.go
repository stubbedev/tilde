package shell

import "testing"

func TestARoleConfiguresBeforeItMaps(t *testing.T) {
	var r roleLife
	steps := []struct {
		ack, buffer bool
		step        roleStep
		configured  bool
	}{
		{false, true, stepNone, false},
		{false, false, stepInitial, true},
		{false, true, stepNone, false},
		{true, false, stepNone, true},
		{false, true, stepMap, true},
		{false, true, stepNone, true},
		{false, false, stepUnmap, true},
		{true, true, stepNone, false},
		{false, false, stepInitial, true},
	}
	for i, want := range steps {
		if want.ack {
			r.acked()
		}
		step, configured := r.commit(want.buffer)
		if step != want.step || configured != want.configured {
			t.Fatalf("commit %d (ack %t, buffer %t): step %d configured %t, want %d %t", i, want.ack, want.buffer, step, configured, want.step, want.configured)
		}
	}
}

func TestConfiguresRememberWhatWasLastSent(t *testing.T) {
	var q configures[int]
	if _, ok := q.last(); ok {
		t.Fatal("nothing sent, yet a last configure")
	}
	q.push(1, 10)
	q.push(2, 20)
	if v, ok := q.last(); !ok || v != 20 {
		t.Fatalf("last %d, %t, want 20", v, ok)
	}
	q.reset()
	if _, ok := q.last(); ok {
		t.Fatal("a reset kept the last configure")
	}
}
