package session

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type PlainOpener struct{}

func (PlainOpener) TakeDevice(path string) (int, Device, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, Device{}, fmt.Errorf("session: open %s: %w", path, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return -1, Device{}, errors.Join(fmt.Errorf("session: stat %s: %w", path, err), unix.Close(fd))
	}
	d, err := deviceOf(&st, path)
	if err != nil {
		return -1, Device{}, errors.Join(err, unix.Close(fd))
	}
	return fd, d, nil
}

func (PlainOpener) ReleaseDevice(Device) error {
	return nil
}
