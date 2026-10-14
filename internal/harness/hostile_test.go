package harness

import (
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/backend/headless"
	"github.com/stubbedev/tilde/internal/compositor"
	cxdg "github.com/stubbedev/tilde/internal/protocol/client/xdgshell"
	"github.com/stubbedev/tilde/internal/shell"
	"github.com/stubbedev/tilde/internal/testclient"
	"github.com/stubbedev/tilde/internal/wire"
)

func unresponsive(id int, want bool) func(*headless.Backend, *compositor.Compositor) bool {
	return func(_ *headless.Backend, c *compositor.Compositor) bool {
		for _, w := range c.Snapshot().Windows {
			if int(w.ID) == id {
				return w.Unresponsive == want
			}
		}
		return false
	}
}

func TestHungClientsAreMarkedUnresponsive(t *testing.T) {
	h := Start(t, small)
	lines := h.watch()
	hung := testclient.Connect(t, h.Socket())
	w := hung.Window("hung", quadrants)
	hung.DispatchUntil("the window to map", w.Mapped)
	h.Eventually("the window focused", func(_ *headless.Backend, c *compositor.Compositor) bool {
		ws := c.Snapshot().Windows
		return len(ws) == 1 && ws[0].Focused
	})
	time.Sleep(shell.PingTimeout)
	h.Eventually("the hung window marked unresponsive", unresponsive(1, true))
	expectEvent(t, lines, `{"event":"unresponsive","window":1,"unresponsive":true}`)

	alive := testclient.Connect(t, h.Socket())
	other := alive.Window("alive", quadrants)
	alive.DispatchUntil("another client's window to map", other.Mapped)
	h.Eventually("the other window responsive", unresponsive(2, false))

	hung.Sync()
	h.Eventually("the recovered window marked responsive", unresponsive(1, false))
	expectEvent(t, lines, `{"event":"unresponsive","window":1,"unresponsive":false}`)
}

func (r *rawClient) closedByServer(within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		m, err := r.receive(deadline)
		if err != nil {
			return !errors.Is(err, errTimedOut)
		}
		_ = m.Finish()
	}
}

func survives(t *testing.T, h *Harness) {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	w := c.Window("survivor", quadrants)
	c.DispatchUntil("a window mapped beside the hostile client", w.Mapped)
}

func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestGarbageDisconnectsOnlyItsSender(t *testing.T) {
	h := StartUntraced(t, small)
	r := dialRaw(t, h.Socket())
	if _, err := unix.Write(r.conn.FD(), []byte{1, 0, 0, 0, 0, 0, 4, 0, 0xde, 0xad, 0xbe, 0xef}); err != nil {
		t.Fatal(err)
	}
	if !r.closedByServer(fuzzTimeout) {
		t.Fatal("a client that sent a 4-byte message was not disconnected")
	}
	survives(t, h)
}

func TestIDsOutsideTheClientRangeAreErrors(t *testing.T) {
	for name, send := range map[string]func(r *rawClient) error{
		"a server-side new id": func(r *rawClient) error {
			return r.send(displayID, getRegistry, func(e *wire.Encoder) { e.Uint(0xff000005) })
		},
		"an unknown object": func(r *rawClient) error {
			return r.send(0x7fffffff, 0, func(*wire.Encoder) {})
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := StartUntraced(t, small)
			r := dialRaw(t, h.Socket())
			if err := errors.Join(send(r), r.conn.Flush()); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(fuzzTimeout)
			m, err := r.receive(deadline)
			if err != nil || m.ID != displayID || m.Opcode != 0 {
				t.Fatalf("got %v, %v instead of a wl_display.error", m, err)
			}
			_ = m.Finish()
			if !r.closedByServer(fuzzTimeout) {
				t.Fatal("the client was not disconnected after its error")
			}
			survives(t, h)
		})
	}
}

func TestFDFloodsAreDisconnectedAndClosed(t *testing.T) {
	h := StartUntraced(t, small)
	r := dialRaw(t, h.Socket())
	pipe := make([]int, 2)
	if err := unix.Pipe2(pipe, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = errors.Join(unix.Close(pipe[0]), unix.Close(pipe[1])) }()
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
	baseline := openFDs(t)
	fds := make([]int, 250)
	for i := range fds {
		fds[i] = pipe[0]
	}
	rights := unix.UnixRights(fds...)
	for range 8 {
		if err := unix.Sendmsg(r.conn.FD(), []byte{0}, rights, nil, unix.MSG_NOSIGNAL); err != nil {
			break
		}
	}
	if !r.closedByServer(fuzzTimeout) {
		t.Fatal("a client that queued 2000 file descriptors was not disconnected")
	}
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
	if n := openFDs(t); n != baseline-1 {
		t.Fatalf("%d file descriptors open after the flood and the server closing its end, %d before", n, baseline)
	}
	survives(t, h)
}

func TestClientsThatNeverReadAreDisconnected(t *testing.T) {
	h := StartUntraced(t, small)
	r := dialRaw(t, h.Socket())
	for deadline := time.Now().Add(2 * fuzzTimeout); ; {
		if time.Now().After(deadline) {
			t.Fatal("a client that never read its events was still connected")
		}
		cb := r.newID()
		err := r.send(displayID, displaySync, func(e *wire.Encoder) { e.Uint(uint32(cb)) })
		if err == nil {
			err = r.conn.Flush()
		}
		if errors.Is(err, wire.ErrWouldBlock) || errors.Is(err, wire.ErrOutputFull) {
			pfd := []unix.PollFd{{Fd: int32(r.conn.FD()), Events: unix.POLLOUT}}
			if _, err := unix.Poll(pfd, 100); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err != nil {
			break
		}
	}
	survives(t, h)
}

func TestStalledWindowsDoNotStallOthers(t *testing.T) {
	h := Start(t, small)
	hung := testclient.Connect(t, h.Socket())
	w := hung.Window("stalled", quadrants)
	hung.DispatchUntil("the window to map", w.Mapped)
	var done bool
	disconnected := false
	for deadline := time.Now().Add(2 * fuzzTimeout); !disconnected; {
		if time.Now().After(deadline) {
			t.Fatal("a client that never reads its frame callbacks was never disconnected")
		}
		var err error
		for range 1000 {
			if _, err = w.Surface.Frame(frameDone{&done}); err != nil {
				break
			}
		}
		if err == nil {
			err = errors.Join(w.Surface.Attach(hung.Buffer(w.Logical, 1, quadrants), 0, 0), w.Surface.Damage(0, 0, 10, 10), w.Surface.Commit(), hung.Display.Flush())
		}
		switch {
		case errors.Is(err, wire.ErrWouldBlock), errors.Is(err, wire.ErrOutputFull):
			time.Sleep(10 * time.Millisecond)
		case err != nil:
			disconnected = true
		}
	}
	for range 3 {
		survives(t, h)
	}
	h.Eventually("the other clients tiled", mappedToplevels(3))
}

func busyClient(t *testing.T, h *Harness, name string) *testclient.Client {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	seat, in := c.Seat()
	w := c.Window(name, quadrants)
	c.DispatchUntil(name+" to map", w.Mapped)
	cb := c.Clipboard(seat)
	src := c.DataSource(map[string]string{"text/plain": name})
	if err := errors.Join(cb.Device.SetSelection(src.Data, in.Serial), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDisconnectedClientsLeaveNothingBehind(t *testing.T) {
	h := StartUntraced(t, small)
	settled := func(what string) {
		t.Helper()
		h.Eventually(what, func(_ *headless.Backend, c *compositor.Compositor) bool { return len(c.Toplevels()) == 0 })
	}
	warm := busyClient(t, h, "warm")
	if err := warm.Display.Close(); err != nil {
		t.Fatal(err)
	}
	settled("the warm-up client gone")
	fds, goroutines := openFDs(t), runtime.NumGoroutine()
	clients := make([]*testclient.Client, 0, 5)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		clients = append(clients, busyClient(t, h, name))
	}
	for _, c := range clients {
		if err := c.Display.Close(); err != nil {
			t.Fatal(err)
		}
	}
	settled("every client gone")
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
	if n, g := openFDs(t), runtime.NumGoroutine(); n != fds || g != goroutines {
		t.Fatalf("after five clients came and went: %d file descriptors (%d before), %d goroutines (%d before)", n, fds, g, goroutines)
	}
}

func TestClientsLeavingABacklogDoNotStopTheCompositor(t *testing.T) {
	h := Start(t, small)
	gone := testclient.Connect(t, h.Socket())
	w := gone.Window("leaving", quadrants)
	gone.DispatchUntil("the window to map", w.Mapped)
	var done bool
	for range 20000 {
		if _, err := w.Surface.Frame(frameDone{&done}); err != nil || errors.Is(gone.Display.Flush(), wire.ErrWouldBlock) {
			break
		}
	}
	if err := errors.Join(w.Surface.Attach(gone.Buffer(w.Logical, 1, quadrants), 0, 0), w.Surface.Commit(), gone.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	if err := gone.Display.Close(); err != nil {
		t.Fatal(err)
	}
	survives(t, h)
	h.Eventually("the leaving client's window gone", mappedToplevels(1))
}

type deaf struct{}

func (deaf) Configure(*cxdg.XdgSurface, uint32) error { return nil }

type deafToplevel struct{}

func (deafToplevel) Configure(*cxdg.XdgToplevel, int32, int32, []byte) error { return nil }
func (deafToplevel) Close(*cxdg.XdgToplevel) error                           { return nil }
func (deafToplevel) ConfigureBounds(*cxdg.XdgToplevel, int32, int32) error   { return nil }
func (deafToplevel) WmCapabilities(*cxdg.XdgToplevel, []byte) error          { return nil }

func TestUnackedConfiguresDoNotStallOthers(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	for range 3 {
		s := c.Surface()
		xs, err := c.WM.GetXdgSurface(deaf{}, s)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := xs.GetToplevel(deafToplevel{}); err != nil {
			t.Fatal(err)
		}
		if err := s.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		survives(t, h)
	}
	h.Eventually("the windows that acked mapped", mappedToplevels(2))
}
