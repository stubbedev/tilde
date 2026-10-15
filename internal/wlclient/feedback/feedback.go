// Package feedback reads zwp_linux_dmabuf_feedback_v1 as a client: the
// device a compositor renders with and the format and modifier pairs each
// tranche of devices imports.
package feedback

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/fourcc"
	"github.com/stubbedev/tilde/internal/gpu"
	ld "github.com/stubbedev/tilde/internal/protocol/client/linuxdmabuf"
	"github.com/stubbedev/tilde/internal/wlclient"
)

const entrySize = 16

type Tranche struct {
	Target  uint64
	Formats []gpu.FormatModifier
	Flags   ld.ZwpLinuxDmabufFeedbackV1TrancheFlags
}

type Feedback struct {
	Main     uint64
	Tranches []Tranche
}

func (f Feedback) Imports(fm gpu.FormatModifier) bool {
	return slices.ContainsFunc(f.Tranches, func(t Tranche) bool {
		return t.Target == f.Main && slices.Contains(t.Formats, fm)
	})
}

type Listener struct {
	done    func(Feedback) error
	table   []gpu.FormatModifier
	next    Feedback
	current Tranche
}

func NewListener(done func(Feedback) error) *Listener {
	return &Listener{done: done}
}

func (l *Listener) Done(*ld.ZwpLinuxDmabufFeedbackV1) error {
	f := l.next
	l.next = Feedback{Main: f.Main}
	return l.done(f)
}

func (l *Listener) FormatTable(_ *ld.ZwpLinuxDmabufFeedbackV1, fd int, size uint32) error {
	if size%entrySize != 0 {
		return errors.Join(fmt.Errorf("feedback: a format table of %d bytes is not a whole number of %d-byte entries", size, entrySize), unix.Close(fd))
	}
	m, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		return errors.Join(fmt.Errorf("feedback: map a format table of %d bytes: %w", size, err), unix.Close(fd))
	}
	l.table = make([]gpu.FormatModifier, 0, len(m)/entrySize)
	for at := 0; at < len(m); at += entrySize {
		l.table = append(l.table, gpu.FormatModifier{
			Format:   fourcc.Format(binary.NativeEndian.Uint32(m[at:])),
			Modifier: fourcc.Modifier(binary.NativeEndian.Uint64(m[at+8:])),
		})
	}
	return errors.Join(unix.Munmap(m), unix.Close(fd))
}

func devT(b []byte) (uint64, error) {
	if len(b) != 8 {
		return 0, fmt.Errorf("feedback: a dev_t of %d bytes", len(b))
	}
	return binary.NativeEndian.Uint64(b), nil
}

func (l *Listener) MainDevice(_ *ld.ZwpLinuxDmabufFeedbackV1, b []byte) error {
	dev, err := devT(b)
	l.next.Main = dev
	return err
}

func (l *Listener) TrancheTargetDevice(_ *ld.ZwpLinuxDmabufFeedbackV1, b []byte) error {
	dev, err := devT(b)
	l.current.Target = dev
	return err
}

func (l *Listener) TrancheFormats(_ *ld.ZwpLinuxDmabufFeedbackV1, indices []byte) error {
	for at := 0; at+1 < len(indices); at += 2 {
		i := int(binary.NativeEndian.Uint16(indices[at:]))
		if i >= len(l.table) {
			return fmt.Errorf("feedback: format index %d is outside a table of %d entries", i, len(l.table))
		}
		l.current.Formats = append(l.current.Formats, l.table[i])
	}
	return nil
}

func (l *Listener) TrancheFlags(_ *ld.ZwpLinuxDmabufFeedbackV1, flags ld.ZwpLinuxDmabufFeedbackV1TrancheFlags) error {
	l.current.Flags = flags
	return nil
}

func (l *Listener) TrancheDone(*ld.ZwpLinuxDmabufFeedbackV1) error {
	l.next.Tranches = append(l.next.Tranches, l.current)
	l.current = Tranche{}
	return nil
}

func Default(d *wlclient.Display, linux *ld.ZwpLinuxDmabufV1) (Feedback, error) {
	var got *Feedback
	res, err := linux.GetDefaultFeedback(NewListener(func(f Feedback) error {
		got = &f
		return nil
	}))
	if err != nil {
		return Feedback{}, err
	}
	for got == nil {
		if err := d.Dispatch(); err != nil {
			return Feedback{}, errors.Join(fmt.Errorf("feedback: read the default dmabuf feedback: %w", err), res.Destroy())
		}
	}
	return *got, res.Destroy()
}

func RenderNode(dev uint64) (string, error) {
	dir := fmt.Sprintf("/sys/dev/char/%d:%d/device/drm", unix.Major(dev), unix.Minor(dev))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("feedback: find the render node of device %d:%d: %w", unix.Major(dev), unix.Minor(dev), err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "renderD") {
			return filepath.Join("/dev/dri", e.Name()), nil
		}
	}
	return "", fmt.Errorf("feedback: device %d:%d has no render node", unix.Major(dev), unix.Minor(dev))
}
