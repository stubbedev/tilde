package compositor

import (
	"slices"
	"time"

	"github.com/stubbedev/tilde/internal/backend"
	"github.com/stubbedev/tilde/internal/committiming"
	"github.com/stubbedev/tilde/internal/fifo"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/tearingcontrol"
	"github.com/stubbedev/tilde/internal/wm"
)

func (c *Compositor) addPacing(l *loop.Loop, display *server.Display) error {
	var err error
	if c.fifo, err = fifo.New(display, c.showing); err != nil {
		return err
	}
	if c.timing, err = committiming.New(display, l, committiming.Clock{Now: backend.Now, NextFlip: c.nextFlip}); err != nil {
		return err
	}
	c.tearingHints, err = tearingcontrol.New(display)
	return err
}

func (c *Compositor) tears(s *surface.Surface) bool {
	if !c.tearing {
		return false
	}
	w := c.windowWithSurface(s)
	if w == nil {
		return false
	}
	switch wm.Decide(c.rules, w.top.AppID(), w.top.Title()).Tearing {
	case wm.TearingAllow:
		return true
	case wm.TearingDeny:
		return false
	}
	return c.tearingHints.Async(s)
}

func (c *Compositor) showing(s *surface.Surface) bool {
	for _, o := range c.outputs {
		if c.Powered(o) && slices.Contains(c.shown[o], s) {
			return true
		}
	}
	return false
}

func (c *Compositor) nextFlip(s *surface.Surface) (time.Duration, bool) {
	var next time.Duration
	found := false
	for _, o := range c.outputs {
		last := c.sceneOf(o).last
		if !c.Powered(o) || last.Time == 0 || !slices.Contains(c.shown[o], s) {
			continue
		}
		if flip := last.Time + last.Refresh; !found || flip < next {
			next, found = flip, true
		}
	}
	return next, found
}
