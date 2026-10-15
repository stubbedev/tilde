package surface

type opt[T any] struct {
	v  T
	ok bool
}

func some[T any](v T) opt[T] {
	return opt[T]{v, true}
}

func (o opt[T]) get() (T, bool) {
	return o.v, o.ok
}
