package shell

import (
	"slices"
	"testing"

	sxdg "github.com/stubbedev/lys-wm/internal/protocol/server/xdgshell"
)

func TestConstrainedStatesNeedVersion7(t *testing.T) {
	for _, version := range []uint32{6, 7} {
		e := setup(t, func(r *recorder) {
			r.initial = func(tl *Toplevel) error {
				_, err := tl.Configure(State{States: []ToplevelState{sxdg.XdgToplevelStateTiledLeft, sxdg.XdgToplevelStateConstrainedLeft}})
				return err
			}
		})
		c := e.connect(version)
		w := c.window()
		must(t, w.surf.Commit())
		c.roundtrip()
		got := w.topE.configures[0].States
		want := []ToplevelState{sxdg.XdgToplevelStateTiledLeft}
		if version >= 7 {
			want = append(want, sxdg.XdgToplevelStateConstrainedLeft)
		}
		if !slices.Equal(got, want) {
			t.Errorf("version %d: states %v, want %v", version, got, want)
		}
	}
}
