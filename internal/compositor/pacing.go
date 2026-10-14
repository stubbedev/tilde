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
)

func (c *Compositor) addPacing(l *loop.Loop, display *server.Display) error {
	var err error
	if c.fifo, err = fifo.New(display, c.showing); err != nil {
		return err
	}
	c.timing, err = committiming.New(display, l, committiming.Clock{Now: backend.Now, NextFlip: c.nextFlip})
	return err
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
