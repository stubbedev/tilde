package surface

type Latched[T comparable] struct {
	fallback T
	values   map[*Surface]*LatchedValue[T]
}

type LatchedValue[T comparable] struct {
	l        *Latched[T]
	s        *Surface
	hooks    *Hooks
	attached bool
	gone     bool
	pending  *T
	inFlight int
	current  T
}

func NewLatched[T comparable](fallback T) *Latched[T] {
	return &Latched[T]{fallback: fallback, values: map[*Surface]*LatchedValue[T]{}}
}

func (l *Latched[T]) Get(s *Surface) T {
	if v, ok := l.values[s]; ok {
		return v.current
	}
	return l.fallback
}

func (l *Latched[T]) Attach(s *Surface) (*LatchedValue[T], bool) {
	v, ok := l.values[s]
	if ok && v.attached {
		return nil, false
	}
	if !ok {
		v = &LatchedValue[T]{l: l, s: s, current: l.fallback}
		v.hooks = &Hooks{
			Commit:    func(u *Update) error { v.commit(u); return nil },
			Destroyed: func() error { v.surfaceGone(); return nil },
		}
		s.AddHooks(v.hooks)
		l.values[s] = v
	}
	v.attached = true
	return v, true
}

func (v *LatchedValue[T]) Set(value T) {
	v.pending = &value
}

func (v *LatchedValue[T]) Gone() bool {
	return v.gone
}

func (v *LatchedValue[T]) Detach() error {
	v.attached = false
	if !v.gone {
		v.Set(v.l.fallback)
	}
	return nil
}

func (v *LatchedValue[T]) commit(u *Update) {
	if v.pending == nil {
		return
	}
	value := *v.pending
	v.pending = nil
	v.inFlight++
	u.OnApply(func() error {
		v.current = value
		v.inFlight--
		if !v.attached && v.pending == nil && v.inFlight == 0 && v.current == v.l.fallback {
			v.forget()
		}
		return nil
	})
}

func (v *LatchedValue[T]) surfaceGone() {
	v.gone = true
	v.forget()
}

func (v *LatchedValue[T]) forget() {
	v.s.RemoveHooks(v.hooks)
	if v.l.values[v.s] == v {
		delete(v.l.values, v.s)
	}
}
