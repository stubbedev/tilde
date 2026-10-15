package wire

import (
	"errors"
	"fmt"
)

type ArgType uint8

const (
	ArgInt ArgType = iota + 1
	ArgUint
	ArgFixed
	ArgString
	ArgObject
	ArgNewID
	ArgArray
	ArgFD
)

type Arg struct {
	Name      string
	Type      ArgType
	Interface string
	Nullable  bool
}

type MessageSpec struct {
	Name       string
	Since      uint32
	Destructor bool
	Args       []Arg
}

type Interface struct {
	Name     string
	Version  uint32
	Requests []MessageSpec
	Events   []MessageSpec
}

type Resource interface {
	Base() *Object
	Dispatch(m *Message) error
}

type Peer interface {
	Conn() *Conn
	Get(id ObjectID) (Resource, bool)
	Add(r Resource) error
	Remove(id ObjectID) error
	NewID() (ObjectID, error)
}

var (
	ErrInvalidOpcode  = errors.New("invalid opcode")
	ErrVersion        = errors.New("not supported by the bound version")
	ErrUnknownObject  = errors.New("unknown object")
	ErrWrongInterface = errors.New("object has the wrong interface")
	ErrNullObject     = errors.New("null object for a non-nullable argument")
	ErrInvalidNewID   = errors.New("invalid new object id")
	ErrNoHandler      = errors.New("no handler for a new object")
)

type ProtocolError struct {
	Object  ObjectID
	Code    uint32
	Message string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("protocol error on object %d, code %d: %s", e.Object, e.Code, e.Message)
}

type Object struct {
	peer    Peer
	id      ObjectID
	version uint32
	iface   *Interface
	removed bool
}

func NewObject(p Peer, id ObjectID, version uint32, iface *Interface) (Object, error) {
	if version == 0 {
		return Object{}, fmt.Errorf("wire: %s@%d: version 0: %w", iface.Name, id, ErrVersion)
	}
	return Object{peer: p, id: id, version: version, iface: iface}, nil
}

func CheckVersion(iface *Interface, version uint32) error {
	if version == 0 || version > iface.Version {
		return fmt.Errorf("wire: %s version %d outside 1..%d: %w", iface.Name, version, iface.Version, ErrVersion)
	}
	return nil
}

func NoHandler(iface *Interface) error {
	return fmt.Errorf("wire: new %s: %w", iface.Name, ErrNoHandler)
}

func (o *Object) ID() ObjectID {
	return o.id
}

func (o *Object) Version() uint32 {
	return o.version
}

func (o *Object) Peer() Peer {
	return o.peer
}

func (o *Object) Interface() *Interface {
	return o.iface
}

func (o *Object) Begin(opcode Opcode) *Encoder {
	e := o.peer.Conn().Begin(o.id, opcode)
	e.discard = o.removed
	return e
}

func (o *Object) MarkRemoved() {
	o.removed = true
}

func (o *Object) Removed() bool {
	return o.removed
}

func (o *Object) Require(message string, since uint32) error {
	if o.version >= since {
		return nil
	}
	return fmt.Errorf("wire: %s@%d.%s needs version %d, bound at %d: %w", o.iface.Name, o.id, message, since, o.version, ErrVersion)
}

func (o *Object) Expect(m *Message, specs []MessageSpec) int {
	op := int(m.Opcode)
	switch {
	case op >= len(specs):
		m.fail(4, fmt.Errorf("%w: %s has %d", ErrInvalidOpcode, o.iface.Name, len(specs)))
		return -1
	case specs[op].Since > o.version:
		m.fail(4, fmt.Errorf("%s.%s needs version %d, bound at %d: %w", o.iface.Name, specs[op].Name, specs[op].Since, o.version, ErrVersion))
		return -1
	}
	return op
}

func (o *Object) NullArg(message, arg string) error {
	return fmt.Errorf("wire: %s@%d.%s: argument %s: %w", o.iface.Name, o.id, message, arg, ErrNullObject)
}

func (o *Object) Owns(r Resource) error {
	if r.Base().peer != o.peer {
		return fmt.Errorf("wire: %s@%d belongs to another peer than %s@%d", r.Base().iface.Name, r.Base().id, o.iface.Name, o.id)
	}
	return nil
}

func (m *Message) NewID() ObjectID {
	off := m.off
	id := m.Object()
	if m.err == nil && id == 0 {
		m.fail(off, ErrNullObject)
	}
	return id
}

func ObjectArg[T Resource](m *Message, p Peer, nullable bool) T {
	var zero T
	off := m.off
	id := m.Object()
	if m.err != nil {
		return zero
	}
	if id == 0 {
		if !nullable {
			m.fail(off, ErrNullObject)
		}
		return zero
	}
	r, ok := p.Get(id)
	if !ok {
		m.fail(off, fmt.Errorf("%w: %d", ErrUnknownObject, id))
		return zero
	}
	if r == nil {
		return zero
	}
	t, ok := r.(T)
	if !ok {
		m.fail(off, fmt.Errorf("%w: %d is %s", ErrWrongInterface, id, r.Base().iface.Name))
		return zero
	}
	return t
}
