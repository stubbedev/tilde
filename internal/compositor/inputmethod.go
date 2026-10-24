package compositor

import (
	"errors"
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/inputmethod"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/surface"
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

type placedInputPopup struct {
	popup  inputmethod.Popup
	at     image.Rectangle
	cursor image.Rectangle
}

func (c *Compositor) inputPopups() []placedInputPopup {
	var placed []placedInputPopup
	for _, p := range c.inputMethods.Popups() {
		org, ok := c.baseOrigin(p.Anchor)
		if !ok || p.Surface.Buffer() == nil {
			continue
		}
		cursor := image.Rectangle{Min: org.child(p.Cursor.Min).at, Max: org.child(p.Cursor.Max).at}
		below := image.Pt(cursor.Min.X, cursor.Max.Y)
		at := image.Rectangle{Min: below, Max: below.Add(p.Surface.Size())}
		if out := c.outputAt(point{float64(cursor.Min.X), float64(cursor.Min.Y)}); out != nil {
			at = clampInto(at, out.Logical())
		}
		placed = append(placed, placedInputPopup{popup: p, at: at, cursor: cursor})
	}
	return placed
}

func (c *Compositor) drawInputPopups(pass *frame, o *output.Output, shown []*surface.Surface) ([]*surface.Surface, error) {
	var errs []error
	for _, p := range c.inputPopups() {
		errs = append(errs, p.popup.Placed(p.cursor.Sub(p.at.Min)))
		if p.at.Overlaps(o.Logical()) {
			shown = c.drawTree(pass, o, p.popup.Surface, flat(p.at.Min), shown)
		}
	}
	return shown, errors.Join(errs...)
}

func (c *Compositor) hitInputPopups(p point) (*surface.Surface, point) {
	for _, placed := range slices.Backward(c.inputPopups()) {
		if s, local := hitTree(placed.popup.Surface, flat(placed.at.Min), p); s != nil {
			return s, local
		}
	}
	return nil, point{}
}
