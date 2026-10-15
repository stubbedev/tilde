package loop

import (
	"errors"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func newLoop(t *testing.T) *Loop {
	t.Helper()
	l, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return l
}

func pipe(t *testing.T) (int, int) {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Close(p[0])
		_ = unix.Close(p[1])
	})
	return p[0], p[1]
}

func isOpen(fd int) bool {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	return err == nil
}

func run(t *testing.T, l *Loop) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop")
	}
}

func TestFDSource(t *testing.T) {
	l := newLoop(t)
	r, w := pipe(t)
	var got []Events
	src, err := l.AddFD(r, Readable, func(ev Events) error {
		got = append(got, ev)
		var buf [16]byte
		_, _ = unix.Read(r, buf[:])
		l.Stop()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Write(w, []byte("x")); err != nil {
		t.Fatal(err)
	}
	run(t, l)
	if len(got) != 1 || got[0]&Readable == 0 {
		t.Fatalf("events = %v, want one Readable", got)
	}
	if err := src.Modify(Writable); err != nil {
		t.Fatal(err)
	}
	if err := src.Remove(); err != nil {
		t.Fatal(err)
	}
	if !isOpen(r) {
		t.Fatal("removing an fd source closed a descriptor the loop does not own")
	}
	if err := src.Modify(Readable); !errors.Is(err, ErrClosed) {
		t.Fatalf("Modify after Remove = %v, want ErrClosed", err)
	}
}

func TestWritableSource(t *testing.T) {
	l := newLoop(t)
	_, w := pipe(t)
	var got Events
	if _, err := l.AddFD(w, Writable, func(ev Events) error {
		got = ev
		l.Stop()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run(t, l)
	if got&Writable == 0 {
		t.Fatalf("events = %v, want Writable", got)
	}
}

func TestTimer(t *testing.T) {
	l := newLoop(t)
	fires := 0
	var timer *Timer
	timer, err := l.AddTimer(func() error {
		fires++
		if fires < 3 {
			return timer.Arm(time.Millisecond)
		}
		l.Stop()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := timer.Arm(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	run(t, l)
	if fires != 3 || time.Since(start) < 3*time.Millisecond {
		t.Fatalf("timer fired %d times in %v, want 3 re-armed one-shots", fires, time.Since(start))
	}
	fd := timer.fd
	if err := timer.Remove(); err != nil {
		t.Fatal(err)
	}
	if isOpen(fd) {
		t.Fatal("Remove left the timerfd open")
	}
	if err := timer.Arm(time.Millisecond); !errors.Is(err, ErrClosed) {
		t.Fatalf("Arm after Remove = %v, want ErrClosed", err)
	}
}

func TestDisarmedTimerDoesNotFire(t *testing.T) {
	l := newLoop(t)
	timer, err := l.AddTimer(func() error { return errors.New("disarmed timer fired") })
	if err != nil {
		t.Fatal(err)
	}
	if err := timer.Arm(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := timer.Disarm(); err != nil {
		t.Fatal(err)
	}
	stop, err := l.AddTimer(func() error {
		l.Stop()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stop.Arm(20 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	run(t, l)
}

func TestSignals(t *testing.T) {
	l := newLoop(t)
	var got []os.Signal
	sigs, err := l.AddSignals(func(s os.Signal) error {
		got = append(got, s)
		l.Stop()
		return nil
	}, syscall.SIGUSR1)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Kill(os.Getpid(), unix.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	run(t, l)
	if !slices.Equal(got, []os.Signal{syscall.SIGUSR1}) {
		t.Fatalf("signals = %v, want [SIGUSR1]", got)
	}
	sigs.Remove()
	if len(l.signals) != 0 {
		t.Fatal("Remove left the signal source registered")
	}
}

func TestMailboxRunsPostedClosuresInOrderOnTheLoop(t *testing.T) {
	l := newLoop(t)
	var got []int
	go func() {
		for i := range 100 {
			if err := l.Post(func() error {
				got = append(got, i)
				if i == 99 {
					l.Stop()
				}
				return nil
			}); err != nil {
				t.Error(err)
			}
		}
	}()
	run(t, l)
	if len(got) != 100 || !slices.IsSorted(got) {
		t.Fatalf("ran %d closures, sorted %v", len(got), slices.IsSorted(got))
	}
}

func TestIdleRunsBeforeBlockingUntilEmpty(t *testing.T) {
	l := newLoop(t)
	var got []string
	l.Idle(func() error {
		got = append(got, "first")
		l.Idle(func() error {
			got = append(got, "added while idle")
			l.Stop()
			return nil
		})
		return nil
	})
	l.Idle(func() error {
		got = append(got, "second")
		return nil
	})
	run(t, l)
	if want := []string{"first", "second", "added while idle"}; !slices.Equal(got, want) {
		t.Fatalf("idle order = %v, want %v", got, want)
	}
	if l.wakeups != 0 {
		t.Fatalf("idle work woke epoll %d times, want 0", l.wakeups)
	}
}

func TestBeforeWaitRunsEveryIterationAfterIdle(t *testing.T) {
	l := newLoop(t)
	var got []string
	l.Idle(func() error {
		got = append(got, "idle")
		return nil
	})
	l.BeforeWait(func() error {
		got = append(got, "before")
		if len(got) == 4 {
			l.Stop()
		}
		return nil
	})
	if err := l.Post(func() error {
		got = append(got, "posted")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run(t, l)
	if want := []string{"idle", "before", "posted", "before"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestHandlerErrorStopsRun(t *testing.T) {
	l := newLoop(t)
	boom := errors.New("boom")
	if err := l.Post(func() error { return boom }); err != nil {
		t.Fatal(err)
	}
	if err := l.Run(); !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want boom", err)
	}
}

func TestRemovedSourceSkipsItsPendingEvent(t *testing.T) {
	l := newLoop(t)
	r1, w1 := pipe(t)
	r2, w2 := pipe(t)
	var a, b *Source
	calls := 0
	handler := func(Events) error {
		calls++
		if err := a.Remove(); err != nil {
			return err
		}
		if err := b.Remove(); err != nil {
			return err
		}
		l.Stop()
		return nil
	}
	var err error
	if a, err = l.AddFD(r1, Readable, handler); err != nil {
		t.Fatal(err)
	}
	if b, err = l.AddFD(r2, Readable, handler); err != nil {
		t.Fatal(err)
	}
	for _, w := range []int{w1, w2} {
		if _, err := unix.Write(w, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	run(t, l)
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1: a removed source's pending event was dispatched", calls)
	}
}

func TestIdleLoopNeverWakes(t *testing.T) {
	l := newLoop(t)
	if _, err := l.AddTimer(func() error { return errors.New("disarmed timer fired") }); err != nil {
		t.Fatal(err)
	}
	r, _ := pipe(t)
	if _, err := l.AddFD(r, Readable, func(Events) error { return errors.New("silent fd fired") }); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AddSignals(func(os.Signal) error { return nil }, syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = l.Post(func() error {
			l.Stop()
			return nil
		})
	}()
	run(t, l)
	if l.wakeups != 1 {
		t.Fatalf("idle loop woke %d times in 500ms, want only the final post", l.wakeups)
	}
}

func TestCloseReleasesEverything(t *testing.T) {
	l, err := New()
	if err != nil {
		t.Fatal(err)
	}
	timer, err := l.AddTimer(func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	r, _ := pipe(t)
	if _, err := l.AddFD(r, Readable, func(Events) error { return nil }); err != nil {
		t.Fatal(err)
	}
	sigs, err := l.AddSignals(func(os.Signal) error { return nil }, syscall.SIGUSR2)
	if err != nil {
		t.Fatal(err)
	}
	owned := []int{l.epfd, l.mailboxFD, timer.fd}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for _, fd := range owned {
		if isOpen(fd) {
			t.Errorf("fd %d is still open after Close", fd)
		}
	}
	if !isOpen(r) {
		t.Error("Close closed a descriptor the loop does not own")
	}
	select {
	case <-sigs.done:
	default:
		t.Error("signal goroutine still running after Close")
	}
	if err := l.Post(func() error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("Post after Close = %v, want ErrClosed", err)
	}
	if _, err := l.AddTimer(func() error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("AddTimer after Close = %v, want ErrClosed", err)
	}
	if err := l.Run(); !errors.Is(err, ErrClosed) {
		t.Errorf("Run after Close = %v, want ErrClosed", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
}

func TestBusyCountsWorkButNotWaiting(t *testing.T) {
	l := newLoop(t)
	const work, wait = 30 * time.Millisecond, 60 * time.Millisecond
	timer, err := l.AddTimer(func() error {
		time.Sleep(work)
		l.Stop()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := timer.Arm(wait); err != nil {
		t.Fatal(err)
	}
	run(t, l)
	if busy := l.Busy(); busy < work || busy >= wait {
		t.Fatalf("busy %v after %v of work and %v of waiting", busy, work, wait)
	}
}
