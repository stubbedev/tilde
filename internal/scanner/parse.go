package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"io"
	"strconv"
	"strings"

	"github.com/stubbedev/tilde/internal/wire"
)

type xmlProtocol struct {
	Name       string         `xml:"name,attr"`
	Interfaces []xmlInterface `xml:"interface"`
}

type xmlInterface struct {
	Name     string       `xml:"name,attr"`
	Version  string       `xml:"version,attr"`
	Requests []xmlMessage `xml:"request"`
	Events   []xmlMessage `xml:"event"`
	Enums    []xmlEnum    `xml:"enum"`
}

type xmlMessage struct {
	Name  string   `xml:"name,attr"`
	Type  string   `xml:"type,attr"`
	Since string   `xml:"since,attr"`
	Args  []xmlArg `xml:"arg"`
}

type xmlArg struct {
	Name      string `xml:"name,attr"`
	Type      string `xml:"type,attr"`
	Interface string `xml:"interface,attr"`
	AllowNull string `xml:"allow-null,attr"`
	Enum      string `xml:"enum,attr"`
}

type xmlEnum struct {
	Name     string     `xml:"name,attr"`
	Bitfield string     `xml:"bitfield,attr"`
	Entries  []xmlEntry `xml:"entry"`
}

type xmlEntry struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
	Since string `xml:"since,attr"`
}

type protocol struct {
	name       string
	pkg        string
	file       string
	interfaces []*iface
}

type iface struct {
	name     string
	goName   string
	version  uint32
	requests []*message
	events   []*message
	enums    []*enum
	proto    *protocol
}

type message struct {
	name       string
	goName     string
	opcode     int
	since      uint32
	destructor bool
	args       []*arg
}

type arg struct {
	name     string
	goName   string
	typ      wire.ArgType
	ifaceRef string
	iface    *iface
	nullable bool
	enumRef  string
	enum     *enum
}

type enum struct {
	name     string
	goName   string
	bitfield bool
	entries  []entry
	owner    *iface
}

type entry struct {
	goName string
	value  uint32
	since  uint32
}

var argTypeTable = []struct {
	xml      string
	typ      wire.ArgType
	constant string
}{
	{"int", wire.ArgInt, "ArgInt"},
	{"uint", wire.ArgUint, "ArgUint"},
	{"fixed", wire.ArgFixed, "ArgFixed"},
	{"string", wire.ArgString, "ArgString"},
	{"object", wire.ArgObject, "ArgObject"},
	{"new_id", wire.ArgNewID, "ArgNewID"},
	{"array", wire.ArgArray, "ArgArray"},
	{"fd", wire.ArgFD, "ArgFD"},
}

var argTypes, argConstants = func() (map[string]wire.ArgType, map[wire.ArgType]string) {
	byXML := map[string]wire.ArgType{}
	constants := map[wire.ArgType]string{}
	for _, t := range argTypeTable {
		byXML[t.xml] = t.typ
		constants[t.typ] = t.constant
	}
	return byXML, constants
}()

var reservedMethods = map[string]bool{
	"Base": true, "Dispatch": true, "Handler": true, "ProtocolError": true,
}

var reservedArgNames = map[string]bool{
	"obj": true, "msg": true, "enc": true, "err": true, "h": true, "wire": true, "child": true,
	"childID": true, "v": true, "ok": true,
}

var initialisms = map[string]bool{"id": true, "fd": true, "url": true, "uuid": true}

func camel(s string) string {
	var b strings.Builder
	for part := range strings.SplitSeq(s, "_") {
		switch {
		case part == "":
		case initialisms[part]:
			b.WriteString(strings.ToUpper(part))
		default:
			b.WriteString(strings.ToUpper(part[:1]))
			b.WriteString(part[1:])
		}
	}
	return b.String()
}

func lowerCamel(s string) string {
	first, rest, _ := strings.Cut(s, "_")
	return strings.ToLower(first) + camel(rest)
}

func argName(name string, imports map[string]bool) string {
	n := lowerCamel(name)
	if token.IsKeyword(n) || types.Universe.Lookup(n) != nil || reservedArgNames[n] || imports[n] {
		return n + "Arg"
	}
	return n
}

func parseVersion(s string, what string) (uint32, error) {
	if s == "" {
		return 1, nil
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("%s: invalid version %q", what, s)
	}
	return uint32(v), nil
}

func parseProtocol(r io.Reader, file, pkg string) (*protocol, error) {
	var x xmlProtocol
	if err := xml.NewDecoder(r).Decode(&x); err != nil {
		return nil, fmt.Errorf("%s: parse XML: %w", file, err)
	}
	if x.Name == "" {
		return nil, fmt.Errorf("%s: protocol has no name", file)
	}
	p := &protocol{name: x.Name, pkg: pkg, file: file}
	for _, xi := range x.Interfaces {
		i, err := parseInterface(xi, p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		p.interfaces = append(p.interfaces, i)
	}
	return p, nil
}

func parseInterface(x xmlInterface, p *protocol) (*iface, error) {
	if !validName(x.Name) {
		return nil, fmt.Errorf("invalid interface name %q", x.Name)
	}
	if x.Version == "" {
		return nil, fmt.Errorf("interface %s has no version", x.Name)
	}
	version, err := parseVersion(x.Version, x.Name)
	if err != nil {
		return nil, err
	}
	i := &iface{name: x.Name, goName: camel(x.Name), version: version, proto: p}
	for _, xe := range x.Enums {
		e, err := parseEnum(xe, i)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", x.Name, err)
		}
		i.enums = append(i.enums, e)
	}
	if i.requests, err = parseMessages(x.Requests, i, "request"); err != nil {
		return nil, err
	}
	if i.events, err = parseMessages(x.Events, i, "event"); err != nil {
		return nil, err
	}
	return i, nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func parseEnum(x xmlEnum, owner *iface) (*enum, error) {
	if !validName(x.Name) {
		return nil, fmt.Errorf("invalid enum name %q", x.Name)
	}
	e := &enum{name: x.Name, goName: owner.goName + camel(x.Name), bitfield: x.Bitfield == "true", owner: owner}
	seen := map[string]bool{}
	for _, xe := range x.Entries {
		if xe.Name == "" || strings.ContainsFunc(xe.Name, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
		}) {
			return nil, fmt.Errorf("enum %s: invalid entry name %q", x.Name, xe.Name)
		}
		v, err := strconv.ParseUint(xe.Value, 0, 32)
		if err != nil {
			return nil, fmt.Errorf("enum %s: entry %s: invalid value %q", x.Name, xe.Name, xe.Value)
		}
		since, err := parseVersion(xe.Since, fmt.Sprintf("enum %s: entry %s", x.Name, xe.Name))
		if err != nil {
			return nil, err
		}
		goName := e.goName + camel(xe.Name)
		if seen[goName] {
			return nil, fmt.Errorf("enum %s: entry %s duplicates another entry's Go name", x.Name, xe.Name)
		}
		seen[goName] = true
		e.entries = append(e.entries, entry{goName: goName, value: uint32(v), since: since})
	}
	return e, nil
}

func parseMessages(xs []xmlMessage, owner *iface, kind string) ([]*message, error) {
	var out []*message
	seen := map[string]bool{}
	for op, x := range xs {
		where := fmt.Sprintf("%s.%s", owner.name, x.Name)
		if !validName(x.Name) {
			return nil, fmt.Errorf("%s: invalid %s name %q", owner.name, kind, x.Name)
		}
		m := &message{name: x.Name, goName: camel(x.Name), opcode: op}
		if seen[m.goName] {
			return nil, fmt.Errorf("%s: %s name collides with another %s", where, kind, kind)
		}
		seen[m.goName] = true
		if kind == "request" && reservedMethods[m.goName] {
			return nil, fmt.Errorf("%s: request name collides with a generated method", where)
		}
		switch x.Type {
		case "":
		case "destructor":
			m.destructor = true
		default:
			return nil, fmt.Errorf("%s: unknown message type %q", where, x.Type)
		}
		var err error
		if m.since, err = parseVersion(x.Since, where); err != nil {
			return nil, err
		}
		if m.since > owner.version {
			return nil, fmt.Errorf("%s: since %d exceeds interface version %d", where, m.since, owner.version)
		}
		if m.args, err = parseArgs(x.Args, where, kind); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func parseArgs(xs []xmlArg, where, kind string) ([]*arg, error) {
	var out []*arg
	names := map[string]bool{}
	newIDs := 0
	for _, x := range xs {
		t, ok := argTypes[x.Type]
		if !ok {
			return nil, fmt.Errorf("%s: arg %q has unknown type %q", where, x.Name, x.Type)
		}
		if !validName(x.Name) {
			return nil, fmt.Errorf("%s: invalid arg name %q", where, x.Name)
		}
		a := &arg{name: x.Name, typ: t, ifaceRef: x.Interface, enumRef: x.Enum}
		switch x.AllowNull {
		case "", "false":
		case "true":
			if t != wire.ArgObject && t != wire.ArgString {
				return nil, fmt.Errorf("%s: arg %s of type %s cannot be nullable", where, x.Name, x.Type)
			}
			a.nullable = true
		default:
			return nil, fmt.Errorf("%s: arg %s: invalid allow-null %q", where, x.Name, x.AllowNull)
		}
		if x.Interface != "" && t != wire.ArgObject && t != wire.ArgNewID {
			return nil, fmt.Errorf("%s: arg %s of type %s cannot name an interface", where, x.Name, x.Type)
		}
		if x.Enum != "" && t != wire.ArgInt && t != wire.ArgUint {
			return nil, fmt.Errorf("%s: arg %s of type %s cannot name an enum", where, x.Name, x.Type)
		}
		if t == wire.ArgNewID {
			if x.Interface == "" && kind == "event" {
				return nil, fmt.Errorf("%s: event arg %s creates an object of unnamed interface", where, x.Name)
			}
			newIDs++
		}
		if names[x.Name] {
			return nil, fmt.Errorf("%s: arg %s listed twice", where, x.Name)
		}
		names[x.Name] = true
		out = append(out, a)
	}
	if newIDs > 1 {
		return nil, fmt.Errorf("%s: more than one new_id argument", where)
	}
	return out, nil
}

type index struct {
	interfaces map[string]*iface
}

func resolve(protocols []*protocol) error {
	idx := index{interfaces: map[string]*iface{}}
	for _, p := range protocols {
		for _, i := range p.interfaces {
			if prev, dup := idx.interfaces[i.name]; dup {
				return fmt.Errorf("interface %s defined in both %s and %s", i.name, prev.proto.file, p.file)
			}
			idx.interfaces[i.name] = i
		}
	}
	imports := map[string]bool{}
	for _, p := range protocols {
		imports[p.pkg] = true
	}
	var errs []error
	for _, p := range protocols {
		for _, i := range p.interfaces {
			for _, m := range append(append([]*message(nil), i.requests...), i.events...) {
				for _, a := range m.args {
					a.goName = argName(a.name, imports)
					if err := idx.resolveArg(a, i, m); err != nil {
						errs = append(errs, fmt.Errorf("%s: %s.%s: %w", p.file, i.name, m.name, err))
					}
				}
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return checkImportCycles(protocols)
}

func (idx index) resolveArg(a *arg, owner *iface, m *message) error {
	if a.ifaceRef != "" {
		target, ok := idx.interfaces[a.ifaceRef]
		if !ok {
			return fmt.Errorf("arg %s names unknown interface %s", a.name, a.ifaceRef)
		}
		a.iface = target
	}
	if a.typ == wire.ArgNewID && m.destructor {
		return fmt.Errorf("destructor creates object %s", a.name)
	}
	if a.enumRef == "" {
		return nil
	}
	ifaceName, enumName := owner.name, a.enumRef
	if dot := strings.IndexByte(a.enumRef, '.'); dot >= 0 {
		ifaceName, enumName = a.enumRef[:dot], a.enumRef[dot+1:]
	}
	target, ok := idx.interfaces[ifaceName]
	if !ok {
		return fmt.Errorf("arg %s names enum %s of unknown interface", a.name, a.enumRef)
	}
	for _, e := range target.enums {
		if e.name == enumName {
			a.enum = e
			return nil
		}
	}
	return fmt.Errorf("arg %s names unknown enum %s", a.name, a.enumRef)
}

func (p *protocol) dependencies() map[*protocol]bool {
	deps := map[*protocol]bool{}
	for _, i := range p.interfaces {
		for _, m := range append(append([]*message(nil), i.requests...), i.events...) {
			for _, a := range m.args {
				if a.iface != nil && a.iface.proto != p {
					deps[a.iface.proto] = true
				}
				if a.enum != nil && a.enum.owner.proto != p {
					deps[a.enum.owner.proto] = true
				}
			}
		}
	}
	return deps
}

func checkImportCycles(protocols []*protocol) error {
	const (
		_ = iota
		visiting
		done
	)
	state := map[*protocol]int{}
	var visit func(p *protocol, path []string) error
	visit = func(p *protocol, path []string) error {
		switch state[p] {
		case visiting:
			return fmt.Errorf("protocols import each other: %s", strings.Join(append(path, p.pkg), " -> "))
		case done:
			return nil
		}
		state[p] = visiting
		for _, d := range sortedProtocols(p.dependencies()) {
			if err := visit(d, append(path, p.pkg)); err != nil {
				return err
			}
		}
		state[p] = done
		return nil
	}
	for _, p := range protocols {
		if err := visit(p, nil); err != nil {
			return err
		}
	}
	return nil
}
