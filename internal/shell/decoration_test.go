package shell

import (
	"slices"
	"testing"

	cxd "github.com/stubbedev/tilde/internal/protocol/client/xdgdecoration"
	sxd "github.com/stubbedev/tilde/internal/protocol/server/xdgdecoration"
)

type decorationEvents struct {
	modes []cxd.ZxdgToplevelDecorationV1Mode
}

func (d *decorationEvents) Configure(_ *cxd.ZxdgToplevelDecorationV1, mode cxd.ZxdgToplevelDecorationV1Mode) error {
	d.modes = append(d.modes, mode)
	return nil
}

func TestDecorationsAreAlwaysServerSide(t *testing.T) {
	c := setup(t).connect(6)
	w := c.window()
	ev := &decorationEvents{}
	deco, err := c.deco.GetToplevelDecoration(ev, w.top)
	must(t, err, deco.SetMode(cxd.ZxdgToplevelDecorationV1ModeClientSide), w.surf.Commit())
	c.roundtrip()
	server := cxd.ZxdgToplevelDecorationV1ModeServerSide
	if !slices.Equal(ev.modes, []cxd.ZxdgToplevelDecorationV1Mode{server, server}) || len(w.xsE.serials) != 1 {
		t.Fatalf("modes %v, xdg_surface configures %v: server-side before the initial commit and one configure after it", ev.modes, w.xsE.serials)
	}
	must(t, w.xs.AckConfigure(w.lastSerial()), w.surf.Attach(c.buffer(10, 10), 0, 0), w.surf.Commit(), deco.UnsetMode())
	c.roundtrip()
	if len(ev.modes) != 3 || ev.modes[2] != server || len(w.xsE.serials) != 2 {
		t.Fatalf("modes %v, configures %v: a mode change on a mapped window is configured again", ev.modes, w.xsE.serials)
	}
}

func TestDecorationProtocolErrors(t *testing.T) {
	cases := []struct {
		name string
		do   func(c *client)
		code uint32
	}{
		{"second decoration", func(c *client) {
			w := c.window()
			_, err := c.deco.GetToplevelDecoration(&decorationEvents{}, w.top)
			must(c.t, err)
			_, err = c.deco.GetToplevelDecoration(&decorationEvents{}, w.top)
			must(c.t, err)
		}, uint32(sxd.ZxdgToplevelDecorationV1ErrorAlreadyConstructed)},
		{"after a buffer", func(c *client) {
			w := c.window()
			must(c.t, w.surf.Commit())
			c.roundtrip()
			must(c.t, w.xs.AckConfigure(w.lastSerial()), w.surf.Attach(c.buffer(4, 4), 0, 0), w.surf.Commit())
			_, err := c.deco.GetToplevelDecoration(&decorationEvents{}, w.top)
			must(c.t, err)
		}, uint32(sxd.ZxdgToplevelDecorationV1ErrorUnconfiguredBuffer)},
		{"invalid mode", func(c *client) {
			deco, err := c.deco.GetToplevelDecoration(&decorationEvents{}, c.window().top)
			must(c.t, err, deco.SetMode(3))
		}, uint32(sxd.ZxdgToplevelDecorationV1ErrorInvalidMode)},
		{"toplevel destroyed first", func(c *client) {
			w := c.window()
			_, err := c.deco.GetToplevelDecoration(&decorationEvents{}, w.top)
			must(c.t, err, w.top.Destroy())
		}, uint32(sxd.ZxdgToplevelDecorationV1ErrorOrphaned)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := setup(t).connect(6)
			tc.do(c)
			c.protocolError(tc.code)
		})
	}
}
