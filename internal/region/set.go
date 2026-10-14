package region

import "image"

type Set struct {
	rects, pieces, scratch []image.Rectangle
}

func (s *Set) Reset() {
	s.rects = s.rects[:0]
}

func (s *Set) Empty() bool {
	return len(s.rects) == 0
}

func (s *Set) Rects() []image.Rectangle {
	return s.rects
}

func (s *Set) Area() int {
	n := 0
	for _, r := range s.rects {
		n += r.Dx() * r.Dy()
	}
	return n
}

func (s *Set) Add(r image.Rectangle) {
	r = r.Canon()
	if r.Empty() {
		return
	}
	s.pieces = append(s.pieces[:0], r)
	for _, existing := range s.rects {
		s.scratch = s.scratch[:0]
		for _, p := range s.pieces {
			s.scratch = minus(p, existing, s.scratch)
		}
		s.pieces, s.scratch = s.scratch, s.pieces
		if len(s.pieces) == 0 {
			return
		}
	}
	s.rects = append(s.rects, s.pieces...)
}

func (s *Set) AddSet(o *Set) {
	for _, r := range o.rects {
		s.Add(r)
	}
}

func (s *Set) AddRegion(g Region) {
	for _, r := range g.rects {
		s.Add(r)
	}
}

func (s *Set) CopyFrom(o *Set) {
	s.rects = append(s.rects[:0], o.rects...)
}

func (s *Set) Clip(bounds image.Rectangle) {
	out := s.rects[:0]
	for _, r := range s.rects {
		if c := r.Intersect(bounds); !c.Empty() {
			out = append(out, c)
		}
	}
	s.rects = out
}
