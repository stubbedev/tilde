package backend

import "testing"

type idleQueue []func() error

func (q *idleQueue) add(f func() error) { *q = append(*q, f) }

func (q *idleQueue) run(t *testing.T) {
	t.Helper()
	for len(*q) > 0 {
		f := (*q)[0]
		*q = (*q)[1:]
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAFrameClockFiresOnceForManyRequests(t *testing.T) {
	var q idleQueue
	frames := 0
	clock := NewFrameClock(q.add, func() bool { return true }, func() error { frames++; return nil })
	clock.Request()
	clock.Request()
	clock.Kick()
	if len(q) != 1 {
		t.Fatalf("%d idle callbacks queued for three requests, want 1", len(q))
	}
	q.run(t)
	if frames != 1 || clock.Requested() {
		t.Fatalf("%d frames, requested %t after the idle callback", frames, clock.Requested())
	}
	q.run(t)
	if frames != 1 {
		t.Fatalf("%d frames without a new request", frames)
	}
}

func TestAFrameClockWaitsUntilReady(t *testing.T) {
	var q idleQueue
	ready, frames := false, 0
	clock := NewFrameClock(q.add, func() bool { return ready }, func() error { frames++; return nil })
	clock.Request()
	if len(q) != 0 {
		t.Fatal("a request queued a frame while the output was not ready")
	}
	ready = true
	clock.Kick()
	ready = false
	q.run(t)
	if frames != 0 || !clock.Requested() {
		t.Fatalf("%d frames once readiness went away before the idle callback; requested %t", frames, clock.Requested())
	}
	ready = true
	clock.Kick()
	q.run(t)
	if frames != 1 {
		t.Fatalf("%d frames once ready again, want 1", frames)
	}
}

func TestACancelledFrameRequestNeverFires(t *testing.T) {
	var q idleQueue
	frames := 0
	clock := NewFrameClock(q.add, func() bool { return true }, func() error { frames++; return nil })
	clock.Request()
	clock.Cancel()
	q.run(t)
	if frames != 0 || clock.Requested() {
		t.Fatalf("%d frames after a cancel, requested %t", frames, clock.Requested())
	}
	clock.Request()
	q.run(t)
	if frames != 1 {
		t.Fatalf("%d frames after a request following a cancel, want 1", frames)
	}
}
