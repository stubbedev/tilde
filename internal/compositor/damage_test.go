package compositor

import (
	"image"
	"testing"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/region"
	"github.com/stubbedev/tilde/internal/render"
)

func (sc *scene) frameWith(ops ...drawOp) {
	f := sc.begin()
	f.ops = append(f.ops, ops...)
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
	sc.begin().FillRect(image.Rect(60, 60, 70, 70), render.Color{A: 0xff})
	sc.frameWith(bg, box(0))
	if d := sc.diff(bounds); !d.Empty() {
		t.Fatalf("an abandoned frame left ops behind: %v", d.Rects())
	}
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

func TestModePolicies(t *testing.T) {
	modes := []backend.Mode{
		{Size: image.Pt(1920, 1080), RefreshMHz: 60000, Preferred: true},
		{Size: image.Pt(2560, 1440), RefreshMHz: 60000},
		{Size: image.Pt(2560, 1440), RefreshMHz: 143912},
		{Size: image.Pt(1280, 720), RefreshMHz: 75000},
	}
	current := modes[3]
	for _, c := range []struct {
		policy ModePolicy
		want   backend.Mode
	}{
		{PreferredMode{}, modes[0]},
		{HighResMode{}, modes[2]},
		{ExactMode{Size: image.Pt(2560, 1440), RefreshMHz: 144000}, modes[2]},
		{ExactMode{Size: image.Pt(2560, 1440)}, modes[2]},
		{ExactMode{Size: image.Pt(2560, 1440), RefreshMHz: 59940}, modes[1]},
	} {
		if got, err := c.policy.choose(modes, current); err != nil || got != c.want {
			t.Errorf("%#v chose %+v, %v; want %+v", c.policy, got, err, c.want)
		}
	}
	if _, err := (ExactMode{Size: image.Pt(800, 600)}).choose(modes, current); err == nil {
		t.Error("a missing exact mode was chosen")
	}
	if got, _ := (PreferredMode{}).choose(modes[1:], current); got != current {
		t.Errorf("no preferred mode chose %+v, want the current one", got)
	}
}

func TestAPointFloorsToItsPixel(t *testing.T) {
	for _, c := range []struct {
		p    point
		want image.Point
	}{{point{0.5, 2.7}, image.Pt(0, 2)}, {point{-0.5, -2.7}, image.Pt(-1, -3)}, {point{-1, 3}, image.Pt(-1, 3)}} {
		if got := c.p.pixel(); got != c.want {
			t.Errorf("%+v.pixel() = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestFloatingSizesKeepTheClientsLimits(t *testing.T) {
	for _, c := range []struct {
		size, minimum, maximum, want image.Point
	}{
		{image.Pt(40, 90), image.Pt(400, 300), image.Point{}, image.Pt(400, 300)},
		{image.Pt(900, 900), image.Point{}, image.Pt(800, 0), image.Pt(800, 900)},
		{image.Pt(500, 500), image.Pt(100, 100), image.Pt(600, 600), image.Pt(500, 500)},
	} {
		if got := clampSize(c.size, c.minimum, c.maximum); got != c.want {
			t.Errorf("clampSize(%v, %v, %v) = %v, want %v", c.size, c.minimum, c.maximum, got, c.want)
		}
	}
}
