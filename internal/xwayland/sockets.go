package xwayland

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const maxDisplays = 32

type sockets struct {
	n        int
	lockPath string
	path     string
	fds      [2]int
}

func bindDisplay(dir string) (*sockets, error) {
	socketDir := filepath.Join(dir, ".X11-unix")
	switch err := os.Mkdir(socketDir, 0o700); {
	case errors.Is(err, os.ErrExist):
	case err != nil:
		return nil, fmt.Errorf("xwayland: make %s: %w", socketDir, err)
	default:
		if err := os.Chmod(socketDir, 0o777|os.ModeSticky); err != nil {
			return nil, fmt.Errorf("xwayland: open %s to every X server: %w", socketDir, err)
		}
	}
	for n := range maxDisplays {
		lock := filepath.Join(dir, fmt.Sprintf(".X%d-lock", n))
		path := filepath.Join(socketDir, fmt.Sprintf("X%d", n))
		taken, err := takeLock(lock, path)
		if err != nil {
			return nil, err
		}
		if !taken {
			continue
		}
		s := &sockets{n: n, lockPath: lock, path: path, fds: [2]int{-1, -1}}
		err = s.listen()
		if err == nil {
			return s, nil
		}
		err = errors.Join(err, s.close())
		if !errors.Is(err, unix.EADDRINUSE) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("xwayland: displays :0 to :%d in %s are all in use", maxDisplays-1, dir)
}

func takeLock(lock, socket string) (bool, error) {
	for range 2 {
		f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444) //nolint:gosec // other X servers read the lock to see the display is taken
		if err == nil {
			_, err = fmt.Fprintf(f, "%10d\n", os.Getpid())
			if err = errors.Join(err, f.Close()); err != nil {
				return false, errors.Join(fmt.Errorf("xwayland: write %s: %w", lock, err), os.Remove(lock))
			}
			return true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return false, fmt.Errorf("xwayland: create %s: %w", lock, err)
		}
		if live, err := held(lock); err != nil || live {
			return false, err
		}
		for _, stale := range []string{lock, socket} {
			if err := os.Remove(stale); err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, fmt.Errorf("xwayland: remove stale %s: %w", stale, err)
			}
		}
	}
	return false, nil
}

func held(lock string) (bool, error) {
	data, err := os.ReadFile(lock) //nolint:gosec // the lock path is built from the display number
	if err != nil {
		return false, fmt.Errorf("xwayland: read %s: %w", lock, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return true, nil
	}
	return !errors.Is(unix.Kill(pid, 0), unix.ESRCH), nil
}

func (s *sockets) listen() error {
	for i, name := range []string{"@" + s.path, s.path} {
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("xwayland: socket for %s: %w", name, err)
		}
		s.fds[i] = fd
		if i == 1 {
			if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("xwayland: remove stale %s: %w", s.path, err)
			}
		}
		if err := unix.Bind(fd, &unix.SockaddrUnix{Name: name}); err != nil {
			return fmt.Errorf("xwayland: bind %s: %w", name, err)
		}
		if err := unix.Listen(fd, 1); err != nil {
			return fmt.Errorf("xwayland: listen on %s: %w", name, err)
		}
	}
	return nil
}

func (s *sockets) close() error {
	var errs []error
	for i, fd := range s.fds {
		if fd >= 0 {
			errs = append(errs, unix.Close(fd))
			s.fds[i] = -1
		}
	}
	for _, p := range []string{s.path, s.lockPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("xwayland: release display :%d: %w", s.n, err)
	}
	return nil
}
