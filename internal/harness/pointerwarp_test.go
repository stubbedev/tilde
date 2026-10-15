package harness

import (
	"errors"
	"slices"
	"strings"
	"testing"

	cpw "github.com/stubbedev/tilde/internal/protocol/client/pointerwarp"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wire"
)

func pointerWarp(t *testing.T, c *testclient.Client) *cpw.WpPointerWarpV1 {
	t.Helper()
	w, err := cpw.NewWpPointerWarpV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(w)
	return w
}

func TestClientsWarpThePointerOverTheirOwnFocusedSurfaces(t *testing.T) {
	h := Start(t, small)
	a := join(t, h, "a")
	a.seen(t, "the pointer over a", func() bool { return a.in.Serial != 0 })
	warp := pointerWarp(t, a.c)
	motions := func() []string {
		return slices.DeleteFunc(slices.Clone(a.in.Log), func(e string) bool { return !strings.HasPrefix(e, "motion") })
	}
	request := func(s *cw.WlSurface, x, y float64, serial uint32) {
		t.Helper()
		if err := errors.Join(warp.WarpPointer(s, a.in.Pointer, wire.FixedFromFloat(x), wire.FixedFromFloat(y), serial), a.c.Display.Flush()); err != nil {
			t.Fatal(err)
		}
	}
	request(a.w.Surface, 500, 20, a.in.Serial)
	request(a.w.Surface, 10, 20, a.in.Serial+1)
	other := testclient.Connect(t, h.Socket())
	_, otherIn := other.Seat()
	otherWarp := pointerWarp(t, other)
	if err := errors.Join(otherWarp.WarpPointer(other.Surface(), otherIn.Pointer, wire.FixedFromFloat(1), wire.FixedFromFloat(1), a.in.Serial), other.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	other.Sync()
	request(a.w.Surface, 10, 20, a.in.Serial)
	a.seen(t, "the warp's motion", func() bool { return len(motions()) > 0 })
	if got := motions(); !slices.Equal(got, []string{"motion 10,20"}) {
		t.Fatalf("a saw %q, want the one honoured warp", got)
	}
}
