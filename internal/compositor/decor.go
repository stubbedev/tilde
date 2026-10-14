package compositor

import (
	"fmt"
	"image"

	gtext "github.com/stubbedev/gelm/render"

	"github.com/stubbedev/tilde/internal/output"
	"github.com/stubbedev/tilde/internal/render"
	"github.com/stubbedev/tilde/internal/wm"
)

type TabColors struct {
	Background, Text render.Color
}

type TabStyle struct {
	Height, Padding, TextHeight, TextPadding int
	Active, Inactive, Urgent                 TabColors
}

type Decor struct {
	BorderWidth                  int
	ActiveBorder, InactiveBorder render.Color
	Tab                          TabStyle
}

var (
	mauve    = render.Color{R: 0xcb, G: 0xa6, B: 0xf7, A: 0xff}
	crust    = render.Color{R: 0x11, G: 0x11, B: 0x1b, A: 0xff}
	surface0 = render.Color{R: 0x31, G: 0x32, B: 0x44, A: 0xff}
	textFg   = render.Color{R: 0xcd, G: 0xd6, B: 0xf4, A: 0xff}
	red      = render.Color{R: 0xf3, G: 0x8b, B: 0xa8, A: 0xff}
)

func DefaultDecor() Decor {
	return Decor{
		BorderWidth:    1,
		ActiveBorder:   mauve,
		InactiveBorder: crust,
		Tab: TabStyle{
			Height: 15, Padding: 5, TextHeight: 8, TextPadding: 3,
			Active:   TabColors{Background: mauve, Text: crust},
			Inactive: TabColors{Background: surface0, Text: textFg},
			Urgent:   TabColors{Background: red, Text: crust},
		},
	}
}

func (d Decor) reserved() int {
	return d.Tab.Height + d.Tab.Padding
}

type labelKey struct {
	text   string
	size   image.Point
	px     int
	colors TabColors
}

type label struct {
	tex  render.Texture
	used bool
}

func gelmColor(c render.Color) gtext.Color {
	return gtext.RGBA(c.R, c.G, c.B, c.A)
}

func (c *Compositor) label(o *output.Output, key labelKey) (render.Texture, error) {
	cache := c.labels[o]
	if cache == nil {
		cache = map[labelKey]*label{}
		c.labels[o] = cache
	}
	if l, ok := cache[key]; ok {
		l.used = true
		return l.tex, nil
	}
	stride := key.size.X * 4
	pix := make([]byte, stride*key.size.Y)
	cv := gtext.New(pix, stride, key.size.X, key.size.Y)
	cv.Clear(gtext.Rect{W: key.size.X, H: key.size.Y}, gelmColor(key.colors.Background))
	pad := o.Scale().ToDevice(c.decor.Tab.TextPadding)
	box := gtext.Rect{X: pad, W: key.size.X - 2*pad, H: key.size.Y}
	text := c.font.Ellipsize(key.text, float64(box.W), float64(key.px))
	c.font.DrawAligned(cv, text, box, float64(key.px), gelmColor(key.colors.Text), gtext.AlignStart)
	tex, err := c.renderer.NewTexture(render.ARGB8888, key.size, stride, pix)
	if err != nil {
		return nil, fmt.Errorf("compositor: tab label %q: %w", key.text, err)
	}
	cache[key] = &label{tex: tex, used: true}
	return tex, nil
}

func (c *Compositor) evictLabels(o *output.Output) {
	for key, l := range c.labels[o] {
		if !l.used {
			l.tex.Destroy()
			delete(c.labels[o], key)
			continue
		}
		l.used = false
	}
}

func (c *Compositor) drawBorder(pass render.Pass, o *output.Output, w *window) {
	if c.decor.BorderWidth <= 0 || w.layer == wm.LayerFullscreen {
		return
	}
	color := c.decor.InactiveBorder
	if w == c.active {
		color = c.decor.ActiveBorder
	}
	outer := o.Scale().Rect(w.tile.Sub(o.Position()))
	inner := o.Scale().Rect(w.rect.Sub(o.Position()))
	for _, strip := range []image.Rectangle{
		{Min: outer.Min, Max: image.Pt(outer.Max.X, inner.Min.Y)},
		{Min: image.Pt(outer.Min.X, inner.Max.Y), Max: outer.Max},
		{Min: image.Pt(outer.Min.X, inner.Min.Y), Max: image.Pt(inner.Min.X, inner.Max.Y)},
		{Min: image.Pt(inner.Max.X, inner.Min.Y), Max: image.Pt(outer.Max.X, inner.Max.Y)},
	} {
		if !strip.Empty() {
			pass.FillRect(strip, color)
		}
	}
}

func (c *Compositor) tabColors(id wm.WindowID, active bool) TabColors {
	switch w := c.windowByID(id); {
	case w != nil && w.urgent:
		return c.decor.Tab.Urgent
	case active:
		return c.decor.Tab.Active
	}
	return c.decor.Tab.Inactive
}

func (c *Compositor) drawTabBars(pass render.Pass, o *output.Output) error {
	for _, bar := range c.tabBars {
		if !bar.Rect.Overlaps(o.Logical()) {
			continue
		}
		strip := image.Rect(bar.Rect.Min.X, bar.Rect.Min.Y, bar.Rect.Max.X, bar.Rect.Min.Y+min(c.decor.Tab.Height, bar.Rect.Dy()))
		n := len(bar.Tabs)
		for i, id := range bar.Tabs {
			x0 := strip.Min.X + strip.Dx()*i/n
			x1 := strip.Min.X + strip.Dx()*(i+1)/n
			dst := o.Scale().Rect(image.Rect(x0, strip.Min.Y, x1, strip.Max.Y).Sub(o.Position()))
			if dst.Empty() {
				continue
			}
			title := ""
			if w := c.windowByID(id); w != nil {
				title = w.toplevel.Title()
			}
			tex, err := c.label(o, labelKey{text: title, size: dst.Size(), px: o.Scale().ToDevice(c.decor.Tab.TextHeight), colors: c.tabColors(id, i == bar.Active)})
			if err != nil {
				return err
			}
			size := tex.Size()
			pass.DrawTexture(tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, dst, render.Normal, 1)
		}
	}
	return nil
}

func (c *Compositor) dropLabels(o *output.Output) {
	for _, l := range c.labels[o] {
		l.tex.Destroy()
	}
	delete(c.labels, o)
}
