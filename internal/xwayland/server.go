// Package xwayland runs Xwayland for X11 clients: it holds the X display
// sockets, starts Xwayland on the first connection, and matches X windows
// to their wl_surfaces through xwayland_shell_v1.
package xwayland

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/data"
	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
)

const Env = "TILDE_XWAYLAND"

var defaultXwayland = "Xwayland"

func Path(getenv func(string) string) string {
	if p := getenv(Env); p != "" {
		return p
	}
	return defaultXwayland
}

type Options struct {
	Binary   string
	Dir      string
	Env      []string
	Log      io.Writer
	Report   func(error)
	Listener Listener
}

type Server struct {
	loop    *loop.Loop
	display *server.Display
	opts    Options
	socks   *sockets
	watch   []*loop.Source
	shell   *Shell
	proc    *process
	closed  bool
}

type process struct {
	cmd    *exec.Cmd
	client *server.Client
	wm     *wm
	notify int
	ready  *loop.Source
	line   []byte
	up     bool
}

func Listen(l *loop.Loop, d *server.Display, opts Options) (*Server, error) {
	if opts.Listener == nil || opts.Report == nil {
		return nil, errors.New("xwayland: Options.Listener and Options.Report are required")
	}
	binary, err := exec.LookPath(opts.Binary)
	if err != nil {
		return nil, fmt.Errorf("xwayland: find %s (set %s to its path): %w", opts.Binary, Env, err)
	}
	opts.Binary = binary
	socks, err := bindDisplay(opts.Dir)
	if err != nil {
		return nil, err
	}
	s := &Server{loop: l, display: d, opts: opts, socks: socks}
	if s.shell, err = newShell(d, s.isXwayland, s.associated, s.dissociated); err != nil {
		return nil, errors.Join(err, socks.close())
	}
	if err := s.arm(); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}

func (s *Server) Display() string {
	return ":" + strconv.Itoa(s.socks.n)
}

func (s *Server) Shell() *Shell {
	return s.shell
}

func (s *Server) Pid() int {
	if s.proc == nil {
		return 0
	}
	return s.proc.cmd.Process.Pid
}

func (s *Server) Running() bool {
	return s.proc != nil && s.proc.up
}

func (s *Server) isXwayland(c *server.Client) bool {
	return s.proc != nil && s.proc.client == c
}

func (s *Server) arm() error {
	for _, fd := range s.socks.fds {
		src, err := s.loop.AddFD(fd, loop.Readable, s.connecting)
		if err != nil {
			return fmt.Errorf("xwayland: watch %s: %w", s.Display(), err)
		}
		s.watch = append(s.watch, src)
	}
	return nil
}

func (s *Server) disarm() error {
	var errs []error
	for _, src := range s.watch {
		errs = append(errs, src.Remove())
	}
	s.watch = s.watch[:0]
	return errors.Join(errs...)
}

func (s *Server) connecting(loop.Events) error {
	if err := s.disarm(); err != nil {
		return err
	}
	if err := s.start(); err != nil {
		return errors.Join(fmt.Errorf("xwayland: start %s for %s: %w", s.opts.Binary, s.Display(), err), s.arm())
	}
	return nil
}

func pair() ([2]int, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	return [2]int{fds[0], fds[1]}, err
}

func (s *Server) start() error {
	var mine, theirs []int
	fail := func(err error) error {
		for _, fd := range append(mine, theirs...) {
			_ = unix.Close(fd)
		}
		return err
	}
	wayland, err := pair()
	if err != nil {
		return fmt.Errorf("socketpair for Wayland: %w", err)
	}
	mine, theirs = append(mine, wayland[0]), append(theirs, wayland[1])
	wm, err := pair()
	if err != nil {
		return fail(fmt.Errorf("socketpair for the window manager: %w", err))
	}
	mine, theirs = append(mine, wm[0]), append(theirs, wm[1])
	var notify [2]int
	if err := unix.Pipe2(notify[:], unix.O_CLOEXEC); err != nil {
		return fail(fmt.Errorf("pipe for -displayfd: %w", err))
	}
	mine, theirs = append(mine, notify[0]), append(theirs, notify[1])
	var listen [2]int
	for i, fd := range s.socks.fds {
		if listen[i], err = unix.Dup(fd); err != nil {
			return fail(fmt.Errorf("dup the X socket: %w", err))
		}
		theirs = append(theirs, listen[i])
	}
	child := []int{listen[0], listen[1], notify[1], wm[1], wayland[1]}
	files := make([]*os.File, len(child))
	for i, fd := range child {
		files[i] = os.NewFile(uintptr(fd), "xwayland")
	}
	cmd := exec.Command(s.opts.Binary, s.Display(), "-rootless", "-core", "-listenfd", "3", "-listenfd", "4", "-displayfd", "5", "-wm", "6") //nolint:gosec // the binary comes from the build or TILDE_XWAYLAND
	cmd.ExtraFiles = files
	cmd.Env = append(slices.DeleteFunc(slices.Clone(s.opts.Env), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == "WAYLAND_DISPLAY" || name == "WAYLAND_SOCKET" || name == "DISPLAY"
	}), "WAYLAND_SOCKET=7")
	cmd.Stdout, cmd.Stderr = s.opts.Log, s.opts.Log
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	err = cmd.Start()
	for _, f := range files {
		_ = f.Close()
	}
	theirs = nil
	if err != nil {
		return fail(err)
	}
	p := &process{cmd: cmd, notify: notify[0]}
	s.proc = p
	go func() {
		err := cmd.Wait()
		_ = s.loop.Post(func() error { return s.exited(p, err) })
	}()
	wmFile := os.NewFile(uintptr(wm[0]), "xwayland-wm")
	conn, err := net.FileConn(wmFile)
	if err = errors.Join(err, wmFile.Close()); err != nil {
		return errors.Join(fmt.Errorf("the window manager socket: %w", err), unix.Close(wayland[0]), s.kill(p))
	}
	go s.manage(p, conn)
	if p.client, err = s.display.AddClient(wayland[0]); err != nil {
		return errors.Join(err, unix.Close(wayland[0]), s.kill(p))
	}
	if p.ready, err = s.loop.AddFD(p.notify, loop.Readable, func(loop.Events) error { return s.notified(p) }); err != nil {
		return errors.Join(err, s.kill(p))
	}
	return nil
}

func (s *Server) notified(p *process) error {
	var buf [16]byte
	n, err := unix.Read(p.notify, buf[:])
	switch {
	case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EINTR):
		return nil
	case err != nil:
		return errors.Join(fmt.Errorf("xwayland: read -displayfd: %w", err), s.stopNotify(p))
	case n == 0:
		return s.stopNotify(p)
	}
	p.line = append(p.line, buf[:n]...)
	if !bytes.ContainsRune(p.line, '\n') {
		return nil
	}
	if got := strings.TrimSpace(string(p.line)); got != strconv.Itoa(s.socks.n) {
		return errors.Join(fmt.Errorf("xwayland: started on display %q, want %s", got, s.Display()), s.stopNotify(p))
	}
	p.up = true
	return s.stopNotify(p)
}

func (s *Server) stopNotify(p *process) error {
	var errs []error
	if p.ready != nil {
		errs = append(errs, p.ready.Remove())
		p.ready = nil
	}
	if p.notify >= 0 {
		errs = append(errs, unix.Close(p.notify))
		p.notify = -1
	}
	return errors.Join(errs...)
}

func (p *process) release() error {
	if p.wm == nil {
		return nil
	}
	err := p.wm.close()
	p.wm.x.Close()
	p.wm = nil
	return err
}

func (s *Server) kill(p *process) error {
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("xwayland: kill pid %d: %w", p.cmd.Process.Pid, err)
	}
	return nil
}

func (s *Server) exited(p *process, status error) error {
	if s.proc != p {
		return nil
	}
	s.proc = nil
	errs := []error{s.stopNotify(p), p.release()}
	if s.closed {
		return errors.Join(errs...)
	}
	if status == nil {
		status = errors.New("exit status 0")
	}
	s.opts.Report(fmt.Errorf("xwayland: Xwayland on %s exited (%w); the next X client starts it again", s.Display(), status))
	return errors.Join(append(errs, s.arm())...)
}

func (s *Server) manage(p *process, conn net.Conn) {
	c, err := claim(conn)
	if err != nil {
		_ = conn.Close()
		s.report(fmt.Errorf("xwayland: %s: %w", s.Display(), err))
		return
	}
	c.report = s.report
	c.post = func(f func(*wm) error) {
		_ = s.loop.Post(func() error {
			if s.proc != p || p.wm == nil {
				return nil
			}
			return f(p.wm)
		})
	}
	_ = s.loop.Post(func() error {
		if s.proc != p {
			c.x.Close()
			return nil
		}
		m := &wm{
			x: c.x, root: c.root, sel: c.sel, atoms: c.atoms, windows: map[xproto.Window]*Window{}, shell: s.shell,
			listener: s.opts.Listener, client: p.client, outgoing: map[outKey]*outgoing{},
			post: func(f func() error) { _ = s.loop.Post(f) },
		}
		for _, k := range []data.Kind{data.Clipboard, data.Primary} {
			m.selections[k] = &selection{kind: k, atom: c.atoms[[]string{"CLIPBOARD", "PRIMARY"}[k]]}
		}
		p.wm = m
		var errs []error
		for _, k := range []data.Kind{data.Clipboard, data.Primary} {
			errs = append(errs, m.selectionChanged(k, s.opts.Listener.CurrentSelection(k)))
		}
		return errors.Join(errs...)
	})
	c.run()
}

func (s *Server) SelectionChanged(k data.Kind, src data.Source) error {
	if s.proc == nil || s.proc.wm == nil {
		return nil
	}
	return s.proc.wm.selectionChanged(k, src)
}

func (s *Server) associated(serial uint64, sf *surface.Surface) error {
	if s.proc == nil || s.proc.wm == nil {
		return nil
	}
	return s.proc.wm.associated(serial, sf)
}

func (s *Server) dissociated(serial uint64) error {
	if s.proc == nil || s.proc.wm == nil {
		return nil
	}
	return s.proc.wm.dissociated(serial)
}

func (s *Server) report(err error) {
	_ = s.loop.Post(func() error {
		s.opts.Report(err)
		return nil
	})
}

func (s *Server) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	errs := []error{s.disarm()}
	if p := s.proc; p != nil {
		errs = append(errs, s.kill(p), s.stopNotify(p), p.release())
		s.proc = nil
	}
	return errors.Join(append(errs, s.shell.close(), s.socks.close())...)
}
