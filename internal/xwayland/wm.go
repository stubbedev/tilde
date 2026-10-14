package xwayland

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

var quietXGB sync.Once

var wmAtoms = []string{"WM_S0", "_NET_WM_CM_S0", "_NET_SUPPORTING_WM_CHECK", "_NET_WM_NAME", "UTF8_STRING"}

func intern(x *xgb.Conn, names []string) (map[string]xproto.Atom, error) {
	cookies := make([]xproto.InternAtomCookie, len(names))
	for i, name := range names {
		cookies[i] = xproto.InternAtom(x, false, uint16(len(name)), name) //nolint:gosec // atom names are short constants
	}
	atoms := make(map[string]xproto.Atom, len(names))
	for i, c := range cookies {
		reply, err := c.Reply()
		if err != nil {
			return nil, fmt.Errorf("intern %s: %w", names[i], err)
		}
		atoms[names[i]] = reply.Atom
	}
	return atoms, nil
}

func claim(conn net.Conn) (*xgb.Conn, error) {
	quietXGB.Do(func() { xgb.Logger = log.New(io.Discard, "", 0) })
	x, err := xgb.NewConnNet(conn)
	if err != nil {
		return nil, fmt.Errorf("connect as the window manager: %w", err)
	}
	if err := own(x); err != nil {
		x.Close()
		return nil, err
	}
	return x, nil
}

func own(x *xgb.Conn) error {
	screen := xproto.Setup(x).DefaultScreen(x)
	atoms, err := intern(x, wmAtoms)
	if err != nil {
		return err
	}
	win, err := xproto.NewWindowId(x)
	if err != nil {
		return fmt.Errorf("allocate the window manager's window: %w", err)
	}
	id := binary.LittleEndian.AppendUint32(nil, uint32(win))
	const name = "tilde"
	checks := []interface{ Check() error }{
		xproto.CreateWindowChecked(x, 0, win, screen.Root, -100, -100, 1, 1, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil),
		xproto.ChangePropertyChecked(x, xproto.PropModeReplace, screen.Root, atoms["_NET_SUPPORTING_WM_CHECK"], xproto.AtomWindow, 32, 1, id),
		xproto.ChangePropertyChecked(x, xproto.PropModeReplace, win, atoms["_NET_SUPPORTING_WM_CHECK"], xproto.AtomWindow, 32, 1, id),
		xproto.ChangePropertyChecked(x, xproto.PropModeReplace, win, atoms["_NET_WM_NAME"], atoms["UTF8_STRING"], 8, uint32(len(name)), []byte(name)),
		xproto.SetSelectionOwnerChecked(x, win, atoms["WM_S0"], xproto.TimeCurrentTime),
		xproto.SetSelectionOwnerChecked(x, win, atoms["_NET_WM_CM_S0"], xproto.TimeCurrentTime),
	}
	var errs []error
	for _, c := range checks {
		errs = append(errs, c.Check())
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("take the WM_S0 selection: %w", err)
	}
	return nil
}
