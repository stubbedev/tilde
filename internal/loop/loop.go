// Package loop is the single-threaded event loop that owns compositor state.
package loop

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type Events uint32

const (
	Readable Events = unix.EPOLLIN
	Writable Events = unix.EPOLLOUT
	Hangup   Events = unix.EPOLLHUP | unix.EPOLLRDHUP
	Error    Events = unix.EPOLLERR
)

var ErrClosed = errors.New("loop: closed")

type token uint64

func (t token) event(events Events) unix.EpollEvent {
	return unix.EpollEvent{Events: uint32(events), Fd: int32(uint32(t)), Pad: int32(uint32(t >> 32))} //nolint:gosec // packs the token into epoll's 64-bit data union
}

func tokenOf(ev *unix.EpollEvent) token {
	return token(uint32(ev.Fd)) | token(uint32(ev.Pad))<<32 //nolint:gosec // unpacks the token from epoll's 64-bit data union
}

type source struct {
	fd      int
	owned   bool
	handler func(Events) error
}

type Loop struct {
	epfd    int
	sources map[token]*source
	next    token
	events  []unix.EpollEvent
	idle    []func() error
	running []func() error
	before  []func() error
	stopped bool
	closed  bool
	wakeups int

	mailboxFD int
	mu        sync.Mutex
	posted    []func() error
	received  []func() error
	shut      bool

	signals []*Signals
}

func New() (*Loop, error) {
	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("loop: epoll_create1: %w", err)
	}
	l := &Loop{epfd: epfd, sources: map[token]*source{}, events: make([]unix.EpollEvent, 64)}
	efd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		_ = unix.Close(epfd)
		return nil, fmt.Errorf("loop: eventfd: %w", err)
	}
	l.mailboxFD = efd
	if _, err := l.add(efd, true, Readable, l.drainMailbox); err != nil {
		_ = unix.Close(efd)
		_ = unix.Close(epfd)
		return nil, err
	}
	return l, nil
}

func (l *Loop) add(fd int, owned bool, events Events, handler func(Events) error) (token, error) {
	if l.closed {
		return 0, ErrClosed
	}
	l.next++
	t := l.next
	ev := t.event(events)
	if err := unix.EpollCtl(l.epfd, unix.EPOLL_CTL_ADD, fd, &ev); err != nil {
		return 0, fmt.Errorf("loop: add fd %d: %w", fd, err)
	}
	l.sources[t] = &source{fd: fd, owned: owned, handler: handler}
	return t, nil
}

func (l *Loop) modify(t token, events Events) error {
	s, ok := l.sources[t]
	if !ok {
		return ErrClosed
	}
	ev := t.event(events)
	if err := unix.EpollCtl(l.epfd, unix.EPOLL_CTL_MOD, s.fd, &ev); err != nil {
		return fmt.Errorf("loop: modify fd %d: %w", s.fd, err)
	}
	return nil
}

func (l *Loop) remove(t token) error {
	s, ok := l.sources[t]
	if !ok {
		return nil
	}
	delete(l.sources, t)
	var errs []error
	if err := unix.EpollCtl(l.epfd, unix.EPOLL_CTL_DEL, s.fd, nil); err != nil && !errors.Is(err, unix.EBADF) {
		errs = append(errs, fmt.Errorf("loop: remove fd %d: %w", s.fd, err))
	}
	if s.owned {
		if err := unix.Close(s.fd); err != nil {
			errs = append(errs, fmt.Errorf("loop: close fd %d: %w", s.fd, err))
		}
	}
	return errors.Join(errs...)
}

type Source struct {
	loop  *Loop
	token token
}

func (l *Loop) AddFD(fd int, events Events, handler func(Events) error) (*Source, error) {
	t, err := l.add(fd, false, events, handler)
	if err != nil {
		return nil, err
	}
	return &Source{loop: l, token: t}, nil
}

func (s *Source) Modify(events Events) error {
	return s.loop.modify(s.token, events)
}

func (s *Source) Remove() error {
	return s.loop.remove(s.token)
}

type Timer struct {
	loop  *Loop
	token token
	fd    int
}

func (l *Loop) AddTimer(handler func() error) (*Timer, error) {
	fd, err := unix.TimerfdCreate(unix.CLOCK_MONOTONIC, unix.TFD_CLOEXEC|unix.TFD_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("loop: timerfd_create: %w", err)
	}
	var buf [8]byte
	t, err := l.add(fd, true, Readable, func(Events) error {
		if _, err := unix.Read(fd, buf[:]); err != nil {
			if errors.Is(err, unix.EAGAIN) {
				return nil
			}
			return fmt.Errorf("loop: read timerfd %d: %w", fd, err)
		}
		return handler()
	})
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &Timer{loop: l, token: t, fd: fd}, nil
}

func (t *Timer) Arm(d time.Duration) error {
	if d <= 0 {
		d = 1
	}
	return t.set(unix.NsecToTimespec(d.Nanoseconds()))
}

func (t *Timer) Disarm() error {
	return t.set(unix.Timespec{})
}

func (t *Timer) set(ts unix.Timespec) error {
	if _, ok := t.loop.sources[t.token]; !ok {
		return ErrClosed
	}
	if err := unix.TimerfdSettime(t.fd, 0, &unix.ItimerSpec{Value: ts}, nil); err != nil {
		return fmt.Errorf("loop: timerfd_settime on fd %d: %w", t.fd, err)
	}
	return nil
}

func (t *Timer) Remove() error {
	return t.loop.remove(t.token)
}

type Signals struct {
	loop *Loop
	ch   chan os.Signal
	stop chan struct{}
	done chan struct{}
}

func (l *Loop) AddSignals(handler func(os.Signal) error, sigs ...os.Signal) (*Signals, error) {
	if l.closed {
		return nil, ErrClosed
	}
	s := &Signals{loop: l, ch: make(chan os.Signal, len(sigs)), stop: make(chan struct{}), done: make(chan struct{})}
	signal.Notify(s.ch, sigs...)
	go func() {
		defer close(s.done)
		for {
			select {
			case sig := <-s.ch:
				if err := l.Post(func() error { return handler(sig) }); err != nil {
					return
				}
			case <-s.stop:
				return
			}
		}
	}()
	l.signals = append(l.signals, s)
	return s, nil
}

func (s *Signals) Remove() {
	if i := slices.Index(s.loop.signals, s); i >= 0 {
		s.loop.signals = slices.Delete(s.loop.signals, i, i+1)
		signal.Stop(s.ch)
		close(s.stop)
		<-s.done
	}
}

func (l *Loop) Post(f func() error) error {
	l.mu.Lock()
	if l.shut {
		l.mu.Unlock()
		return ErrClosed
	}
	wake := len(l.posted) == 0
	l.posted = append(l.posted, f)
	l.mu.Unlock()
	if !wake {
		return nil
	}
	one := [8]byte{1}
	if _, err := unix.Write(l.mailboxFD, one[:]); err != nil && !errors.Is(err, unix.EAGAIN) {
		return fmt.Errorf("loop: wake mailbox: %w", err)
	}
	return nil
}

func (l *Loop) drainMailbox(Events) error {
	var buf [8]byte
	if _, err := unix.Read(l.mailboxFD, buf[:]); err != nil && !errors.Is(err, unix.EAGAIN) {
		return fmt.Errorf("loop: read mailbox: %w", err)
	}
	l.mu.Lock()
	l.received, l.posted = l.posted, l.received[:0]
	l.mu.Unlock()
	for i, f := range l.received {
		l.received[i] = nil
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) Idle(f func() error) {
	l.idle = append(l.idle, f)
}

func (l *Loop) runIdle() error {
	for len(l.idle) > 0 && !l.stopped {
		l.running, l.idle = l.idle, l.running[:0]
		for i, f := range l.running {
			l.running[i] = nil
			if err := f(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *Loop) BeforeWait(f func() error) {
	l.before = append(l.before, f)
}

func (l *Loop) Wakeups() int {
	return l.wakeups
}

func (l *Loop) Stop() {
	l.stopped = true
}

func (l *Loop) Run() error {
	if l.closed {
		return ErrClosed
	}
	l.stopped = false
	for !l.stopped {
		if err := l.runIdle(); err != nil {
			return err
		}
		for _, f := range l.before {
			if err := f(); err != nil {
				return err
			}
		}
		if l.stopped {
			break
		}
		n, err := unix.EpollWait(l.epfd, l.events, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("loop: epoll_wait: %w", err)
		}
		l.wakeups++
		for i := range l.events[:n] {
			if l.stopped {
				break
			}
			ev := &l.events[i]
			s, ok := l.sources[tokenOf(ev)]
			if !ok {
				continue
			}
			if err := s.handler(Events(ev.Events)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *Loop) Close() error {
	if l.closed {
		return nil
	}
	l.mu.Lock()
	l.shut = true
	l.mu.Unlock()
	for len(l.signals) > 0 {
		l.signals[0].Remove()
	}
	var errs []error
	for _, t := range slices.Sorted(maps.Keys(l.sources)) {
		errs = append(errs, l.remove(t))
	}
	l.closed = true
	if err := unix.Close(l.epfd); err != nil {
		errs = append(errs, fmt.Errorf("loop: close epoll fd %d: %w", l.epfd, err))
	}
	return errors.Join(errs...)
}
