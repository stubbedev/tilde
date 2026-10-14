package xwayland

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/data"
	"github.com/stubbedev/tilde/internal/server"
)

const (
	incrChunk       = 64 << 10
	maxSelection    = 64 << 20
	transferTimeout = 30 * time.Second
	maxIncoming     = 32
)

func mimeOf(atom string) (string, bool) {
	switch {
	case atom == "UTF8_STRING":
		return "text/plain;charset=utf-8", true
	case atom == "TEXT":
		return "text/plain", true
	case strings.Contains(atom, "/"):
		return atom, true
	}
	return "", false
}

func atomOf(mime string) (string, bool) {
	switch {
	case mime == "text/plain;charset=utf-8":
		return "UTF8_STRING", true
	case mime == "text/plain":
		return "TEXT", true
	case strings.Contains(mime, "/"):
		return mime, true
	}
	return "", false
}

type selection struct {
	kind    data.Kind
	atom    xproto.Atom
	x       *xSource
	wayland data.Source
	targets map[xproto.Atom]string
	owned   bool
}

type xSource struct {
	m     *wm
	sel   *selection
	mimes []string
	atoms map[string]xproto.Atom
}

func (s *xSource) Client() *server.Client {
	return s.m.client
}

func (s *xSource) Mimes() []string {
	return s.mimes
}

func (s *xSource) Send(mime string, fd int) error {
	return s.m.receive(s, mime, fd)
}

func (s *xSource) Cancel() error {
	if s.sel.x == s {
		s.sel.x = nil
	}
	return nil
}

type incoming struct {
	src  *xSource
	atom xproto.Atom
	out  *os.File
	incr bool
	busy bool
}

type outKey struct {
	requestor xproto.Window
	property  xproto.Atom
}

type outgoing struct {
	target xproto.Atom
	data   []byte
	done   bool
}

func (m *wm) selectionOf(atom xproto.Atom) *selection {
	for _, s := range m.selections {
		if s.atom == atom {
			return s
		}
	}
	return nil
}

func (m *wm) selectionOwned(atom xproto.Atom, owner xproto.Window, at xproto.Timestamp) error {
	sel := m.selectionOf(atom)
	if sel == nil || owner == m.sel {
		return nil
	}
	if owner == 0 {
		if sel.x == nil {
			return nil
		}
		sel.x = nil
		return m.listener.SetSelection(sel.kind, nil)
	}
	sel.owned = false
	if m.focused == 0 {
		return nil
	}
	xproto.ConvertSelection(m.x, m.sel, sel.atom, m.atoms["TARGETS"], m.atoms["_WL_TARGETS"], at)
	return nil
}

func (m *wm) targetsOffered(atom xproto.Atom, offered map[string]xproto.Atom) error {
	xproto.DeleteProperty(m.x, m.sel, m.atoms["_WL_TARGETS"])
	sel := m.selectionOf(atom)
	if sel == nil || len(offered) == 0 {
		return nil
	}
	src := &xSource{m: m, sel: sel, mimes: slices.Sorted(maps.Keys(offered)), atoms: offered}
	sel.x = src
	return m.listener.SetSelection(sel.kind, src)
}

func pollable(fd int, name string) (*os.File, error) {
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, errors.Join(fmt.Errorf("make the %s fd non-blocking: %w", name, err), unix.Close(fd))
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (m *wm) receive(src *xSource, mime string, fd int) error {
	own, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("xwayland: keep the paste fd: %w", err)
	}
	out, err := pollable(own, "selection")
	if err != nil {
		return fmt.Errorf("xwayland: paste from the X selection: %w", err)
	}
	atom, ok := src.atoms[mime]
	if !ok || src.sel.x != src {
		return errors.Join(fmt.Errorf("xwayland: the X selection no longer offers %s", mime), out.Close())
	}
	if len(m.incoming) >= maxIncoming {
		return errors.Join(fmt.Errorf("xwayland: %d X selection transfers are already waiting; dropped %s", maxIncoming, mime), out.Close())
	}
	m.incoming = append(m.incoming, &incoming{src: src, atom: atom, out: out})
	if len(m.incoming) == 1 {
		m.convert()
	}
	return nil
}

func (m *wm) convert() {
	if len(m.incoming) == 0 {
		return
	}
	in := m.incoming[0]
	xproto.ConvertSelection(m.x, m.sel, in.src.sel.atom, in.atom, m.atoms["_WL_SELECTION"], xproto.TimeCurrentTime)
}

func (m *wm) finishIncoming(err error) error {
	in := m.incoming[0]
	m.incoming = m.incoming[1:]
	xproto.DeleteProperty(m.x, m.sel, m.atoms["_WL_SELECTION"])
	err = errors.Join(err, in.out.Close())
	m.convert()
	if err != nil {
		return fmt.Errorf("xwayland: paste from the X selection: %w", err)
	}
	return nil
}

func (m *wm) converted(target xproto.Atom, typ xproto.Atom, value []byte, ok bool) error {
	if len(m.incoming) == 0 || m.incoming[0].atom != target || m.incoming[0].incr {
		return nil
	}
	in := m.incoming[0]
	switch {
	case !ok:
		return m.finishIncoming(errors.New("the X owner refused the conversion"))
	case typ == m.atoms["INCR"]:
		in.incr = true
		xproto.DeleteProperty(m.x, m.sel, m.atoms["_WL_SELECTION"])
		return nil
	}
	return m.write(in, value, true)
}

func (m *wm) chunk(typ xproto.Atom, value []byte) error {
	if len(m.incoming) == 0 || !m.incoming[0].incr || typ == m.atoms["INCR"] || m.incoming[0].busy {
		return nil
	}
	in := m.incoming[0]
	if len(value) == 0 {
		return m.finishIncoming(nil)
	}
	return m.write(in, value, false)
}

func (m *wm) write(in *incoming, value []byte, last bool) error {
	in.busy = true
	out := in.out
	go func() {
		err := out.SetWriteDeadline(time.Now().Add(transferTimeout))
		if err == nil {
			_, err = out.Write(value)
		}
		m.post(func() error {
			if len(m.incoming) == 0 || m.incoming[0] != in {
				return nil
			}
			in.busy = false
			if err != nil || last {
				return m.finishIncoming(err)
			}
			xproto.DeleteProperty(m.x, m.sel, m.atoms["_WL_SELECTION"])
			return nil
		})
	}()
	return nil
}

func (m *wm) selectionChanged(k data.Kind, src data.Source) error {
	sel := m.selections[k]
	if _, ours := src.(*xSource); ours {
		return nil
	}
	sel.wayland, sel.targets = src, nil
	if sel.owned {
		sel.owned = false
		xproto.SetSelectionOwner(m.x, 0, sel.atom, xproto.TimeCurrentTime)
	}
	if src == nil {
		return nil
	}
	names := map[string]string{}
	for _, mime := range src.Mimes() {
		if name, ok := atomOf(mime); ok {
			names[name] = mime
		}
	}
	go func() {
		atoms, err := intern(m.x, slices.Sorted(maps.Keys(names)))
		m.post(func() error {
			if err != nil {
				return fmt.Errorf("xwayland: offer the Wayland selection to X: %w", err)
			}
			if sel.wayland != src {
				return nil
			}
			sel.targets = make(map[xproto.Atom]string, len(atoms))
			for name, a := range atoms {
				sel.targets[a] = names[name]
			}
			sel.owned = true
			xproto.SetSelectionOwner(m.x, m.sel, sel.atom, xproto.TimeCurrentTime)
			return nil
		})
	}()
	return nil
}

func (m *wm) notifySelection(e xproto.SelectionRequestEvent, property xproto.Atom) {
	ev := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: property}
	xproto.SendEvent(m.x, false, e.Requestor, xproto.EventMaskNoEvent, string(ev.Bytes()))
}

func (m *wm) requested(e xproto.SelectionRequestEvent) error {
	sel := m.selectionOf(e.Selection)
	property := cmp.Or(e.Property, e.Target)
	if sel == nil || sel.wayland == nil || !sel.owned {
		m.notifySelection(e, 0)
		return nil
	}
	if e.Target == m.atoms["TARGETS"] {
		list := append([]xproto.Atom{m.atoms["TARGETS"]}, slices.Sorted(maps.Keys(sel.targets))...)
		m.changeAtoms(e.Requestor, property, xproto.AtomAtom, list)
		m.notifySelection(e, property)
		return nil
	}
	mime, ok := sel.targets[e.Target]
	if !ok {
		m.notifySelection(e, 0)
		return nil
	}
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		m.notifySelection(e, 0)
		return fmt.Errorf("xwayland: pipe for a selection request: %w", err)
	}
	in, err := pollable(p[0], "selection")
	if err != nil {
		m.notifySelection(e, 0)
		return errors.Join(fmt.Errorf("xwayland: pipe for a selection request: %w", err), unix.Close(p[1]))
	}
	err = sel.wayland.Send(mime, p[1])
	if err = errors.Join(err, unix.Close(p[1])); err != nil {
		m.notifySelection(e, 0)
		return errors.Join(fmt.Errorf("xwayland: ask the Wayland selection for %s: %w", mime, err), in.Close())
	}
	go func() {
		value, err := readSelection(in)
		m.post(func() error { return m.sent(e, property, value, err) })
	}()
	return nil
}

func readSelection(in *os.File) ([]byte, error) {
	defer in.Close()
	if err := in.SetReadDeadline(time.Now().Add(transferTimeout)); err != nil {
		return nil, err
	}
	value, err := io.ReadAll(io.LimitReader(in, maxSelection+1))
	if err == nil && len(value) > maxSelection {
		err = fmt.Errorf("the selection is larger than %d bytes", maxSelection)
	}
	return value, err
}

func (m *wm) sent(e xproto.SelectionRequestEvent, property xproto.Atom, value []byte, err error) error {
	if err != nil {
		m.notifySelection(e, 0)
		return fmt.Errorf("xwayland: copy the Wayland selection to X: %w", err)
	}
	if len(value) <= incrChunk {
		xproto.ChangeProperty(m.x, xproto.PropModeReplace, e.Requestor, property, e.Target, 8, uint32(len(value)), value) //nolint:gosec // at most one chunk
		m.notifySelection(e, property)
		return nil
	}
	xproto.ChangeWindowAttributes(m.x, e.Requestor, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})
	size := binary.LittleEndian.AppendUint32(nil, uint32(len(value))) //nolint:gosec // bounded by maxSelection
	xproto.ChangeProperty(m.x, xproto.PropModeReplace, e.Requestor, property, m.atoms["INCR"], 32, 1, size)
	m.outgoing[outKey{e.Requestor, property}] = &outgoing{target: e.Target, data: value}
	m.notifySelection(e, property)
	return nil
}

func (m *wm) propertyDeleted(win xproto.Window, property xproto.Atom) error {
	key := outKey{win, property}
	o, ok := m.outgoing[key]
	if !ok {
		return nil
	}
	if o.done {
		delete(m.outgoing, key)
		return nil
	}
	n := min(incrChunk, len(o.data))
	xproto.ChangeProperty(m.x, xproto.PropModeReplace, win, property, o.target, 8, uint32(n), o.data[:n])
	o.data = o.data[n:]
	o.done = n == 0
	return nil
}
