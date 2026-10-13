package xcursor

import "image"

var legacy = map[string][]string{
	"default":       {"left_ptr"},
	"context-menu":  {"left_ptr"},
	"help":          {"question_arrow", "left_ptr"},
	"pointer":       {"hand2", "hand1"},
	"progress":      {"left_ptr_watch", "watch"},
	"wait":          {"watch"},
	"cell":          {"plus"},
	"crosshair":     {"cross", "tcross"},
	"text":          {"xterm"},
	"vertical-text": {"xterm"},
	"alias":         {"dnd-link"},
	"copy":          {"dnd-copy"},
	"move":          {"fleur", "dnd-move"},
	"no-drop":       {"dnd-none", "crossed_circle"},
	"not-allowed":   {"crossed_circle"},
	"grab":          {"openhand", "hand1"},
	"grabbing":      {"closedhand", "fleur"},
	"e-resize":      {"right_side"},
	"n-resize":      {"top_side"},
	"ne-resize":     {"top_right_corner"},
	"nw-resize":     {"top_left_corner"},
	"s-resize":      {"bottom_side"},
	"se-resize":     {"bottom_right_corner"},
	"sw-resize":     {"bottom_left_corner"},
	"w-resize":      {"left_side"},
	"ew-resize":     {"sb_h_double_arrow"},
	"ns-resize":     {"sb_v_double_arrow"},
	"nesw-resize":   {"fd_double_arrow"},
	"nwse-resize":   {"bd_double_arrow"},
	"col-resize":    {"sb_h_double_arrow"},
	"row-resize":    {"sb_v_double_arrow"},
	"all-scroll":    {"fleur"},
	"zoom-in":       {"left_ptr"},
	"zoom-out":      {"left_ptr"},
	"dnd-ask":       {"dnd-copy"},
	"all-resize":    {"fleur"},
}

func Names(css string) []string {
	return append([]string{css}, legacy[css]...)
}

func Builtin(size int) Image {
	size = max(size, 8)
	img := Image{Size: image.Pt(size, size), Nominal: size, Pixels: make([]byte, size*size*4)}
	inside := func(x, y int) bool {
		fy := float64(y) + 1
		return x >= 0 && y >= 0 && fy <= float64(size)*0.85 && float64(x) <= fy*0.6
	}
	for y := range size {
		for x := range size {
			if !inside(x, y) {
				continue
			}
			v := byte(0xff)
			if !inside(x-1, y) || !inside(x+1, y) || !inside(x, y-1) || !inside(x, y+1) {
				v = 0
			}
			copy(img.Pixels[(y*size+x)*4:], []byte{v, v, v, 0xff})
		}
	}
	return img
}
