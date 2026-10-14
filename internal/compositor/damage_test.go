package compositor

import (
	"image"
	"testing"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/region"
	"github.com/stubbedev/tilde/internal/render"
)

func (sc *scene) frameWith(ops ...drawOp) {
	sc.cur.ops = append(sc.cur.ops[:0], ops...)
}

func TestSceneDiffAndBufferAge(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	bg := drawOp{fill: true, color: Background, dst: bounds}
	box := func(x int) drawOp {
		return drawOp{fill: true, color: render.Color{A: 0xff}, dst: image.Rect(x, 0, x+10, 10)}
	}
	sc := newScene()
	sc.frameWith(bg, box(0))
	if d := sc.diff(bounds); d.Area() != 100*100 {
		t.Fatalf("the first frame damaged %v", d.Rects())
	}
	sc.presented()
	sc.frameWith(bg, box(0))
	if d := sc.diff(bounds); !d.Empty() {
		t.Fatalf("an identical frame damaged %v", d.Rects())
	}
	sc.skipped()
	sc.frameWith(bg, box(20))
	if d := sc.diff(bounds); d.Area() != 200 {
		t.Fatalf("moving a box damaged %v", d.Rects())
	}
	sc.presented()
	sc.frameWith(bg, box(40))
	sc.diff(bounds)
	cases := []struct {
		age  int
		area int
	}{{1, 200}, {2, 300}, {0, 100 * 100}, {maxBufferAge + 1, 100 * 100}}
	for _, c := range cases {
		if got := sc.repaintFor(backend.Frame{Age: c.age}, bounds).Area(); got != c.area {
			t.Errorf("age %d repaints %d pixels, want %d", c.age, got, c.area)
		}
	}
}

func TestSurfaceDamageScalesWithItsDestination(t *testing.T) {
	sc := newScene()
	op := drawOp{dst: image.Rect(10, 10, 30, 30), size: image.Pt(10, 10)}
	var d region.Set
	d.Add(image.Rect(2, 3, 4, 5))
	sc.addSurfaceDamage(op, &d)
	if r := sc.damage.Rects(); len(r) != 1 || r[0] != image.Rect(14, 16, 18, 20) {
		t.Fatalf("damage %v", r)
	}
}
