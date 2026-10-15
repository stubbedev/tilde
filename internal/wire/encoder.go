package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

type Encoder struct {
	c       *Conn
	object  ObjectID
	opcode  Opcode
	start   int
	fdStart int
	err     error
	open    bool
	discard bool
}

func (c *Conn) Begin(object ObjectID, opcode Opcode) *Encoder {
	if c.enc.open {
		panic("wire: Begin called before the previous message was ended")
	}
	c.enc = Encoder{
		c:       c,
		object:  object,
		opcode:  opcode,
		start:   len(c.out),
		fdStart: len(c.fdsOut),
		open:    true,
	}
	e := &c.enc
	if e.reserve(headerSize) {
		c.out = binary.NativeEndian.AppendUint32(c.out, uint32(object))
		c.out = binary.NativeEndian.AppendUint32(c.out, 0)
	}
	return e
}

func (e *Encoder) fail(err error) {
	if e.err == nil {
		e.err = &MessageError{e.object, e.opcode, len(e.c.out) - e.start, err}
	}
}

func (e *Encoder) flushBefore() error {
	err := e.c.send(e.start, e.fdStart)
	if errors.Is(err, ErrWouldBlock) {
		return ErrOutputFull
	}
	return err
}

func (e *Encoder) reserve(n int) bool {
	if e.err != nil {
		return false
	}
	c := e.c
	if len(c.out)-e.start+n > maxMessageSize {
		e.fail(ErrMessageTooLarge)
		return false
	}
	if len(c.out)+n <= cap(c.out) {
		return true
	}
	if err := e.flushBefore(); err != nil {
		e.fail(err)
		return false
	}
	if len(c.out)+n > cap(c.out) {
		e.fail(ErrOutputFull)
		return false
	}
	return true
}

func (e *Encoder) word(v uint32) {
	if e.reserve(4) {
		e.c.out = binary.NativeEndian.AppendUint32(e.c.out, v)
	}
}

func (e *Encoder) Int(v int32) {
	e.word(uint32(v))
}

func (e *Encoder) Uint(v uint32) {
	e.word(v)
}

func (e *Encoder) Fixed(v Fixed) {
	e.word(uint32(v))
}

func (e *Encoder) Object(id ObjectID) {
	e.word(uint32(id))
}

func putBlob[T ~string | ~[]byte](e *Encoder, b T, terminated bool) {
	n := len(b)
	if terminated {
		n++
	}
	if !e.reserve(4 + pad(n)) {
		return
	}
	c := e.c
	c.out = binary.NativeEndian.AppendUint32(c.out, uint32(n))
	c.out = append(c.out, b...)
	for range pad(n) - len(b) {
		c.out = append(c.out, 0)
	}
}

func (e *Encoder) Str(s string) {
	if strings.IndexByte(s, 0) >= 0 {
		e.fail(ErrEmbeddedNUL)
		return
	}
	putBlob(e, s, true)
}

func (e *Encoder) NullStr() {
	e.word(0)
}

func (e *Encoder) Array(b []byte) {
	putBlob(e, b, false)
}

func (e *Encoder) FD(fd int) {
	if e.err != nil {
		return
	}
	c := e.c
	if len(c.fdsOut) == maxFDsOut {
		if err := e.flushBefore(); err != nil {
			e.fail(err)
			return
		}
		if len(c.fdsOut) == maxFDsOut {
			e.fail(fmt.Errorf("%w: more than %d in one message", ErrTooManyFDs, maxFDsOut))
			return
		}
	}
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		e.fail(fmt.Errorf("duplicate fd %d: %w", fd, err))
		return
	}
	c.fdsOut = append(c.fdsOut, dup)
}

func (e *Encoder) End() error {
	e.open = false
	c := e.c
	if e.err != nil || e.discard {
		for _, fd := range c.fdsOut[e.fdStart:] {
			_ = unix.Close(fd)
		}
		c.fdsOut = c.fdsOut[:e.fdStart]
		c.out = c.out[:e.start]
		if e.discard {
			return nil
		}
		c.broken(e.err)
		if c.defers {
			return nil
		}
		return e.err
	}
	size := len(c.out) - e.start
	binary.NativeEndian.PutUint32(c.out[e.start+4:], uint32(size)<<16|uint32(e.opcode))
	if c.tracer != nil {
		c.tracer(false, e.object, e.opcode, c.out[e.start+headerSize:])
	}
	return nil
}
