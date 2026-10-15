package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/wm"
)

const (
	SocketEnv     = "TILDE_SOCKET"
	maxRequest    = 64 << 10
	ioTimeout     = 5 * time.Second
	acceptBackoff = 100 * time.Millisecond
	watchBacklog  = 256
)

var ErrShutdown = errors.New("ipc: tilde is shutting down")

func SocketPath(runtimeDir, display string) (string, error) {
	if !filepath.IsAbs(runtimeDir) {
		return "", fmt.Errorf("ipc: XDG_RUNTIME_DIR %q is not an absolute path", runtimeDir)
	}
	return filepath.Join(runtimeDir, "tilde", display+".sock"), nil
}

type Handler interface {
	Snapshot() Snapshot
	Dispatch(a wm.Action) error
}

type watcher struct {
	lines chan []byte
}

type Server struct {
	loop    *loop.Loop
	handler Handler
	report  func(error)
	ln      *net.UnixListener
	done    chan struct{}
	closing sync.Once
	wg      sync.WaitGroup

	mu    sync.Mutex
	conns map[*net.UnixConn]struct{}

	watchers []*watcher
	last     Snapshot
	pending  []Event
}

func Listen(l *loop.Loop, path string, h Handler, report func(error)) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("ipc: create the socket directory: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("ipc: remove the stale socket: %w", err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("ipc: listen: %w", err)
	}
	ln.SetUnlinkOnClose(true)
	s := &Server{loop: l, handler: h, report: report, ln: ln, done: make(chan struct{}), conns: map[*net.UnixConn]struct{}{}}
	l.BeforeWait(s.publish)
	s.wg.Go(s.accept)
	return s, nil
}

func (s *Server) Path() string {
	return s.ln.Addr().String()
}

func (s *Server) Close() error {
	var err error
	s.closing.Do(func() {
		close(s.done)
		err = s.ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			_ = c.CloseRead()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	if err != nil {
		return fmt.Errorf("ipc: close the listener: %w", err)
	}
	return nil
}

func (s *Server) shut() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Server) post(f func()) {
	_ = s.loop.Post(func() error {
		f()
		return nil
	})
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.AcceptUnix()
		if s.shut() {
			if conn != nil {
				_ = conn.Close()
			}
			return
		}
		if err != nil {
			s.post(func() { s.report(fmt.Errorf("ipc: accept: %w", err)) })
			time.Sleep(acceptBackoff)
			continue
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Go(func() { s.serve(conn) })
	}
}

func (s *Server) forget(conn *net.UnixConn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	_ = conn.Close()
}

func readRequest(conn *net.UnixConn) ([]byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(ioTimeout)); err != nil {
		return nil, err
	}
	line, err := bufio.NewReaderSize(conn, maxRequest).ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		return nil, fmt.Errorf("%w: longer than %d bytes", ErrBadRequest, maxRequest)
	case errors.Is(err, io.EOF) && len(line) > 0:
		return line, nil
	}
	return line, err
}

func write(conn *net.UnixConn, line []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}
	_, err := conn.Write(line)
	return err
}

func (s *Server) reply(conn *net.UnixConn, result any, failure error) {
	resp := Response{Version: Version}
	if failure != nil {
		resp.Error = failure.Error()
	} else if result != nil {
		body, err := json.Marshal(result)
		if err != nil {
			resp.Error = fmt.Sprintf("ipc: encode the result: %v", err)
		}
		resp.Result = body
	}
	line, err := json.Marshal(resp)
	if err != nil {
		s.post(func() { s.report(fmt.Errorf("ipc: encode a response: %w", err)) })
		return
	}
	_ = write(conn, append(line, '\n'))
}

func (s *Server) onLoop(f func() (any, error)) (any, error) {
	type outcome struct {
		v   any
		err error
	}
	ch := make(chan outcome, 1)
	if err := s.loop.Post(func() error {
		v, err := f()
		ch <- outcome{v, err}
		return nil
	}); err != nil {
		return nil, ErrShutdown
	}
	select {
	case o := <-ch:
		return o.v, o.err
	case <-s.done:
	}
	select {
	case o := <-ch:
		return o.v, o.err
	default:
		return nil, ErrShutdown
	}
}

func (s *Server) serve(conn *net.UnixConn) {
	defer s.forget(conn)
	line, err := readRequest(conn)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.reply(conn, nil, err)
		}
		return
	}
	req, err := parseRequest(line)
	if err != nil {
		s.reply(conn, nil, err)
		return
	}
	if req.kind == KindWatch {
		s.watch(conn)
		return
	}
	result, err := s.onLoop(func() (any, error) {
		if req.kind == KindDispatch {
			return nil, s.handler.Dispatch(req.action)
		}
		return s.handler.Snapshot().answer(req.kind), nil
	})
	s.reply(conn, result, err)
}

func (s *Server) watch(conn *net.UnixConn) {
	w := &watcher{lines: make(chan []byte, watchBacklog)}
	if _, err := s.onLoop(func() (any, error) {
		if len(s.watchers) == 0 {
			s.last, s.pending = s.handler.Snapshot(), nil
		}
		s.watchers = append(s.watchers, w)
		return nil, nil
	}); err != nil {
		s.reply(conn, nil, err)
		return
	}
	defer s.post(func() { s.drop(w) })
	s.reply(conn, nil, nil)
	for {
		select {
		case line, ok := <-w.lines:
			if !ok || write(conn, line) != nil {
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *Server) drop(w *watcher) {
	if i := slices.Index(s.watchers, w); i >= 0 {
		s.watchers = slices.Delete(s.watchers, i, i+1)
		close(w.lines)
	}
}

func (s *Server) Dispatched(a wm.Action) {
	if len(s.watchers) > 0 {
		s.pending = append(s.pending, DispatchEvent{Action: a.String()})
	}
}

func (s *Server) publish() error {
	if len(s.watchers) == 0 || s.shut() {
		return nil
	}
	next := s.handler.Snapshot()
	events := append(s.pending, Diff(s.last, next)...)
	s.last, s.pending = next, nil
	for _, e := range events {
		line, err := Encode(e)
		if err != nil {
			s.report(err)
			continue
		}
		s.watchers = slices.DeleteFunc(s.watchers, func(w *watcher) bool {
			select {
			case w.lines <- line:
				return false
			default:
				close(w.lines)
				return true
			}
		})
	}
	return nil
}
