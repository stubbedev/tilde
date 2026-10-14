package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/wm"
)

type fake struct {
	snap       Snapshot
	dispatched []wm.Action
	server     *Server
}

func (f *fake) Snapshot() Snapshot {
	snap := f.snap
	snap.Windows = append([]Window(nil), f.snap.Windows...)
	return snap
}

func (f *fake) Dispatch(a wm.Action) error {
	if _, ok := a.(wm.KillAction); ok {
		return errors.New("nothing to kill")
	}
	f.dispatched = append(f.dispatched, a)
	f.server.Dispatched(a)
	return nil
}

type rig struct {
	t      *testing.T
	loop   *loop.Loop
	server *Server
	fake   *fake
	path   string
	errs   chan error
}

func start(t *testing.T) *rig {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, loop: l, fake: &fake{}, path: filepath.Join(t.TempDir(), "tilde", "wayland-9.sock"), errs: make(chan error, 16)}
	if r.server, err = Listen(l, r.path, r.fake, func(err error) { r.errs <- err }); err != nil {
		t.Fatal(err)
	}
	r.fake.server = r.server
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		if err := l.Post(func() error {
			l.Stop()
			return nil
		}); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
		if err := r.server.Close(); err != nil {
			t.Error(err)
		}
		if err := l.Close(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-r.errs:
			t.Errorf("reported: %v", err)
		default:
		}
	})
	return r
}

func (r *rig) do(f func(*fake)) {
	r.t.Helper()
	ran := make(chan struct{})
	if err := r.loop.Post(func() error {
		f(r.fake)
		close(ran)
		return nil
	}); err != nil {
		r.t.Fatal(err)
	}
	<-ran
}

func (r *rig) raw(line string) Response {
	r.t.Helper()
	c, err := net.Dial("unix", r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte(line)); err != nil {
		r.t.Fatal(err)
	}
	if err := c.(*net.UnixConn).CloseWrite(); err != nil {
		r.t.Fatal(err)
	}
	var resp Response
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		r.t.Fatal(err)
	}
	return resp
}

func TestQueriesAndDispatch(t *testing.T) {
	r := start(t)
	if got, err := Do(r.path, Request{Request: KindVersion}); err != nil || string(got) != "1" {
		t.Fatalf("version %s, %v", got, err)
	}
	if got, err := Do(r.path, Request{Request: KindWindows}); err != nil || string(got) != "[]" {
		t.Fatalf("no windows gave %s, %v", got, err)
	}
	if got, err := Do(r.path, Request{Request: KindActiveWindow}); err != nil || got != nil {
		t.Fatalf("no active window gave %s, %v", got, err)
	}
	r.do(func(f *fake) { f.snap.Windows = []Window{{ID: 7, Title: "seven", Focused: true}} })
	got, err := Do(r.path, Request{Request: KindActiveWindow})
	var w Window
	if err != nil || json.Unmarshal(got, &w) != nil || w.ID != 7 || w.Title != "seven" {
		t.Fatalf("active window %s, %v", got, err)
	}
	if _, err := Do(r.path, Request{Request: KindDispatch, Action: "workspace 2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Do(r.path, Request{Request: KindDispatch, Action: "kill"}); err == nil || err.Error() != "tilde: nothing to kill" {
		t.Fatalf("a failing dispatch: %v", err)
	}
	r.do(func(f *fake) {
		if len(f.dispatched) != 1 || f.dispatched[0] != (wm.WorkspaceAction{Target: wm.WorkspaceTarget{ID: 2}}) {
			t.Errorf("dispatched %v", f.dispatched)
		}
	})
	if _, err := Do(r.path, Request{Request: "teleport"}); err == nil || !strings.Contains(err.Error(), `unknown request "teleport"`) {
		t.Fatalf("an unknown request: %v", err)
	}
	if resp := r.raw(strings.Repeat("x", maxRequest+10)); !strings.Contains(resp.Error, "longer than") || resp.Version != Version {
		t.Fatalf("an oversized request: %+v", resp)
	}
	if resp := r.raw(`{"request":"version"}`); resp.Error != "" || string(resp.Result) != "1" {
		t.Fatalf("a request without a newline: %+v", resp)
	}
}

func watch(t *testing.T, path string) (<-chan string, func()) {
	t.Helper()
	lines := make(chan string, 1024)
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte(`{"request":"watch"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(c)
	first, err := r.ReadString('\n')
	if err != nil || first != `{"version":1}`+"\n" {
		t.Fatalf("watch answered %q, %v", first, err)
	}
	go func() {
		defer close(lines)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			lines <- strings.TrimSpace(line)
		}
	}()
	return lines, func() { _ = c.Close() }
}

func next(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatal("the event stream ended")
		}
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5s")
	}
	return ""
}

func TestWatchStreamsDiffsAndDispatches(t *testing.T) {
	r := start(t)
	r.do(func(f *fake) { f.snap.Submap = "resize" })
	lines, stop := watch(t, r.path)
	defer stop()
	if _, err := Do(r.path, Request{Request: KindDispatch, Action: "submap reset"}); err != nil {
		t.Fatal(err)
	}
	r.do(func(f *fake) {
		f.snap.Submap = ""
		f.snap.Windows = []Window{{ID: 1, Title: "one"}}
	})
	for _, want := range []string{
		`{"event":"dispatch","action":"submap reset"}`,
		`{"event":"open","window":{"id":1,"app_id":"","title":"one","pid":0,"workspace":0,"output":"","rect":{"x":0,"y":0,"width":0,"height":0},"floating":false,"fullscreen":false,"urgent":false,"focused":false}}`,
		`{"event":"submap","submap":""}`,
	} {
		if got := next(t, lines); got != want {
			t.Fatalf("event %s, want %s", got, want)
		}
	}
}

func TestSlowWatchersAreDropped(t *testing.T) {
	r := start(t)
	c, err := net.Dial("unix", r.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte(`{"request":"watch"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	fast, stop := watch(t, r.path)
	defer stop()
	for i := range watchBacklog * 4 {
		r.do(func(f *fake) { f.snap.Submap = strings.Repeat("s", i%2+1) + strings.Repeat("x", 4096) })
		next(t, fast)
	}
	r.do(func(f *fake) {
		if n := len(r.server.watchers); n != 1 {
			t.Errorf("%d watchers left, want only the fast one", n)
		}
	})
}

func TestCloseWithClientsConnected(t *testing.T) {
	r := start(t)
	lines, stop := watch(t, r.path)
	defer stop()
	idle, err := net.Dial("unix", r.path)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	closed := make(chan error, 1)
	go func() { closed <- r.server.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(ioTimeout / 2):
		t.Fatal("Close waited for connected clients")
	}
	if _, ok := <-lines; ok {
		t.Fatal("the event stream outlived the server")
	}
}

func TestFindSocket(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"XDG_RUNTIME_DIR": dir}
	getenv := func(k string) string { return env[k] }
	if _, err := FindSocket(getenv); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("no sockets: %v", err)
	}
	r := start(t)
	env["XDG_RUNTIME_DIR"] = filepath.Dir(filepath.Dir(r.path))
	if got, err := FindSocket(getenv); err != nil || got != r.path {
		t.Fatalf("one socket: %q, %v", got, err)
	}
	env[SocketEnv] = "/elsewhere.sock"
	if got, err := FindSocket(getenv); err != nil || got != "/elsewhere.sock" {
		t.Fatalf("TILDE_SOCKET: %q, %v", got, err)
	}
}
