package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unsafe"

	"golang.org/x/sys/unix"
)

type Conn struct {
	fd     int
	in     []byte
	r, w   int
	fdsIn  fdQueue
	oobIn  []byte
	rmsg   unix.Msghdr
	riov   unix.Iovec
	msg    Message
	out    []byte
	fdsOut []int
	oobOut []byte
	enc    Encoder
	werr   error
	tracer Tracer
	closed bool
	defers bool
}

type Tracer func(incoming bool, id ObjectID, opcode Opcode, body []byte)

func (c *Conn) SetTracer(t Tracer) {
	c.tracer = t
}

func (c *Conn) DeferSendErrors() {
	c.defers = true
}

func (c *Conn) Err() error {
	return c.werr
}

func (c *Conn) broken(err error) {
	if c.werr == nil {
		c.werr = err
	}
}

func NewConn(fd int) *Conn {
	return &Conn{
		fd:     fd,
		in:     make([]byte, bufferSize),
		fdsIn:  newFDQueue(maxFDsIn),
		oobIn:  make([]byte, unix.CmsgSpace(maxFDsPerRecv*4)),
		out:    make([]byte, 0, bufferSize),
		fdsOut: make([]int, 0, maxFDsOut),
		oobOut: make([]byte, unix.CmsgSpace(maxFDsOut*4)),
	}
}

func (c *Conn) FD() int {
	return c.fd
}

func (c *Conn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	c.fdsIn.closeFirst(c.fdsIn.n)
	for _, fd := range c.fdsOut {
		_ = unix.Close(fd)
	}
	c.fdsOut = c.fdsOut[:0]
	if err := unix.Close(c.fd); err != nil {
		return fmt.Errorf("wire: close socket fd %d: %w", c.fd, err)
	}
	return nil
}

func (c *Conn) Fill() error {
	if c.msg.open {
		panic("wire: Fill called before the current message was finished")
	}
	if c.r > 0 {
		c.w = copy(c.in, c.in[c.r:c.w])
		c.r = 0
	}
	for c.w < len(c.in) {
		n, oobn, flags, err := c.recvmsg(c.in[c.w:])
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EAGAIN):
			return nil
		case err != nil:
			return fmt.Errorf("wire: recvmsg on fd %d: %w", c.fd, err)
		}
		if err := c.takeRights(c.oobIn[:oobn]); err != nil {
			return err
		}
		if flags&unix.MSG_CTRUNC != 0 {
			return fmt.Errorf("wire: recvmsg on fd %d: control message truncated: %w", c.fd, ErrTooManyFDs)
		}
		if n == 0 {
			return io.EOF
		}
		c.w += n
	}
	return nil
}

func (c *Conn) recvmsg(p []byte) (n, oobn, flags int, err error) {
	c.riov.Base = &p[0]
	c.riov.SetLen(len(p))
	c.rmsg = unix.Msghdr{Iov: &c.riov, Control: &c.oobIn[0]}
	c.rmsg.SetIovlen(1)
	c.rmsg.SetControllen(len(c.oobIn))
	r, _, errno := unix.Syscall(unix.SYS_RECVMSG, uintptr(c.fd), uintptr(unsafe.Pointer(&c.rmsg)), unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC) //nolint:gosec // rmsg points only at buffers owned by c
	if errno != 0 {
		return 0, 0, 0, errno
	}
	return int(r), int(c.rmsg.Controllen), int(c.rmsg.Flags), nil
}

func (c *Conn) takeRights(oob []byte) error {
	var overflow bool
	for len(oob) > 0 {
		hdr, data, rest, err := unix.ParseOneSocketControlMessage(oob)
		if err != nil {
			return fmt.Errorf("wire: parse control message on fd %d: %w", c.fd, err)
		}
		oob = rest
		if hdr.Level != unix.SOL_SOCKET || hdr.Type != unix.SCM_RIGHTS {
			continue
		}
		for ; len(data) >= 4; data = data[4:] {
			fd := int(int32(binary.NativeEndian.Uint32(data)))
			if overflow || !c.fdsIn.push(fd) {
				overflow = true
				_ = unix.Close(fd)
			}
		}
	}
	if overflow {
		return fmt.Errorf("wire: fd %d: more than %d queued file descriptors: %w", c.fd, maxFDsIn, ErrTooManyFDs)
	}
	return nil
}

func (c *Conn) Next() (*Message, error) {
	if c.msg.open {
		panic("wire: Next called before the current message was finished")
	}
	if c.w-c.r < headerSize {
		return nil, nil
	}
	object, opcode, size, err := parseHeader(c.in[c.r:c.w])
	if err != nil {
		return nil, err
	}
	if c.w-c.r < size {
		return nil, nil
	}
	c.msg = Message{
		ID:     object,
		Opcode: opcode,
		data:   c.in[c.r : c.r+size],
		off:    headerSize,
		fds:    &c.fdsIn,
		open:   true,
	}
	c.r += size
	if c.tracer != nil {
		c.tracer(true, object, opcode, c.msg.data[headerSize:])
	}
	return &c.msg, nil
}

func (c *Conn) Flush() error {
	limit, fdLimit := len(c.out), len(c.fdsOut)
	if c.enc.open {
		limit, fdLimit = c.enc.start, c.enc.fdStart
	}
	return c.send(limit, fdLimit)
}

func (c *Conn) send(limit, fdLimit int) error {
	sent := 0
	defer func() { c.consume(sent) }()
	for sent < limit {
		oob := c.rights(fdLimit)
		n, err := unix.SendmsgN(c.fd, c.out[sent:limit], oob, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EAGAIN):
			return ErrWouldBlock
		case err != nil:
			err = fmt.Errorf("wire: sendmsg on fd %d: %w", c.fd, err)
			c.broken(err)
			return err
		}
		if fdLimit > 0 {
			c.dropFDsOut(fdLimit)
			fdLimit = 0
		}
		sent += n
	}
	return nil
}

func (c *Conn) rights(n int) []byte {
	if n == 0 {
		return nil
	}
	oob := c.oobOut[:unix.CmsgSpace(n*4)]
	hdr := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0])) //nolint:gosec // oobOut is sized by CmsgSpace and aligned by make
	hdr.Level = unix.SOL_SOCKET
	hdr.Type = unix.SCM_RIGHTS
	hdr.SetLen(unix.CmsgLen(n * 4))
	data := oob[unix.CmsgLen(0):]
	for i, fd := range c.fdsOut[:n] {
		binary.NativeEndian.PutUint32(data[i*4:], uint32(fd))
	}
	return oob
}

func (c *Conn) dropFDsOut(n int) {
	for _, fd := range c.fdsOut[:n] {
		_ = unix.Close(fd)
	}
	c.fdsOut = c.fdsOut[:copy(c.fdsOut, c.fdsOut[n:])]
	if c.enc.open {
		c.enc.fdStart -= n
	}
}

func (c *Conn) consume(n int) {
	if n == 0 {
		return
	}
	c.out = c.out[:copy(c.out, c.out[n:])]
	if c.enc.open {
		c.enc.start -= n
	}
}
