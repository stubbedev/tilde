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
	return area(s.rects)
}

func (s *Set) Add(r image.Rectangle) {
	s.rects = appendUncovered(s.rects, r, &s.pieces, &s.scratch)
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
	s.rects = appendClipped(s.rects[:0], s.rects, bounds)
}
