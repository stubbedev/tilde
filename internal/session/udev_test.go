package session

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func TestEnumerateFiltersBySeatAndSkipsNodelessEntries(t *testing.T) {
	root := t.TempDir()
	roots := Roots{Sys: filepath.Join(root, "sys"), UdevData: filepath.Join(root, "udev")}
	devices := filepath.Join(roots.Sys, "devices/pci0000:00/0000:01:00.0/drm")
	write(t, filepath.Join(devices, "card0/uevent"), "MAJOR=226\nMINOR=0\nDEVNAME=dri/card0\nDEVTYPE=drm_minor\n")
	write(t, filepath.Join(devices, "card0-DP-1/uevent"), "DEVTYPE=drm_connector\n")
	write(t, filepath.Join(devices, "renderD128/uevent"), "MAJOR=226\nMINOR=128\nDEVNAME=dri/renderD128\n")
	link(t, filepath.Join(devices, "card0"), filepath.Join(roots.Sys, "class/drm/card0"))
	link(t, filepath.Join(devices, "card0-DP-1"), filepath.Join(roots.Sys, "class/drm/card0-DP-1"))
	link(t, filepath.Join(devices, "renderD128"), filepath.Join(roots.Sys, "class/drm/renderD128"))
	inputs := filepath.Join(roots.Sys, "devices/usb/input")
	write(t, filepath.Join(inputs, "event3/uevent"), "MAJOR=13\nMINOR=67\nDEVNAME=input/event3\n")
	write(t, filepath.Join(inputs, "event4/uevent"), "MAJOR=13\nMINOR=68\nDEVNAME=input/event4\n")
	write(t, filepath.Join(roots.Sys, "class/drm/version"), "drm 1.1.0 20060810\n")
	write(t, filepath.Join(inputs, "mice/uevent"), "MAJOR=13\nMINOR=63\nDEVNAME=input/mice\n")
	link(t, filepath.Join(inputs, "mice"), filepath.Join(roots.Sys, "class/input/mice"))
	link(t, filepath.Join(inputs, "event3"), filepath.Join(roots.Sys, "class/input/event3"))
	link(t, filepath.Join(inputs, "event4"), filepath.Join(roots.Sys, "class/input/event4"))
	write(t, filepath.Join(roots.UdevData, "c13:67"), "S:input/by-id/kbd\nE:ID_INPUT=1\nE:ID_INPUT_KEYBOARD=1\nG:seat\n")
	write(t, filepath.Join(roots.UdevData, "c13:68"), "E:ID_INPUT=1\nE:ID_SEAT=seat1\n")

	got, err := Enumerate(roots, DefaultSeat, DRM, Input)
	if err != nil {
		t.Fatal(err)
	}
	var nodes []string
	for _, d := range got {
		nodes = append(nodes, d.Devnode)
	}
	if !slices.Equal(nodes, []string{"/dev/dri/card0", "/dev/dri/renderD128", "/dev/input/event3"}) {
		t.Fatalf("devices %v", nodes)
	}
	kbd := got[2]
	if kbd.Subsystem != Input || kbd.Device != (Device{Major: 13, Minor: 67}) || kbd.Properties["ID_INPUT_KEYBOARD"] != "1" || kbd.Syspath != filepath.Join(inputs, "event3") {
		t.Fatalf("keyboard %+v", kbd)
	}
	seat1, err := Enumerate(roots, "seat1", Input)
	if err != nil || len(seat1) != 1 || seat1[0].Devnode != "/dev/input/event4" {
		t.Fatalf("seat1: %+v, %v", seat1, err)
	}
	write(t, filepath.Join(devices, "card1/uevent"), "MAJOR=226\nMINOR=x\nDEVNAME=dri/card1\n")
	link(t, filepath.Join(devices, "card1"), filepath.Join(roots.Sys, "class/drm/card1"))
	if _, err := Enumerate(roots, DefaultSeat, DRM); err == nil {
		t.Fatal("a bad device number was accepted")
	}
}

func udevMessage(props ...string) []byte {
	var body []byte
	for _, p := range props {
		body = append(append(body, p...), 0)
	}
	msg := make([]byte, udevHeaderLength, udevHeaderLength+len(body))
	copy(msg, udevPrefix)
	binary.BigEndian.PutUint32(msg[8:], udevMagic)
	binary.NativeEndian.PutUint32(msg[12:], udevHeaderLength)
	binary.NativeEndian.PutUint32(msg[16:], udevHeaderLength)
	binary.NativeEndian.PutUint32(msg[20:], uint32(len(body)))
	return append(msg, body...)
}

func TestParseUdevMessages(t *testing.T) {
	msg := udevMessage("ACTION=change", "DEVPATH=/devices/pci/drm/card0", "SUBSYSTEM=drm", "DEVNAME=/dev/dri/card0", "MAJOR=226", "MINOR=0", "HOTPLUG=1")
	ev, err := parseUdevMessage(msg, "/sys")
	if err != nil {
		t.Fatal(err)
	}
	if ev.Action != Change || ev.Subsystem != DRM || ev.Devnode != "/dev/dri/card0" || ev.Syspath != "/sys/devices/pci/drm/card0" || ev.Properties["HOTPLUG"] != "1" {
		t.Fatalf("event %+v", ev)
	}
	bad := slices.Clone(msg)
	bad[8] = 0
	if _, err := parseUdevMessage(bad, "/sys"); !errors.Is(err, errNotUdev) {
		t.Fatalf("a wrong magic: %v", err)
	}
	short := slices.Clone(msg)
	binary.NativeEndian.PutUint32(short[20:], 1<<20)
	if _, err := parseUdevMessage(short, "/sys"); err == nil || errors.Is(err, errNotUdev) {
		t.Fatalf("properties past the end: %v", err)
	}
	if _, err := parseUdevMessage(udevMessage("ACTION=add", "SUBSYSTEM=input", "DEVPATH=/x"), "/sys"); !errors.Is(err, errNotUdev) {
		t.Fatalf("a nodeless device: %v", err)
	}
}

func TestMonitorDeliversTrustedSeatEvents(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var got []UdevEvent
	m, err := watch(l, fds[0], DefaultSeat, "/sys", []Subsystem{DRM, Input}, func(_ unix.Sockaddr, c *unix.Ucred) bool {
		return c != nil && c.Uid == uint32(os.Getuid())
	}, func(ev UdevEvent) error {
		got = append(got, ev)
		if len(got) == 2 {
			l.Stop()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	creds := unix.UnixCredentials(&unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())})
	for _, msg := range [][]byte{
		udevMessage("ACTION=add", "SUBSYSTEM=sound", "DEVPATH=/s", "DEVNAME=snd/c", "MAJOR=116", "MINOR=1"),
		udevMessage("ACTION=add", "SUBSYSTEM=input", "DEVPATH=/i", "DEVNAME=input/event9", "MAJOR=13", "MINOR=73", "ID_SEAT=seat1"),
		[]byte("not udev"),
		udevMessage("ACTION=add", "SUBSYSTEM=input", "DEVPATH=/i", "DEVNAME=input/event8", "MAJOR=13", "MINOR=72"),
		udevMessage("ACTION=remove", "SUBSYSTEM=drm", "DEVPATH=/d", "DEVNAME=dri/card1", "MAJOR=226", "MINOR=1"),
	} {
		if err := unix.Sendmsg(fds[1], msg, creds, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Run(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Devnode != "/dev/input/event8" || got[0].Action != Add || got[1].Action != Remove || got[1].Subsystem != DRM {
		t.Fatalf("events %+v", got)
	}
}

func TestWatchBindsTheUdevGroup(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	m, err := Watch(l, DefaultSeat, []Subsystem{DRM}, func(UdevEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEnumerateWithoutAClassDirectory(t *testing.T) {
	sys := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sys, "class", string(Input)), 0o755); err != nil {
		t.Fatal(err)
	}
	devices, err := Enumerate(Roots{Sys: sys, UdevData: t.TempDir()}, DefaultSeat, DRM, Input)
	if err != nil || len(devices) != 0 {
		t.Fatalf("devices %v, err %v", devices, err)
	}
	if os.Geteuid() == 0 {
		return
	}
	class := filepath.Join(sys, "class", string(Input))
	if err := os.Chmod(class, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(class, 0o755); err != nil {
			t.Error(err)
		}
	})
	if _, err := Enumerate(Roots{Sys: sys, UdevData: t.TempDir()}, DefaultSeat, Input); err == nil {
		t.Fatal("an unreadable class directory was not an error")
	}
}

func TestEnumerateThisMachine(t *testing.T) {
	devices, err := Enumerate(SystemRoots, DefaultSeat, DRM, Input)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if !filepath.IsAbs(d.Devnode) || d.Device.Major == 0 {
			t.Errorf("device %+v", d)
		}
		t.Logf("%s %s %d:%d", d.Subsystem, d.Devnode, d.Device.Major, d.Device.Minor)
	}
}

func TestCardsReportBootVGAAndRenderNodes(t *testing.T) {
	root := t.TempDir()
	roots := Roots{Sys: filepath.Join(root, "sys"), UdevData: filepath.Join(root, "udev")}
	gpu := filepath.Join(roots.Sys, "devices/pci0000:00/0000:01:00.0")
	write(t, filepath.Join(gpu, "boot_vga"), "1\n")
	write(t, filepath.Join(gpu, "drm/card0/uevent"), "MAJOR=226\nMINOR=0\nDEVNAME=dri/card0\n")
	write(t, filepath.Join(gpu, "drm/renderD128/uevent"), "MAJOR=226\nMINOR=128\nDEVNAME=dri/renderD128\n")
	display := filepath.Join(roots.Sys, "devices/platform/vkms")
	write(t, filepath.Join(display, "drm/card1/uevent"), "MAJOR=226\nMINOR=1\nDEVNAME=dri/card1\n")
	link(t, filepath.Join(gpu, "drm/card0"), filepath.Join(roots.Sys, "class/drm/card0"))
	link(t, filepath.Join(gpu, "drm/renderD128"), filepath.Join(roots.Sys, "class/drm/renderD128"))
	link(t, filepath.Join(display, "drm/card1"), filepath.Join(roots.Sys, "class/drm/card1"))
	devices, err := Enumerate(roots, DefaultSeat, DRM)
	if err != nil {
		t.Fatal(err)
	}
	type facts struct{ boot, render bool }
	got := map[string]facts{}
	for _, d := range devices {
		boot, err := d.BootVGA()
		if err != nil {
			t.Fatal(err)
		}
		render, err := d.HasRenderNode()
		if err != nil {
			t.Fatal(err)
		}
		got[filepath.Base(d.Devnode)] = facts{boot, render}
	}
	if got["card0"] != (facts{true, true}) || got["card1"] != (facts{false, false}) {
		t.Fatalf("cards %+v", got)
	}
}
