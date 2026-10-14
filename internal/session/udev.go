package session

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
)

const (
	DefaultSeat      = "seat0"
	udevGroup        = 2
	udevMagic        = 0xfeedcafe
	udevPrefix       = "libudev\x00"
	udevHeaderLength = 40
	maxMessage       = 8192
)

type Subsystem string

const (
	DRM   Subsystem = "drm"
	Input Subsystem = "input"
)

type UdevDevice struct {
	Subsystem  Subsystem
	Syspath    string
	Devnode    string
	Device     Device
	Properties map[string]string
}

func (d UdevDevice) Seat() string {
	if s := d.Properties["ID_SEAT"]; s != "" {
		return s
	}
	return DefaultSeat
}

type Action string

const (
	Add      Action = "add"
	Remove   Action = "remove"
	Change   Action = "change"
	Overflow Action = "overflow"
)

type UdevEvent struct {
	Action Action
	UdevDevice
}

func parseProperties(lines [][]byte, sep byte, props map[string]string) {
	for _, line := range lines {
		k, v, ok := bytes.Cut(line, []byte{sep})
		if ok && len(k) > 0 {
			props[string(k)] = string(v)
		}
	}
}

func device(subsystem Subsystem, syspath string, props map[string]string) (UdevDevice, bool, error) {
	name, ok := props["DEVNAME"]
	if !ok {
		return UdevDevice{}, false, nil
	}
	major, errMaj := strconv.ParseUint(props["MAJOR"], 10, 32)
	minor, errMin := strconv.ParseUint(props["MINOR"], 10, 32)
	if err := errors.Join(errMaj, errMin); err != nil {
		return UdevDevice{}, false, fmt.Errorf("session: %s has device number %s:%s: %w", syspath, props["MAJOR"], props["MINOR"], err)
	}
	if !strings.HasPrefix(name, "/") {
		name = "/dev/" + name
	}
	if subsystem == Input && !strings.HasPrefix(name, "/dev/input/event") {
		return UdevDevice{}, false, nil
	}
	return UdevDevice{
		Subsystem: subsystem, Syspath: syspath, Devnode: name,
		Device: Device{Major: uint32(major), Minor: uint32(minor)}, Properties: props,
	}, true, nil
}

func udevDatabase(dbDir string, d Device, props map[string]string) error {
	f, err := os.Open(filepath.Join(dbDir, fmt.Sprintf("c%d:%d", d.Major, d.Minor))) //nolint:gosec // a path in the udev database named by a device number
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("session: read the udev database: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if prop, ok := bytes.CutPrefix(sc.Bytes(), []byte("E:")); ok {
			parseProperties([][]byte{prop}, '=', props)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("session: read the udev database for %d:%d: %w", d.Major, d.Minor, err)
	}
	return nil
}

type Roots struct {
	Sys, UdevData string
}

var SystemRoots = Roots{Sys: "/sys", UdevData: "/run/udev/data"}

func Enumerate(roots Roots, seat string, subsystems ...Subsystem) ([]UdevDevice, error) {
	var out []UdevDevice
	for _, sub := range subsystems {
		dir := filepath.Join(roots.Sys, "class", string(sub))
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("session: list %s: %w", dir, err)
		}
		for _, e := range entries {
			syspath, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("session: resolve %s: %w", e.Name(), err)
			}
			if info, err := os.Stat(syspath); err != nil || !info.IsDir() {
				if err != nil {
					return nil, fmt.Errorf("session: stat %s: %w", syspath, err)
				}
				continue
			}
			uevent, err := os.ReadFile(filepath.Join(syspath, "uevent"))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("session: read %s/uevent: %w", syspath, err)
			}
			props := map[string]string{}
			parseProperties(bytes.Split(uevent, []byte{'\n'}), '=', props)
			d, ok, err := device(sub, syspath, props)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if err := udevDatabase(roots.UdevData, d.Device, props); err != nil {
				return nil, err
			}
			if d.Seat() == seat {
				out = append(out, d)
			}
		}
	}
	slices.SortFunc(out, func(a, b UdevDevice) int { return strings.Compare(a.Devnode, b.Devnode) })
	return out, nil
}

var errNotUdev = errors.New("session: not a udev message")

func parseUdevMessage(msg []byte, sysRoot string) (UdevEvent, error) {
	if len(msg) < udevHeaderLength || string(msg[:len(udevPrefix)]) != udevPrefix || binary.BigEndian.Uint32(msg[8:]) != udevMagic {
		return UdevEvent{}, errNotUdev
	}
	off, n := binary.NativeEndian.Uint32(msg[16:]), binary.NativeEndian.Uint32(msg[20:])
	if uint64(off)+uint64(n) > uint64(len(msg)) {
		return UdevEvent{}, fmt.Errorf("session: udev properties at %d+%d past a %d byte message", off, n, len(msg))
	}
	props := map[string]string{}
	parseProperties(bytes.Split(msg[off:off+n], []byte{0}), '=', props)
	d, ok, err := device(Subsystem(props["SUBSYSTEM"]), filepath.Join(sysRoot, props["DEVPATH"]), props)
	if err != nil {
		return UdevEvent{}, err
	}
	if !ok {
		return UdevEvent{}, errNotUdev
	}
	return UdevEvent{Action: Action(props["ACTION"]), UdevDevice: d}, nil
}

type Monitor struct {
	fd       int
	src      *loop.Source
	seat     string
	sysRoot  string
	subs     []Subsystem
	trusted  func(from unix.Sockaddr, creds *unix.Ucred) bool
	handle   func(UdevEvent) error
	buf, oob []byte
}

func fromUdevd(from unix.Sockaddr, creds *unix.Ucred) bool {
	nl, ok := from.(*unix.SockaddrNetlink)
	return ok && nl.Groups == udevGroup && nl.Pid != 0 && creds != nil && creds.Uid == 0
}

func Watch(l *loop.Loop, seat string, subsystems []Subsystem, handle func(UdevEvent) error) (*Monitor, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return nil, fmt.Errorf("session: udev netlink socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: udevGroup}); err != nil {
		return nil, errors.Join(fmt.Errorf("session: bind the udev netlink group: %w", err), unix.Close(fd))
	}
	return watch(l, fd, seat, SystemRoots.Sys, subsystems, fromUdevd, handle)
}

func watch(l *loop.Loop, fd int, seat, sysRoot string, subs []Subsystem, trusted func(unix.Sockaddr, *unix.Ucred) bool, handle func(UdevEvent) error) (*Monitor, error) {
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return nil, errors.Join(fmt.Errorf("session: SO_PASSCRED: %w", err), unix.Close(fd))
	}
	m := &Monitor{fd: fd, seat: seat, sysRoot: sysRoot, subs: subs, trusted: trusted, handle: handle, buf: make([]byte, maxMessage), oob: make([]byte, unix.CmsgSpace(unix.SizeofUcred))}
	var err error
	if m.src, err = l.AddFD(fd, loop.Readable, m.read); err != nil {
		return nil, errors.Join(err, unix.Close(fd))
	}
	return m, nil
}

func credentials(oob []byte) *unix.Ucred {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	for _, m := range msgs {
		if creds, err := unix.ParseUnixCredentials(&m); err == nil {
			return creds
		}
	}
	return nil
}

func (m *Monitor) read(loop.Events) error {
	for {
		n, oobn, _, from, err := unix.Recvmsg(m.fd, m.buf, m.oob, 0)
		switch {
		case errors.Is(err, unix.EAGAIN):
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ENOBUFS):
			if err := m.handle(UdevEvent{Action: Overflow}); err != nil {
				return err
			}
			continue
		case err != nil:
			return fmt.Errorf("session: read udev events: %w", err)
		}
		if !m.trusted(from, credentials(m.oob[:oobn])) {
			continue
		}
		ev, err := parseUdevMessage(m.buf[:n], m.sysRoot)
		if errors.Is(err, errNotUdev) {
			continue
		}
		if err != nil {
			return err
		}
		if !slices.Contains(m.subs, ev.Subsystem) || ev.Seat() != m.seat {
			continue
		}
		if err := m.handle(ev); err != nil {
			return err
		}
	}
}

func (m *Monitor) Close() error {
	return errors.Join(m.src.Remove(), unix.Close(m.fd))
}
