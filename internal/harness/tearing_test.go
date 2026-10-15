package harness

import (
	"image"
	"regexp"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	ctc "github.com/stubbedev/tilde/internal/protocol/client/tearingcontrol"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wm"
)

func TestTearingFollowsTheSwitchTheHintAndTheRules(t *testing.T) {
	testclientRule := func(tearing wm.RuleTearing) []wm.Rule {
		return []wm.Rule{{AppID: regexp.MustCompile(`^tilde\.testclient$`), Tearing: tearing}}
	}
	cases := []struct {
		name    string
		tearing bool
		rules   []wm.Rule
		async   bool
		torn    bool
	}{
		{name: "an async hint with tearing off", async: true},
		{name: "an async hint", tearing: true, async: true, torn: true},
		{name: "no hint", tearing: true},
		{name: "no hint and a rule that allows tearing", tearing: true, rules: testclientRule(wm.TearingAllow), torn: true},
		{name: "an async hint and a rule that denies tearing", tearing: true, rules: testclientRule(wm.TearingDeny), async: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fullscreenScanout(t, func(cfg *compositor.Config) {
				cfg.Tearing, cfg.Rules = tc.tearing, tc.rules
			})
			if tc.async {
				m, err := ctc.NewWpTearingControlManagerV1(s.c.Display, 1)
				if err != nil {
					t.Fatal(err)
				}
				s.c.Bind(m)
				hint, err := m.GetTearingControl(s.w.Surface)
				if err != nil {
					t.Fatal(err)
				}
				if err := hint.SetPresentationHint(ctc.WpTearingControlV1PresentationHintAsync); err != nil {
					t.Fatal(err)
				}
			}
			s.show(t, "the dmabuf", s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0xff, 0, 0)))
			var torn uint64
			s.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
				torn = b.Outputs()[0].Torn()
				return nil
			})
			if (torn == 1) != tc.torn {
				t.Fatalf("%d torn scanouts, want torn %v", torn, tc.torn)
			}
		})
	}
}

func tearingManager(t *testing.T, c *testclient.Client) *ctc.WpTearingControlManagerV1 {
	t.Helper()
	m, err := ctc.NewWpTearingControlManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	return m
}

func TestOneTearingControlPerSurfaceUntilItIsGone(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m := tearingManager(t, c)
	s := c.Surface()
	first, err := m.GetTearingControl(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Destroy(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetTearingControl(s); err != nil {
		t.Fatal(err)
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatalf("a tearing control after the first was destroyed: %v", err)
	}
	if _, err := m.GetTearingControl(s); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(ctc.WpTearingControlManagerV1ErrorTearingControlExists))
}

func TestDestroyingTheHintStopsTearing(t *testing.T) {
	s := fullscreenScanout(t, func(cfg *compositor.Config) { cfg.Tearing = true })
	hint, err := tearingManager(t, s.c).GetTearingControl(s.w.Surface)
	if err != nil {
		t.Fatal(err)
	}
	if err := hint.SetPresentationHint(ctc.WpTearingControlV1PresentationHintAsync); err != nil {
		t.Fatal(err)
	}
	s.show(t, "the first dmabuf", s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0xff, 0, 0)))
	if err := hint.Destroy(); err != nil {
		t.Fatal(err)
	}
	s.show(t, "the second dmabuf", s.c.DMABUF(image.Pt(160, 100), testclient.Fill(0, 0xff, 0)))
	var torn uint64
	s.h.Do(func(b *headless.Backend, _ *compositor.Compositor) error {
		torn = b.Outputs()[0].Torn()
		return nil
	})
	if torn != 1 {
		t.Fatalf("%d torn scanouts, want only the one before the hint went", torn)
	}
}
