package region

import (
	"image"
	"math/rand/v2"
	"testing"
)

const grid = 16

type oracle [grid][grid]bool

func (o *oracle) set(r image.Rectangle, v bool) {
	r = r.Canon().Intersect(image.Rect(0, 0, grid, grid))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			o[y][x] = v
		}
	}
}

func randRect(rng *rand.Rand) image.Rectangle {
	return image.Rect(rng.IntN(grid), rng.IntN(grid), rng.IntN(grid+1), rng.IntN(grid+1))
}

func check(t *testing.T, g Region, o *oracle, step int) {
	t.Helper()
	count := 0
	for y := range grid {
		for x := range grid {
			if g.Contains(image.Pt(x, y)) != o[y][x] {
				t.Fatalf("step %d: pixel (%d, %d) is %v in the region, %v in the oracle", step, x, y, !o[y][x], o[y][x])
			}
			if o[y][x] {
				count++
			}
		}
	}
	if g.Area() != count {
		t.Fatalf("step %d: area %d, oracle has %d pixels: rectangles overlap", step, g.Area(), count)
	}
	for i, a := range g.rects {
		if a.Empty() {
			t.Fatalf("step %d: empty rectangle stored", step)
		}
		for _, b := range g.rects[i+1:] {
			if a.Overlaps(b) {
				t.Fatalf("step %d: %v overlaps %v", step, a, b)
			}
		}
	}
}

func TestAddAndSubtractMatchAPixelGrid(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range 200 {
		var g Region
		var o oracle
		for step := range 12 {
			r := randRect(rng)
			if rng.IntN(3) == 0 {
				g = g.Subtract(r)
				o.set(r, false)
			} else {
				g = g.Add(r)
				o.set(r, true)
			}
			check(t, g, &o, trial*100+step)
		}
		clip := randRect(rng)
		var clipped oracle
		for y := range grid {
			for x := range grid {
				clipped[y][x] = o[y][x] && image.Pt(x, y).In(clip.Canon())
			}
		}
		check(t, g.Intersect(clip), &clipped, -trial)
	}
}

func TestUnionTranslateAndBounds(t *testing.T) {
	a := Rect(image.Rect(0, 0, 4, 4))
	b := Rect(image.Rect(2, 2, 6, 6))
	u := a.Union(b)
	if u.Area() != 16+16-4 {
		t.Fatalf("union area %d", u.Area())
	}
	if u.Bounds() != image.Rect(0, 0, 6, 6) {
		t.Fatalf("bounds %v", u.Bounds())
	}
	moved := u.Translate(image.Pt(10, -1))
	if !moved.Contains(image.Pt(10, -1)) || moved.Contains(image.Pt(0, 0)) || moved.Area() != u.Area() {
		t.Fatal("translate moved the wrong pixels")
	}
	doubled := u.Map(func(r image.Rectangle) image.Rectangle { return image.Rectangle{Min: r.Min.Mul(2), Max: r.Max.Mul(2)} })
	if doubled.Area() != 4*u.Area() {
		t.Fatalf("mapped area %d, want %d", doubled.Area(), 4*u.Area())
	}
	if !(Region{}).Empty() || !Rect(image.Rect(3, 3, 3, 9)).Empty() {
		t.Fatal("empty regions are not empty")
	}
}

func TestOperationsDoNotMutateTheirReceiver(t *testing.T) {
	a := Rect(image.Rect(0, 0, 4, 4))
	_ = a.Add(image.Rect(10, 10, 12, 12))
	_ = a.Subtract(image.Rect(0, 0, 2, 2))
	if a.Area() != 16 || len(a.Rects()) != 1 {
		t.Fatalf("receiver changed: %v", a.Rects())
	}
}
