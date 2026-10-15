// Package wire encodes and decodes Wayland protocol messages on a Unix socket.
package wire

import (
	"errors"
	"fmt"
	"math"
	"time"
)

type ObjectID uint32

const (
	FirstServerID ObjectID = 0xff000000
	LastServerID  ObjectID = 0xffffffff
)

func (id ObjectID) ServerSide() bool {
	return id >= FirstServerID
}

type Opcode uint16

type Fixed int32

func FixedFromFloat(f float64) Fixed {
	v := math.Round(f * 256)
	switch {
	case math.IsNaN(v):
		return 0
	case v >= math.MaxInt32:
		return math.MaxInt32
	case v <= math.MinInt32:
		return math.MinInt32
	}
	return Fixed(v)
}

func (f Fixed) Float() float64 {
	return float64(f) / 256
}

const (
	headerSize     = 8
	maxMessageSize = math.MaxUint16 &^ 3
	bufferSize     = 1 << 16
	maxFDsOut      = 28
	maxFDsIn       = 1024
	maxFDsPerRecv  = 253
)

var (
	ErrTruncated          = errors.New("argument runs past the end of the message")
	ErrNullString         = errors.New("null string for a non-nullable argument")
	ErrUnterminatedString = errors.New("string is not NUL-terminated")
	ErrEmbeddedNUL        = errors.New("string contains a NUL byte")
	ErrMissingFD          = errors.New("file descriptor expected but none received")
	ErrTrailingBytes      = errors.New("bytes left after the last argument")
	ErrMessageSize        = errors.New("invalid message size")
	ErrMessageTooLarge    = errors.New("message exceeds the maximum size")
	ErrTooManyFDs         = errors.New("too many file descriptors")
	ErrOutputFull         = errors.New("output buffer full: the peer is not reading")
	ErrWouldBlock         = errors.New("socket buffer full")
)

type MessageError struct {
	Object ObjectID
	Opcode Opcode
	Offset int
	Err    error
}

func (e *MessageError) Error() string {
	return fmt.Sprintf("wire: object %d opcode %d offset %d: %v", e.Object, e.Opcode, e.Offset, e.Err)
}

func (e *MessageError) Unwrap() error {
	return e.Err
}

func Millis(t time.Duration) uint32 {
	return uint32(t.Milliseconds())
}

func Halves(v uint64) (hi, lo uint32) {
	return uint32(v >> 32), uint32(v)
}

func Join(hi, lo uint32) uint64 {
	return uint64(hi)<<32 | uint64(lo)
}

type Timespec struct{ SecHi, SecLo, Nsec uint32 }

func TimespecOf(t time.Duration) Timespec {
	t = max(t, 0)
	hi, lo := Halves(uint64(t / time.Second))
	return Timespec{SecHi: hi, SecLo: lo, Nsec: uint32(t % time.Second)}
}

func pad(n int) int {
	return (n + 3) &^ 3
}
