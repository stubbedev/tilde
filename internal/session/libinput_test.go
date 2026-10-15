package session

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/loop"
	"github.com/stubbedev/tilde/internal/testenv"
)

type refusingOpener struct{ asked []string }

func (o *refusingOpener) TakeDevice(path string) (int, Device, error) {
	o.asked = append(o.asked, path)
	return -1, Device{}, unix.EPERM
}

func (o *refusingOpener) ReleaseDevice(Device) error { return nil }

func libinput(t *testing.T, opener Opener) *Libinput {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	path := LibinputPath(os.Getenv)
	in, err := OpenLibinput(l, path, opener, InputConfig{}, func(err error) { t.Errorf("reported: %v", err) })
	if err != nil {
		if _, statErr := os.Stat(path); statErr != nil {
			testenv.Require(t, "libinput")
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := in.Close(); err != nil {
			t.Error(err)
		}
	})
	return in
}

func evdevNode(t *testing.T) string {
	t.Helper()
	nodes, err := filepath.Glob("/dev/input/event*")
	if err != nil || len(nodes) == 0 {
		t.Skip("no evdev nodes here")
	}
	slices.Sort(nodes)
	return nodes[0]
}

func TestLibinputAsksTheOpenerForDevices(t *testing.T) {
	node := evdevNode(t)
	opener := &refusingOpener{}
	in := libinput(t, opener)
	err := in.Add(node)
	if err == nil || !errors.Is(err, unix.EPERM) || !strings.Contains(err.Error(), node) {
		t.Fatalf("a refused open: %v", err)
	}
	if !slices.Equal(opener.asked, []string{node}) {
		t.Fatalf("the opener was asked for %v", opener.asked)
	}
	in.Suspend()
	if err := in.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := in.Remove(node); err != nil {
		t.Fatal(err)
	}
}

func TestLibinputRejectsNonInputDevices(t *testing.T) {
	in := libinput(t, PlainOpener{})
	if err := in.Add(os.DevNull); err == nil {
		t.Fatal("/dev/null was added as an input device")
	}
	if _, err := OpenLibinput(nil, "/nonexistent/libinput.so.10", PlainOpener{}, InputConfig{}, nil); err == nil || !strings.Contains(err.Error(), LibinputEnv) {
		t.Fatalf("a missing library: %v", err)
	}
}

func TestPlainOpenerNamesTheDeviceItOpened(t *testing.T) {
	fd, d, err := PlainOpener{}.TakeDevice("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if d != (Device{Major: 1, Minor: 3}) {
		t.Fatalf("/dev/null is %+v", d)
	}
	if _, _, err := (PlainOpener{}).TakeDevice("/"); err == nil {
		t.Fatal("a directory was taken as a device")
	}
}
