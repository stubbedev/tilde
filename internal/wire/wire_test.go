package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"golang.org/x/sys/unix"
)

func pair(t testing.TB) (*Conn, *Conn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	a, b := NewConn(fds[0]), NewConn(fds[1])
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a, b
}

func send(t testing.TB, c *Conn, object ObjectID, opcode Opcode, encode func(*Encoder)) {
	t.Helper()
	e := c.Begin(object, opcode)
	encode(e)
	if err := e.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func receive(t testing.TB, c *Conn) *Message {
	t.Helper()
	if err := c.Fill(); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	m, err := c.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if m == nil {
		t.Fatal("Next: no complete message")
	}
	return m
}

func finish(t testing.TB, m *Message) {
	t.Helper()
	if err := m.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

func pipe(t testing.TB) int {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatalf("pipe2: %v", err)
	}
	t.Cleanup(func() {
		_ = unix.Close(p[0])
		_ = unix.Close(p[1])
	})
	return p[0]
}

func inode(t testing.TB, fd int) uint64 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatalf("fstat %d: %v", fd, err)
	}
	return st.Ino
}

func isOpen(fd int) bool {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	return err == nil
}

func rawWrite(t testing.TB, c *Conn, b []byte) {
	t.Helper()
	if _, err := unix.Write(c.FD(), b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func raw(object ObjectID, opcode Opcode, words ...[]byte) []byte {
	body := bytes.Join(words, nil)
	b := binary.NativeEndian.AppendUint32(nil, uint32(object))
	b = binary.NativeEndian.AppendUint32(b, uint32(headerSize+len(body))<<16|uint32(opcode))
	return append(b, body...)
}

func u32(v uint32) []byte {
	return binary.NativeEndian.AppendUint32(nil, v)
}

func TestRoundTripEveryArgumentType(t *testing.T) {
	a, b := pair(t)
	property := func(i int32, u uint32, f int32, o uint32, s, opt string, null bool, arr []byte, op uint16) bool {
		s, opt = strings.ReplaceAll(s, "\x00", ""), strings.ReplaceAll(opt, "\x00", "")
		send(t, a, ObjectID(o|1), Opcode(op), func(e *Encoder) {
			e.Int(i)
			e.Uint(u)
			e.Fixed(Fixed(f))
			e.Object(ObjectID(o))
			e.Str(s)
			if null {
				e.NullStr()
			} else {
				e.Str(opt)
			}
			e.Array(arr)
		})
		m := receive(t, b)
		gotI, gotU, gotF, gotO := m.Int(), m.Uint(), m.Fixed(), m.Object()
		gotS := m.Str()
		gotOpt, present := m.OptStr()
		gotArr := m.Array()
		finish(t, m)
		return m.ID == ObjectID(o|1) && m.Opcode == Opcode(op) &&
			gotI == i && gotU == u && gotF == Fixed(f) && gotO == ObjectID(o) &&
			gotS == s && present == !null && (null || gotOpt == opt) &&
			bytes.Equal(gotArr, arr)
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTripEmptyStringIsNotNull(t *testing.T) {
	a, b := pair(t)
	send(t, a, 1, 0, func(e *Encoder) { e.Str("") })
	m := receive(t, b)
	s, ok := m.OptStr()
	finish(t, m)
	if s != "" || !ok {
		t.Fatalf("OptStr = %q, %v; want \"\", true", s, ok)
	}
}

func TestRoundTripUntypedNewID(t *testing.T) {
	a, b := pair(t)
	property := func(iface string, version uint32, id uint32) bool {
		iface = strings.ReplaceAll(iface, "\x00", "")
		send(t, a, 2, 0, func(e *Encoder) {
			e.Uint(7)
			e.Str(iface)
			e.Uint(version)
			e.Object(ObjectID(id))
		})
		m := receive(t, b)
		name, gotIface, gotVersion, gotID := m.Uint(), m.Str(), m.Uint(), m.Object()
		finish(t, m)
		return name == 7 && gotIface == iface && gotVersion == version && gotID == ObjectID(id)
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTripFDsInArgumentOrder(t *testing.T) {
	a, b := pair(t)
	first, second := pipe(t), pipe(t)
	send(t, a, 3, 1, func(e *Encoder) {
		e.FD(first)
		e.Uint(9)
		e.FD(second)
	})
	if !isOpen(first) || !isOpen(second) {
		t.Fatal("sending closed the caller's fds")
	}
	m := receive(t, b)
	got1, u, got2 := m.FD(), m.Uint(), m.FD()
	finish(t, m)
	t.Cleanup(func() {
		_ = unix.Close(got1)
		_ = unix.Close(got2)
	})
	if u != 9 || inode(t, got1) != inode(t, first) || inode(t, got2) != inode(t, second) {
		t.Fatalf("fds arrived out of order or uint corrupted: %d", u)
	}
	flags, err := unix.FcntlInt(uintptr(got1), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("received fd is not close-on-exec: flags %#x, err %v", flags, err)
	}
}

func TestFixedFromFloat(t *testing.T) {
	cases := []struct {
		in   float64
		want Fixed
	}{
		{0, 0},
		{1, 256},
		{-1.5, -384},
		{1.0 / 512, 1},
		{-1.0 / 512, -1},
		{math.Inf(1), math.MaxInt32},
		{math.Inf(-1), math.MinInt32},
		{1e12, math.MaxInt32},
		{math.NaN(), 0},
	}
	for _, c := range cases {
		if got := FixedFromFloat(c.in); got != c.want {
			t.Errorf("FixedFromFloat(%v) = %d, want %d", c.in, got, c.want)
		}
	}
	property := func(v int32) bool {
		return FixedFromFloat(Fixed(v).Float()) == Fixed(v)
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func TestMillisWrapsLikeWaylandTimestamps(t *testing.T) {
	if got := Millis(1500 * time.Microsecond); got != 1 {
		t.Fatalf("Millis(1.5ms) = %d", got)
	}
	if got := Millis(time.Duration(math.MaxUint32+5) * time.Millisecond); got != 4 {
		t.Fatalf("Millis past 2^32 ms = %d, want the wrapped 4", got)
	}
}

func TestDecodeErrorsNameObjectOpcodeAndOffset(t *testing.T) {
	cases := []struct {
		name   string
		body   [][]byte
		decode func(*Message)
		want   error
		offset int
	}{
		{"truncated uint", nil, func(m *Message) { m.Uint() }, ErrTruncated, 8},
		{"truncated second arg", [][]byte{u32(1)}, func(m *Message) { m.Uint(); m.Int() }, ErrTruncated, 12},
		{"string longer than message", [][]byte{u32(100)}, func(m *Message) { m.Str() }, ErrTruncated, 8},
		{"string payload truncated", [][]byte{u32(5), []byte("abcd")}, func(m *Message) { m.Str() }, ErrTruncated, 8},
		{"array longer than message", [][]byte{u32(9), u32(0)}, func(m *Message) { m.Array() }, ErrTruncated, 8},
		{"null string", [][]byte{u32(0)}, func(m *Message) { m.Str() }, ErrNullString, 8},
		{"unterminated string", [][]byte{u32(4), []byte("abcd")}, func(m *Message) { m.Str() }, ErrUnterminatedString, 8},
		{"embedded NUL", [][]byte{u32(1), u32(4), []byte("a\x00b\x00")}, func(m *Message) { m.Uint(); m.OptStr() }, ErrEmbeddedNUL, 12},
		{"missing fd", [][]byte{u32(1)}, func(m *Message) { m.Uint(); m.FD() }, ErrMissingFD, 12},
		{"trailing bytes", [][]byte{u32(1), u32(2)}, func(m *Message) { m.Uint() }, ErrTrailingBytes, 12},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, b := pair(t)
			rawWrite(t, a, raw(42, 3, c.body...))
			m := receive(t, b)
			c.decode(m)
			err := m.Finish()
			var me *MessageError
			if !errors.As(err, &me) || !errors.Is(err, c.want) {
				t.Fatalf("Finish = %v, want MessageError wrapping %v", err, c.want)
			}
			if me.Object != 42 || me.Opcode != 3 || me.Offset != c.offset {
				t.Fatalf("error at object %d opcode %d offset %d, want 42 3 %d", me.Object, me.Opcode, me.Offset, c.offset)
			}
		})
	}
}

func TestNullOptStrIsNotAnError(t *testing.T) {
	a, b := pair(t)
	rawWrite(t, a, raw(1, 0, u32(0)))
	m := receive(t, b)
	s, ok := m.OptStr()
	finish(t, m)
	if s != "" || ok {
		t.Fatalf("OptStr = %q, %v; want \"\", false", s, ok)
	}
}

func TestInvalidHeaderSize(t *testing.T) {
	for _, size := range []uint32{0, 4, 10} {
		a, b := pair(t)
		hdr := append(u32(5), u32(size<<16|2)...)
		rawWrite(t, a, append(hdr, make([]byte, 8)...))
		if err := b.Fill(); err != nil {
			t.Fatalf("Fill: %v", err)
		}
		_, err := b.Next()
		var me *MessageError
		if !errors.As(err, &me) || !errors.Is(err, ErrMessageSize) || me.Object != 5 || me.Opcode != 2 || me.Offset != 4 {
			t.Fatalf("size %d: Next = %v, want ErrMessageSize at object 5 opcode 2 offset 4", size, err)
		}
	}
}

func TestFailedDecodeClosesTakenFDs(t *testing.T) {
	a, b := pair(t)
	send(t, a, 1, 0, func(e *Encoder) { e.FD(pipe(t)) })
	rawWrite(t, a, raw(1, 1))
	m := receive(t, b)
	fd := m.FD()
	m.Uint()
	if err := m.Finish(); !errors.Is(err, ErrTruncated) {
		t.Fatalf("Finish = %v, want ErrTruncated", err)
	}
	if isOpen(fd) {
		t.Fatal("fd taken by a failed message is still open")
	}
}

func TestEncodeErrorsRollBack(t *testing.T) {
	a, b := pair(t)
	cases := []struct {
		name   string
		encode func(*Encoder)
		want   error
	}{
		{"embedded NUL", func(e *Encoder) { e.Uint(1); e.Str("a\x00b") }, ErrEmbeddedNUL},
		{"too large", func(e *Encoder) { e.Array(make([]byte, maxMessageSize)) }, ErrMessageTooLarge},
		{"too many fds", func(e *Encoder) {
			fd := pipe(t)
			for range maxFDsOut + 1 {
				e.FD(fd)
			}
		}, ErrTooManyFDs},
		{"bad fd", func(e *Encoder) { e.FD(-1) }, unix.EBADF},
	}
	for _, c := range cases {
		e := a.Begin(9, 4)
		c.encode(e)
		var me *MessageError
		if err := e.End(); !errors.As(err, &me) || !errors.Is(err, c.want) || me.Object != 9 || me.Opcode != 4 {
			t.Fatalf("%s: End = %v, want MessageError for object 9 opcode 4 wrapping %v", c.name, err, c.want)
		}
		if len(a.out) != 0 || len(a.fdsOut) != 0 {
			t.Fatalf("%s: failed message left %d bytes and %d fds queued", c.name, len(a.out), len(a.fdsOut))
		}
	}
	send(t, a, 1, 0, func(e *Encoder) { e.Uint(5) })
	m := receive(t, b)
	if v := m.Uint(); v != 5 {
		t.Fatalf("message after rollback decoded %d, want 5", v)
	}
	finish(t, m)
}

func TestNothingIsSentBeforeFlush(t *testing.T) {
	a, b := pair(t)
	for i := range 100 {
		e := a.Begin(1, Opcode(i))
		e.Uint(uint32(i))
		if err := e.End(); err != nil {
			t.Fatalf("End: %v", err)
		}
	}
	if err := b.Fill(); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if m, err := b.Next(); m != nil || err != nil {
		t.Fatalf("peer saw a message before Flush: %v, %v", m, err)
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := b.Fill(); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	for i := range 100 {
		m, err := b.Next()
		if err != nil || m == nil {
			t.Fatalf("message %d: %v, %v", i, m, err)
		}
		if v := m.Uint(); m.Opcode != Opcode(i) || v != uint32(i) {
			t.Fatalf("message %d arrived as opcode %d value %d", i, m.Opcode, v)
		}
		finish(t, m)
	}
}

func TestMessagesStraddlingTheInputBuffer(t *testing.T) {
	a, b := pair(t)
	sizes := []int{maxMessageSize - 12, 100, maxMessageSize - 12, 3, maxMessageSize - 12, 0}
	go func() {
		for i, n := range sizes {
			e := a.Begin(1, Opcode(i))
			e.Array(bytes.Repeat([]byte{byte(i)}, n))
			if err := e.End(); err != nil {
				t.Errorf("End: %v", err)
				return
			}
			for {
				err := a.Flush()
				if err == nil {
					break
				}
				if !errors.Is(err, ErrWouldBlock) {
					t.Errorf("Flush: %v", err)
					return
				}
				var pfd [1]unix.PollFd
				pfd[0] = unix.PollFd{Fd: int32(a.FD()), Events: unix.POLLOUT}
				_, _ = unix.Poll(pfd[:], -1)
			}
		}
	}()
	for i := 0; i < len(sizes); {
		var pfd [1]unix.PollFd
		pfd[0] = unix.PollFd{Fd: int32(b.FD()), Events: unix.POLLIN}
		if n, err := unix.Poll(pfd[:], 5000); err != nil || n == 0 {
			t.Fatalf("poll: %d ready, %v", n, err)
		}
		if err := b.Fill(); err != nil {
			t.Fatalf("Fill: %v", err)
		}
		for {
			m, err := b.Next()
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if m == nil {
				break
			}
			got := m.Array()
			finish(t, m)
			if !bytes.Equal(got, bytes.Repeat([]byte{byte(i)}, sizes[i])) {
				t.Fatalf("message %d: %d bytes, corrupted", i, len(got))
			}
			i++
		}
	}
}

func TestEncoderFlushesWhenFDsFillAMessageBatch(t *testing.T) {
	a, b := pair(t)
	const n = 3*maxFDsOut + 5
	want := make([]uint64, n)
	for i := range n {
		fd := pipe(t)
		want[i] = inode(t, fd)
		e := a.Begin(1, 0)
		e.FD(fd)
		if err := e.End(); err != nil {
			t.Fatalf("End %d: %v", i, err)
		}
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := b.Fill(); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	for i := range n {
		m, err := b.Next()
		if err != nil || m == nil {
			t.Fatalf("message %d: %v, %v", i, m, err)
		}
		fd := m.FD()
		finish(t, m)
		if got := inode(t, fd); got != want[i] {
			t.Fatalf("message %d got the fd of another message", i)
		}
		_ = unix.Close(fd)
	}
}

func TestBackPressure(t *testing.T) {
	a, b := pair(t)
	payload := make([]byte, 4000)
	var full bool
	for range 1000 {
		e := a.Begin(1, 0)
		e.Array(payload)
		if err := e.End(); err != nil {
			if !errors.Is(err, ErrOutputFull) {
				t.Fatalf("End = %v, want ErrOutputFull", err)
			}
			full = true
			break
		}
	}
	if !full {
		t.Fatal("a peer that never reads did not fill the output buffer")
	}
	if !errors.Is(a.Err(), ErrOutputFull) {
		t.Fatalf("Err = %v, want the connection marked broken with ErrOutputFull", a.Err())
	}
	if err := a.Flush(); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("Flush = %v, want ErrWouldBlock", err)
	}
	for {
		if err := b.Fill(); err != nil {
			t.Fatalf("Fill: %v", err)
		}
		for {
			m, err := b.Next()
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if m == nil {
				break
			}
			m.Array()
			finish(t, m)
		}
		err := a.Flush()
		if err == nil {
			break
		}
		if !errors.Is(err, ErrWouldBlock) {
			t.Fatalf("Flush: %v", err)
		}
	}
}

func TestTracerSeesBothDirections(t *testing.T) {
	a, b := pair(t)
	type seen struct {
		incoming bool
		id       ObjectID
		opcode   Opcode
		body     string
	}
	var got []seen
	record := func(incoming bool, id ObjectID, opcode Opcode, body []byte) {
		got = append(got, seen{incoming, id, opcode, string(body)})
	}
	a.SetTracer(record)
	b.SetTracer(record)
	send(t, a, 4, 2, func(e *Encoder) { e.Uint(7) })
	m := receive(t, b)
	m.Uint()
	finish(t, m)
	want := []seen{{false, 4, 2, string(u32(7))}, {true, 4, 2, string(u32(7))}}
	if !slices.Equal(got, want) {
		t.Fatalf("traced %v, want %v", got, want)
	}
}

func TestAppendArgs(t *testing.T) {
	args := []Arg{
		{Name: "i", Type: ArgInt},
		{Name: "u", Type: ArgUint},
		{Name: "f", Type: ArgFixed},
		{Name: "s", Type: ArgString},
		{Name: "n", Type: ArgString, Nullable: true},
		{Name: "o", Type: ArgObject, Interface: "wl_output"},
		{Name: "z", Type: ArgObject, Interface: "wl_output", Nullable: true},
		{Name: "id", Type: ArgNewID, Interface: "wl_callback"},
		{Name: "any", Type: ArgNewID},
		{Name: "a", Type: ArgArray},
		{Name: "fd", Type: ArgFD},
	}
	body := bytes.Join([][]byte{
		u32(uint32(0xffffffff)), u32(5), u32(384), u32(3), []byte("hi\x00\x00"), u32(0), u32(9), u32(0), u32(10),
		u32(7), []byte("wl_seat\x00"), u32(2), u32(11), u32(3),
		{1, 2, 3, 0},
	}, nil)
	got := string(AppendArgs(nil, args, body))
	want := `-1, 5, 1.5, "hi", nil, wl_output@9, nil, new id wl_callback@10, new id wl_seat@11, array[3], fd`
	if got != want {
		t.Fatalf("AppendArgs =\n%s\nwant\n%s", got, want)
	}
	if got := string(AppendArgs(nil, args[:2], u32(1))); got != "1, <truncated>" {
		t.Fatalf("truncated body formatted as %q", got)
	}
}

func TestTooManyQueuedFDs(t *testing.T) {
	a, b := pair(t)
	fd := pipe(t)
	for range maxFDsIn/maxFDsOut + 1 {
		e := a.Begin(1, 0)
		for range maxFDsOut {
			e.FD(fd)
		}
		if err := e.End(); err != nil {
			t.Fatalf("End: %v", err)
		}
		if err := a.Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
	}
	if err := b.Fill(); !errors.Is(err, ErrTooManyFDs) {
		t.Fatalf("Fill = %v, want ErrTooManyFDs", err)
	}
}

func TestFillReportsEOF(t *testing.T) {
	a, b := pair(t)
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Fill(); !errors.Is(err, io.EOF) {
		t.Fatalf("Fill = %v, want io.EOF", err)
	}
}

func TestCloseReleasesQueuedFDs(t *testing.T) {
	a, b := pair(t)
	fd := pipe(t)
	e := a.Begin(1, 0)
	e.FD(fd)
	if err := e.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	queued := a.fdsOut[0]
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if isOpen(queued) {
		t.Fatal("sent fd copy is still open")
	}
	if err := b.Fill(); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	received, _ := b.fdsIn.peek(0)
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if isOpen(received) {
		t.Fatal("Close left a received fd open")
	}
}

func TestMisuseFailsLoudly(t *testing.T) {
	a, b := pair(t)
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		f()
	}
	e := a.Begin(1, 0)
	mustPanic("Begin before End", func() { a.Begin(1, 0) })
	if err := e.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	send(t, a, 1, 0, func(e *Encoder) { e.Uint(1) })
	receive(t, b)
	mustPanic("Next before Finish", func() { _, _ = b.Next() })
	mustPanic("Fill before Finish", func() { _ = b.Fill() })
}

func steadyStateRoundTrip(t testing.TB, a, b *Conn, fd int, title string) {
	e := a.Begin(7, 2)
	e.Int(-1)
	e.Uint(2)
	e.Fixed(3)
	e.Object(4)
	e.FD(fd)
	if title != "" {
		e.Str(title)
	}
	if err := e.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := b.Fill(); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	m, err := b.Next()
	if err != nil || m == nil {
		t.Fatalf("Next: %v, %v", m, err)
	}
	m.Int()
	m.Uint()
	m.Fixed()
	m.Object()
	got := m.FD()
	if title != "" {
		m.Str()
	}
	if err := m.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	_ = unix.Close(got)
}

func TestSteadyStateAllocations(t *testing.T) {
	a, b := pair(t)
	fd := pipe(t)
	if allocs := testing.AllocsPerRun(1000, func() { steadyStateRoundTrip(t, a, b, fd, "") }); allocs != 0 {
		t.Errorf("round trip of fixed-size arguments and an fd allocates %v times, want 0", allocs)
	}
	if allocs := testing.AllocsPerRun(1000, func() { steadyStateRoundTrip(t, a, b, fd, "title") }); allocs != 1 {
		t.Errorf("round trip with a string allocates %v times, want 1 for the decoded string", allocs)
	}
}

func BenchmarkRoundTrip(b *testing.B) {
	x, y := pair(b)
	fd := pipe(b)
	b.ReportAllocs()
	for b.Loop() {
		steadyStateRoundTrip(b, x, y, fd, "")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte{0, 1, 4}, raw(1, 2, u32(1), u32(6), []byte("hello\x00\x00\x00")))
	f.Add([]byte{5, 6, 7}, raw(3, 4, u32(0), u32(2), []byte("a\x00\x00\x00"), u32(1)))
	f.Add([]byte{}, raw(1, 0))
	f.Fuzz(func(t *testing.T, script, data []byte) {
		if len(data) < headerSize {
			return
		}
		id, opcode, size, err := parseHeader(data)
		if err != nil {
			return
		}
		if size > len(data) {
			return
		}
		fds := newFDQueue(4)
		m := Message{ID: id, Opcode: opcode, data: data[:size], off: headerSize, fds: &fds, open: true}
		for _, op := range script {
			switch op % 8 {
			case 0:
				m.Int()
			case 1:
				m.Uint()
			case 2:
				m.Fixed()
			case 3:
				m.Object()
			case 4:
				m.Str()
			case 5:
				m.OptStr()
			case 6:
				m.Array()
			case 7:
				m.FD()
			}
		}
		if err := m.Finish(); err == nil && m.off != size {
			t.Fatalf("Finish succeeded with %d of %d bytes consumed", m.off, size)
		}
	})
}
