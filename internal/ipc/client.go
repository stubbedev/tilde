package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"
)

var ErrNoSocket = errors.New("ipc: no lys-wm socket")

func FindSocket(getenv func(string) string) (string, error) {
	if path := getenv(SocketEnv); path != "" {
		return path, nil
	}
	dir := getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w: neither %s nor XDG_RUNTIME_DIR is set", ErrNoSocket, SocketEnv)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "lys-wm", "*.sock"))
	if err != nil {
		return "", fmt.Errorf("ipc: look for sockets: %w", err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("%w: %s is unset and %s holds %d sockets %v", ErrNoSocket, SocketEnv, filepath.Join(dir, "lys-wm"), len(matches), matches)
	}
	return matches[0], nil
}

type conn struct {
	c *net.UnixConn
	r *bufio.Reader
}

func send(path string, req Request) (*conn, error) {
	c, err := net.DialTimeout("unix", path, ioTimeout)
	if err != nil {
		return nil, fmt.Errorf("ipc: connect to %s: %w", path, err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("ipc: %s is not a Unix socket", path)
	}
	line, err := json.Marshal(req)
	if err == nil {
		err = write(uc, append(line, '\n'))
	}
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("ipc: send %s: %w", req.Request, err)
	}
	return &conn{c: uc, r: bufio.NewReader(uc)}, nil
}

func (cn *conn) response() (json.RawMessage, error) {
	if err := cn.c.SetReadDeadline(time.Now().Add(ioTimeout)); err != nil {
		return nil, err
	}
	line, err := cn.r.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("ipc: read the response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("ipc: decode the response %q: %w", line, err)
	}
	if resp.Version != Version {
		return nil, fmt.Errorf("ipc: lys-wm speaks IPC version %d, this client version %d", resp.Version, Version)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("lys-wm: %s", resp.Error)
	}
	return resp.Result, nil
}

func (cn *conn) close() error {
	if err := cn.c.Close(); err != nil {
		return fmt.Errorf("ipc: close: %w", err)
	}
	return nil
}

func Do(path string, req Request) (result json.RawMessage, err error) {
	if req.Request == KindWatch {
		return nil, fmt.Errorf("%w: use Watch for the event stream", ErrBadRequest)
	}
	cn, err := send(path, req)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, cn.close()) }()
	return cn.response()
}

type Stream struct {
	cn *conn
}

func Subscribe(path string) (*Stream, error) {
	cn, err := send(path, Request{Request: KindWatch})
	if err != nil {
		return nil, err
	}
	if _, err := cn.response(); err != nil {
		return nil, errors.Join(err, cn.close())
	}
	if err := cn.c.SetReadDeadline(time.Time{}); err != nil {
		return nil, errors.Join(err, cn.close())
	}
	return &Stream{cn: cn}, nil
}

func (s *Stream) Next() ([]byte, error) {
	line, err := s.cn.r.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("ipc: read the event stream: %w", err)
	}
	return line, nil
}

func (s *Stream) Close() error {
	return s.cn.close()
}
