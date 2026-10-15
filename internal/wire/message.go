package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type Message struct {
	ID     ObjectID
	Opcode Opcode
	data   []byte
	off    int
	fds    *fdQueue
	taken  int
	err    error
	open   bool
}

func parseHeader(b []byte) (ObjectID, Opcode, int, error) {
	object := ObjectID(binary.NativeEndian.Uint32(b))
	word := binary.NativeEndian.Uint32(b[4:])
	opcode := Opcode(word & 0xffff)
	size := int(word >> 16)
	if size < headerSize || size%4 != 0 {
		return object, opcode, 0, &MessageError{object, opcode, 4, fmt.Errorf("%w: %d bytes", ErrMessageSize, size)}
	}
	return object, opcode, size, nil
}

func (m *Message) fail(off int, err error) {
	if m.err == nil {
		m.err = &MessageError{m.ID, m.Opcode, off, err}
	}
}

func (m *Message) word() uint32 {
	if m.err != nil {
		return 0
	}
	if len(m.data)-m.off < 4 {
		m.fail(m.off, ErrTruncated)
		return 0
	}
	v := binary.NativeEndian.Uint32(m.data[m.off:])
	m.off += 4
	return v
}

func (m *Message) Int() int32 {
	return int32(m.word())
}

func (m *Message) Uint() uint32 {
	return m.word()
}

func (m *Message) Fixed() Fixed {
	return Fixed(m.word())
}

func (m *Message) Object() ObjectID {
	return ObjectID(m.word())
}

func (m *Message) blob() (payload []byte, start int, null bool) {
	start = m.off
	n := m.word()
	if m.err != nil {
		return nil, start, false
	}
	if n == 0 {
		return nil, start, true
	}
	if uint64(len(m.data)-m.off) < (uint64(n)+3)&^3 {
		m.fail(start, ErrTruncated)
		return nil, start, false
	}
	payload = m.data[m.off : m.off+int(n)]
	m.off += pad(int(n))
	return payload, start, false
}

func (m *Message) text(b []byte, start int) string {
	if b[len(b)-1] != 0 {
		m.fail(start, ErrUnterminatedString)
		return ""
	}
	if bytes.IndexByte(b[:len(b)-1], 0) >= 0 {
		m.fail(start, ErrEmbeddedNUL)
		return ""
	}
	return string(b[:len(b)-1])
}

func (m *Message) Str() string {
	b, start, null := m.blob()
	if null {
		m.fail(start, ErrNullString)
	}
	if m.err != nil {
		return ""
	}
	return m.text(b, start)
}

func (m *Message) OptStr() (string, bool) {
	b, start, null := m.blob()
	if null || m.err != nil {
		return "", false
	}
	s := m.text(b, start)
	return s, m.err == nil
}

func (m *Message) Array() []byte {
	b, _, _ := m.blob()
	if m.err != nil || len(b) == 0 {
		return nil
	}
	return bytes.Clone(b)
}

func (m *Message) FD() int {
	if m.err != nil {
		return -1
	}
	fd, ok := m.fds.peek(m.taken)
	if !ok {
		m.fail(m.off, ErrMissingFD)
		return -1
	}
	m.taken++
	return fd
}

func CloseUnclaimed(err error, fds ...int) error {
	errs := []error{err}
	for _, fd := range fds {
		if cerr := unix.Close(fd); cerr != nil {
			errs = append(errs, fmt.Errorf("wire: close an unclaimed fd %d: %w", fd, cerr))
		}
	}
	return errors.Join(errs...)
}

func (m *Message) Finish() error {
	m.open = false
	if m.err == nil && m.off != len(m.data) {
		m.fail(m.off, ErrTrailingBytes)
	}
	if m.err != nil {
		m.fds.closeFirst(m.taken)
		return m.err
	}
	m.fds.drop(m.taken)
	return nil
}
