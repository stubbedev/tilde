package compositor

import (
	"errors"
	"image"

	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/surface"
)

func (c *Compositor) PopupsChanged() error {
	return c.scheduleAll()
}

func clampInto(r, bounds image.Rectangle) image.Rectangle {
	shift := image.Point{}
	if r.Max.X > bounds.Max.X {
		shift.X = bounds.Max.X - r.Max.X
	}
	if r.Max.Y > bounds.Max.Y {
		shift.Y = bounds.Max.Y - r.Max.Y
	}
	r = r.Add(shift)
	shift = image.Point{}
	if r.Min.X < bounds.Min.X {
		shift.X = bounds.Min.X - r.Min.X
	}
	if r.Min.Y < bounds.Min.Y {
		shift.Y = bounds.Min.Y - r.Min.Y
	}
	return r.Add(shift)
}

func (c *Compositor) drawInputPopups(pass *frame, o *output.Output, shown []*surface.Surface) ([]*surface.Surface, error) {
	var errs []error
	for _, p := range c.inputMethods.Popups() {
		org, ok := c.originOf(p.Anchor)
		if !ok || p.Surface.Buffer() == nil {
			continue
		}
		cursor := image.Rectangle{Min: org.child(p.Cursor.Min).at, Max: org.child(p.Cursor.Max).at}
		at := image.Rectangle{Min: image.Pt(cursor.Min.X, cursor.Max.Y), Max: image.Pt(cursor.Min.X, cursor.Max.Y).Add(p.Surface.Size())}
		if out := c.outputAt(point{float64(cursor.Min.X), float64(cursor.Min.Y)}); out != nil {
			at = clampInto(at, out.Logical())
		}
		errs = append(errs, p.Placed(cursor.Sub(at.Min)))
		if at.Overlaps(o.Logical()) {
			shown = c.drawTree(pass, o, p.Surface, flat(at.Min), shown)
		}
	}
	return shown, errors.Join(errs...)
}
