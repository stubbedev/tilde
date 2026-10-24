package feedback

import (
	"encoding/binary"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/fourcc"
	"github.com/stubbedev/lys-wm/internal/gpu"
)

var (
	linear = gpu.FormatModifier{Format: fourcc.XRGB8888, Modifier: fourcc.Linear}
	tiled  = gpu.FormatModifier{Format: fourcc.XRGB8888, Modifier: 7}
)

func table(t *testing.T, size int, entries ...gpu.FormatModifier) (int, uint32) {
	t.Helper()
	fd, err := unix.MemfdCreate("feedback-test", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 0, size)
	for _, e := range entries {
		b = binary.NativeEndian.AppendUint32(b, uint32(e.Format))
		b = binary.NativeEndian.AppendUint32(b, 0)
		b = binary.NativeEndian.AppendUint64(b, uint64(e.Modifier))
	}
	b = b[:size]
	if _, err := unix.Write(fd, b); err != nil {
		t.Fatal(err)
	}
	return fd, uint32(size)
}

func indices(is ...uint16) []byte {
	var b []byte
	for _, i := range is {
		b = binary.NativeEndian.AppendUint16(b, i)
	}
	return b
}

func TestABatchCollectsTranchesFromTheTable(t *testing.T) {
	var got []Feedback
	l := NewListener(func(f Feedback) error {
		got = append(got, f)
		return nil
	})
	fd, size := table(t, 2*entrySize, linear, tiled)
	steps := []error{
		l.FormatTable(nil, fd, size),
		l.MainDevice(nil, binary.NativeEndian.AppendUint64(nil, 0xe280)),
		l.TrancheTargetDevice(nil, binary.NativeEndian.AppendUint64(nil, 0xe200)),
		l.TrancheFormats(nil, indices(1)),
		l.TrancheDone(nil),
		l.TrancheTargetDevice(nil, binary.NativeEndian.AppendUint64(nil, 0xe280)),
		l.TrancheFormats(nil, indices(0, 1)),
		l.TrancheDone(nil),
		l.Done(nil),
		l.TrancheTargetDevice(nil, binary.NativeEndian.AppendUint64(nil, 0xe280)),
		l.TrancheFormats(nil, indices(0)),
		l.TrancheDone(nil),
		l.Done(nil),
	}
	for i, err := range steps {
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	want := []Feedback{
		{Main: 0xe280, Tranches: []Tranche{{Target: 0xe200, Formats: []gpu.FormatModifier{tiled}}, {Target: 0xe280, Formats: []gpu.FormatModifier{linear, tiled}}}},
		{Main: 0xe280, Tranches: []Tranche{{Target: 0xe280, Formats: []gpu.FormatModifier{linear}}}},
	}
	if !slices.EqualFunc(got, want, func(a, b Feedback) bool {
		return a.Main == b.Main && slices.EqualFunc(a.Tranches, b.Tranches, func(x, y Tranche) bool {
			return x.Target == y.Target && x.Flags == y.Flags && slices.Equal(x.Formats, y.Formats)
		})
	}) {
		t.Fatalf("batches %+v, want %+v", got, want)
	}
}

func TestOnlyTheMainDevicesTranchesImport(t *testing.T) {
	f := Feedback{Main: 1, Tranches: []Tranche{
		{Target: 2, Formats: []gpu.FormatModifier{tiled}},
		{Target: 1, Formats: []gpu.FormatModifier{linear}},
	}}
	if !f.Imports(linear) || f.Imports(tiled) {
		t.Fatalf("imports linear %t and tiled %t, want only linear", f.Imports(linear), f.Imports(tiled))
	}
	if got := f.Modifiers(fourcc.XRGB8888); !slices.Equal(got, []fourcc.Modifier{fourcc.Linear}) {
		t.Fatalf("modifiers %v, want only linear", got)
	}
	if got := f.Modifiers(fourcc.ARGB8888); len(got) != 0 {
		t.Fatalf("modifiers of an unlisted format %v", got)
	}
}

func TestMalformedFeedbackFails(t *testing.T) {
	l := NewListener(func(Feedback) error { return nil })
	fd, size := table(t, entrySize+8, linear, tiled)
	if err := l.FormatTable(nil, fd, size); err == nil {
		t.Fatal("a table of partial entries was accepted")
	}
	fd, size = table(t, entrySize, linear)
	if err := l.FormatTable(nil, fd, size); err != nil {
		t.Fatal(err)
	}
	if err := l.TrancheFormats(nil, indices(1)); err == nil {
		t.Fatal("an index past the table was accepted")
	}
	if err := l.MainDevice(nil, []byte{1, 2, 3, 4}); err == nil {
		t.Fatal("a four-byte dev_t was accepted")
	}
}

func TestADeviceWithoutARenderNodeFails(t *testing.T) {
	if _, err := RenderNode(unix.Mkdev(1, 3)); err == nil {
		t.Fatal("/dev/null has a render node")
	}
}
