package compositor

import (
	"image"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/region"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/surface"
)

const maxBufferAge = 4

type drawOp struct {
	fill      bool
	color     render.Color
	tex       render.Texture
	src       render.FRect
	dst       image.Rectangle
	transform render.Transform
	alpha     float32
	surface   *surface.Surface
	size      image.Point
}

type frame struct {
	ops []drawOp
}

func (f *frame) FillRect(r image.Rectangle, c render.Color) {
	f.ops = append(f.ops, drawOp{fill: true, color: c, dst: r})
}

func (f *frame) DrawTexture(t render.Texture, src render.FRect, dst image.Rectangle, transform render.Transform, alpha float32) {
	f.ops = append(f.ops, drawOp{tex: t, src: src, dst: dst, transform: transform, alpha: alpha})
}

func (f *frame) drawSurface(s *surface.Surface, t render.Texture, src render.FRect, dst image.Rectangle, transform render.Transform) {
	f.ops = append(f.ops, drawOp{tex: t, src: src, dst: dst, transform: transform, alpha: 1, surface: s, size: s.Size()})
}

func (f *frame) replay(p render.Pass) {
	for _, op := range f.ops {
		if op.fill {
			p.FillRect(op.dst, op.color)
			continue
		}
		p.DrawTexture(op.tex, op.src, op.dst, op.transform, op.alpha)
	}
}

type scene struct {
	prev, cur frame
	content   map[*surface.Surface]*region.Set
	damage    region.Set
	repaint   region.Set
	history   [maxBufferAge]region.Set
	head      int
	drawn     bool
	seq       uint64
}

func newScene() *scene {
	return &scene{content: map[*surface.Surface]*region.Set{}}
}

func (sc *scene) damaged(s *surface.Surface, d region.Region) {
	set, ok := sc.content[s]
	if !ok {
		set = &region.Set{}
		sc.content[s] = set
	}
	set.AddRegion(d)
}

func (sc *scene) addSurfaceDamage(op drawOp, d *region.Set) {
	if op.size.X <= 0 || op.size.Y <= 0 {
		return
	}
	w, h := op.dst.Dx(), op.dst.Dy()
	for _, r := range d.Rects() {
		sc.damage.Add(image.Rect(
			op.dst.Min.X+r.Min.X*w/op.size.X, op.dst.Min.Y+r.Min.Y*h/op.size.Y,
			op.dst.Min.X+(r.Max.X*w+op.size.X-1)/op.size.X, op.dst.Min.Y+(r.Max.Y*h+op.size.Y-1)/op.size.Y,
		).Intersect(op.dst))
	}
}

func (sc *scene) diff(bounds image.Rectangle) *region.Set {
	sc.damage.Reset()
	if !sc.drawn {
		sc.damage.Add(bounds)
	}
	prev, cur := sc.prev.ops, sc.cur.ops
	for i := range max(len(prev), len(cur)) {
		switch {
		case i >= len(prev):
			sc.damage.Add(cur[i].dst)
		case i >= len(cur):
			sc.damage.Add(prev[i].dst)
		case prev[i] != cur[i]:
			sc.damage.Add(prev[i].dst)
			sc.damage.Add(cur[i].dst)
		case cur[i].surface != nil:
			if content, ok := sc.content[cur[i].surface]; ok {
				sc.addSurfaceDamage(cur[i], content)
			}
		}
	}
	for _, content := range sc.content {
		content.Reset()
	}
	sc.damage.Clip(bounds)
	return &sc.damage
}

func (sc *scene) repaintFor(f backend.Frame, bounds image.Rectangle) *region.Set {
	sc.repaint.Reset()
	if f.Age <= 0 || f.Age > maxBufferAge {
		sc.repaint.Add(bounds)
		return &sc.repaint
	}
	sc.repaint.CopyFrom(&sc.damage)
	for i := range f.Age - 1 {
		sc.repaint.AddSet(&sc.history[(sc.head+i)%maxBufferAge])
	}
	return &sc.repaint
}

func (sc *scene) presented() {
	sc.head = (sc.head + maxBufferAge - 1) % maxBufferAge
	sc.history[sc.head].CopyFrom(&sc.damage)
	sc.prev, sc.cur = sc.cur, sc.prev
	sc.cur.ops = sc.cur.ops[:0]
	sc.drawn = true
}

func (sc *scene) skipped() {
	sc.cur.ops = sc.cur.ops[:0]
}

func (sc *scene) forget(s *surface.Surface) {
	delete(sc.content, s)
}
