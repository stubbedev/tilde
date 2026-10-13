package harness

import (
	"errors"
	"testing"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/backend/nested"
	"github.com/stubbedev/tilde/internal/compositor"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/testenv"
)

func TestNestedTildeHidesTheHostCursor(t *testing.T) {
	host := Start(t, small)
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	b, err := nested.New(l, host.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	display, err := server.New(l, server.Options{RuntimeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer display.Close()
	comp, err := compositor.New(l, display, b, compositor.DefaultConfig(testenv.Keymap(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer comp.Close()
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	defer func() {
		if err := l.Post(func() error { l.Stop(); return nil }); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil && !errors.Is(err, nested.ErrClosed) {
			t.Error(err)
		}
	}()
	host.Eventually("the nested window under the pointer with no host cursor", func(_ *headless.Backend, c *compositor.Compositor) bool {
		cur := c.Seat().Cursor()
		return c.Seat().PointerFocus() != nil && cur.Set() && cur.Surface() == nil && cur.Shape() == ""
	})
}
