package harness

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	cim "github.com/stubbedev/lys-wm/internal/protocol/client/inputmethod"
	ctix "github.com/stubbedev/lys-wm/internal/protocol/client/textinput"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type textInputLog struct{ log testclient.Log }

func (l *textInputLog) Enter(*ctix.ZwpTextInputV3, *cw.WlSurface) error { return l.log.Note("enter") }

func (l *textInputLog) Leave(*ctix.ZwpTextInputV3, *cw.WlSurface) error { return l.log.Note("leave") }

func (l *textInputLog) PreeditString(_ *ctix.ZwpTextInputV3, text *string, begin, end int32) error {
	return l.log.Note("preedit %q %d %d", *text, begin, end)
}

func (l *textInputLog) CommitString(_ *ctix.ZwpTextInputV3, text *string) error {
	return l.log.Note("commit %q", *text)
}

func (l *textInputLog) DeleteSurroundingText(_ *ctix.ZwpTextInputV3, before, after uint32) error {
	return l.log.Note("delete %d %d", before, after)
}

func (l *textInputLog) Done(_ *ctix.ZwpTextInputV3, serial uint32) error {
	return l.log.Note("done %d", serial)
}

func (l *textInputLog) Action(*ctix.ZwpTextInputV3, ctix.ZwpTextInputV3Action, uint32) error {
	return l.log.Note("action")
}

func (l *textInputLog) Language(*ctix.ZwpTextInputV3, string) error { return l.log.Note("language") }

func (l *textInputLog) PreeditHint(*ctix.ZwpTextInputV3, uint32, uint32, ctix.ZwpTextInputV3PreeditHint) error {
	return l.log.Note("preedit_hint")
}

type inputMethodLog struct {
	log   testclient.Log
	dones uint32
}

func (l *inputMethodLog) Activate(*cim.ZwpInputMethodV2) error { return l.log.Note("activate") }

func (l *inputMethodLog) Deactivate(*cim.ZwpInputMethodV2) error { return l.log.Note("deactivate") }

func (l *inputMethodLog) SurroundingText(_ *cim.ZwpInputMethodV2, text string, cursor, anchor uint32) error {
	return l.log.Note("surrounding %q %d %d", text, cursor, anchor)
}

func (l *inputMethodLog) TextChangeCause(_ *cim.ZwpInputMethodV2, cause ctix.ZwpTextInputV3ChangeCause) error {
	return l.log.Note("cause %d", cause)
}

func (l *inputMethodLog) ContentType(_ *cim.ZwpInputMethodV2, hint ctix.ZwpTextInputV3ContentHint, purpose ctix.ZwpTextInputV3ContentPurpose) error {
	return l.log.Note("content %d %d", hint, purpose)
}

func (l *inputMethodLog) Done(*cim.ZwpInputMethodV2) error {
	l.dones++
	return l.log.Note("done")
}

func (l *inputMethodLog) Unavailable(*cim.ZwpInputMethodV2) error { return l.log.Note("unavailable") }

type grabLog struct {
	log    testclient.Log
	keymap uint32
	repeat [2]int32
}

func (g *grabLog) Keymap(_ *cim.ZwpInputMethodKeyboardGrabV2, _ cw.WlKeyboardKeymapFormat, fd int, size uint32) error {
	g.keymap = size
	return unix.Close(fd)
}

func (g *grabLog) Key(_ *cim.ZwpInputMethodKeyboardGrabV2, _, _, key uint32, state cw.WlKeyboardKeyState) error {
	return g.log.Note("key %d %d", key, state)
}

func (g *grabLog) Modifiers(*cim.ZwpInputMethodKeyboardGrabV2, uint32, uint32, uint32, uint32, uint32) error {
	return nil
}

func (g *grabLog) RepeatInfo(_ *cim.ZwpInputMethodKeyboardGrabV2, rate, delay int32) error {
	g.repeat = [2]int32{rate, delay}
	return nil
}

type rectangleLog struct{ rects []image.Rectangle }

func (r *rectangleLog) TextInputRectangle(_ *cim.ZwpInputPopupSurfaceV2, x, y, w, h int32) error {
	r.rects = append(r.rects, image.Rect(int(x), int(y), int(x+w), int(y+h)))
	return nil
}

type typist struct {
	*party
	ti  *ctix.ZwpTextInputV3
	log *textInputLog
}

func typing(t *testing.T, h *Harness, name string) *typist {
	t.Helper()
	p := join(t, h, name)
	m, err := ctix.NewZwpTextInputManagerV3(p.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.c.Bind(m)
	log := &textInputLog{}
	ti, err := m.GetTextInput(log, p.seat)
	if err != nil {
		t.Fatal(err)
	}
	return &typist{party: p, ti: ti, log: log}
}

type method struct {
	c   *testclient.Client
	in  *testclient.Input
	im  *cim.ZwpInputMethodV2
	log *inputMethodLog
}

func inputMethod(t *testing.T, h *Harness) *method {
	t.Helper()
	c := testclient.Connect(t, h.Socket())
	seat, in := c.Seat()
	m, err := cim.NewZwpInputMethodManagerV2(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	log := &inputMethodLog{}
	im, err := m.GetInputMethod(seat, log)
	if err != nil {
		t.Fatal(err)
	}
	c.Sync()
	return &method{c: c, in: in, im: im, log: log}
}

func send(t *testing.T, requests ...func() error) {
	t.Helper()
	for _, r := range requests {
		if err := r(); err != nil {
			t.Fatal(err)
		}
	}
}

func (m *method) commit(t *testing.T, requests ...func() error) {
	t.Helper()
	send(t, append(requests, func() error { return m.im.Commit(m.log.dones) }, m.c.Display.Flush)...)
}

func (ty *typist) commit(t *testing.T, requests ...func() error) {
	t.Helper()
	send(t, append(requests, ty.ti.Commit, ty.c.Display.Flush)...)
}

func until(t *testing.T, c *testclient.Client, what string, log *testclient.Log, want ...string) {
	t.Helper()
	c.DispatchUntil(what, func() bool { return len(*log) >= len(want) })
	if got := []string((*log)[:len(want)]); !slices.Equal(got, want) {
		t.Fatalf("%s: got %q, want %q", what, *log, want)
	}
	*log = (*log)[len(want):]
}

func TestAnInputMethodTypesIntoTheFocusedTextInput(t *testing.T) {
	h := Start(t, small)
	ty := typing(t, h, "editor")
	until(t, ty.c, "the focused text input entered", &ty.log.log, "enter")
	m := inputMethod(t, h)
	if other := inputMethod(t, h); !slices.Equal(other.log.log, []string{"unavailable"}) {
		t.Fatalf("a second input method on the seat got %q, want unavailable", other.log.log)
	}

	ty.commit(t, ty.ti.Enable, func() error { return ty.ti.SetSurroundingText("hello", 5, 5) }, func() error {
		return ty.ti.SetContentType(ctix.ZwpTextInputV3ContentHintSpellcheck, ctix.ZwpTextInputV3ContentPurposeNormal)
	})
	until(t, m.c, "the input method activated with the text input's state", &m.log.log,
		"activate", `surrounding "hello" 5 5`, fmt.Sprintf("content %d %d", ctix.ZwpTextInputV3ContentHintSpellcheck, ctix.ZwpTextInputV3ContentPurposeNormal), "done")

	m.commit(t, func() error { return m.im.SetPreeditString("ni", 0, 2) })
	until(t, ty.c, "the preedit", &ty.log.log, `preedit "ni" 0 2`, "done 1")
	m.commit(t, func() error { return m.im.DeleteSurroundingText(1, 0) }, func() error { return m.im.CommitString("你") })
	until(t, ty.c, "the committed text", &ty.log.log, "delete 1 0", `commit "你"`, "done 1")

	ty.commit(t, ty.ti.Disable)
	until(t, m.c, "the input method deactivated", &m.log.log, "deactivate", "done")

	ty.commit(t, ty.ti.Enable)
	until(t, m.c, "the input method activated again", &m.log.log, "activate", "done")
	other := join(t, h, "other")
	other.seen(t, "the other window focused", func() bool { return slices.Contains(other.in.Log, "kb_enter other") })
	until(t, ty.c, "the text input left with the keyboard focus", &ty.log.log, "leave")
	until(t, m.c, "the input method deactivated with the focus", &m.log.log, "deactivate", "done")
}

func TestAnInputMethodCommitWithAStaleSerialChangesNothing(t *testing.T) {
	h := Start(t, small)
	ty := typing(t, h, "editor")
	until(t, ty.c, "the focused text input entered", &ty.log.log, "enter")
	m := inputMethod(t, h)
	ty.commit(t, ty.ti.Enable)
	until(t, m.c, "the input method activated", &m.log.log, "activate", "done")
	send(t, func() error { return m.im.CommitString("stale") }, func() error { return m.im.Commit(m.log.dones + 1) }, m.c.Display.Flush)
	m.commit(t, func() error { return m.im.CommitString("fresh") })
	until(t, ty.c, "only the current commit", &ty.log.log, `commit "fresh"`, "done 1")
}

func TestAnInputMethodGrabsTheKeyboard(t *testing.T) {
	h := Start(t, small)
	ty := typing(t, h, "editor")
	until(t, ty.c, "the focused text input entered", &ty.log.log, "enter")
	const keyA, keyB = 30, 48
	h.key(keyA, true)
	ty.seen(t, "the editor got the first press", func() bool { return slices.Contains(ty.in.Log, "key 30 1") })

	m := inputMethod(t, h)
	grab := &grabLog{}
	if _, err := m.im.GrabKeyboard(grab); err != nil {
		t.Fatal(err)
	}
	m.c.DispatchUntil("the grab's keymap", func() bool { return grab.keymap != 0 && grab.repeat != [2]int32{} })
	h.key(keyB, true)
	h.key(keyB, false)
	h.key(keyA, false)
	m.c.DispatchUntil("the grabbed keys", func() bool { return len(grab.log) == 2 })
	if !slices.Equal(grab.log, []string{"key 48 1", "key 48 0"}) {
		t.Fatalf("the grab got %q", grab.log)
	}
	ty.seen(t, "the editor got the release of the key it saw pressed", func() bool { return slices.Contains(ty.in.Log, "key 30 0") })
	if slices.Contains(ty.in.Log, "key 48 1") {
		t.Fatal("a grabbed key reached the focused client")
	}
}

func TestInputMethodPopupsSitUnderTheTextCursor(t *testing.T) {
	h := Start(t, small)
	ty := typing(t, h, "editor")
	until(t, ty.c, "the focused text input entered", &ty.log.log, "enter")
	m := inputMethod(t, h)
	s := m.c.Surface()
	rects := &rectangleLog{}
	if _, err := m.im.GetInputPopupSurface(rects, s); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(s.Attach(m.c.Buffer(image.Pt(20, 10), 0, testclient.Fill(0xff, 0, 0xff)), 0, 0), s.DamageBuffer(0, 0, 20, 10), s.Commit(), m.c.Display.Flush()); err != nil {
		t.Fatal(err)
	}
	ty.commit(t, ty.ti.Enable, func() error { return ty.ti.SetCursorRectangle(30, 20, 2, 16) })
	until(t, m.c, "the input method activated", &m.log.log, "activate", "done")
	h.Eventually("the popup under the text cursor", showing(31+1, 36+1, [3]byte{0xff, 0, 0xff}))
	m.c.DispatchUntil("the text input rectangle", func() bool { return len(rects.rects) > 0 })
	if got, want := rects.rects[len(rects.rects)-1], image.Rect(0, -16, 2, 0); got != want {
		t.Fatalf("the popup was told the cursor is at %v, want %v", got, want)
	}
	m.in.Name(s, "popup")
	h.click(0, 35, 40, btnLeft)
	m.c.DispatchUntil("the click on the popup", func() bool { return slices.Contains(m.in.Log, "button 272 0") })
	if !slices.Contains(m.in.Log, "enter popup 5,4") {
		t.Fatalf("the input method's client saw %q, want the pointer to enter its popup at 5,4", m.in.Log)
	}
	ty.c.Sync()
	if slices.Contains(ty.in.Log, "kb_leave editor") || slices.Contains(m.log.log, "deactivate") {
		t.Fatal("clicking the popup took the keyboard focus from the text input")
	}
	ty.commit(t, ty.ti.Disable)
	h.Eventually("the popup hidden with the input method", showing(32, 37, [3]byte{0xff, 0, 0}))
}

func TestAnUnavailableInputMethodNeitherTypesNorGrabs(t *testing.T) {
	h := Start(t, small)
	ty := typing(t, h, "editor")
	until(t, ty.c, "the focused text input entered", &ty.log.log, "enter")
	m := inputMethod(t, h)
	other := inputMethod(t, h)
	ty.commit(t, ty.ti.Enable)
	until(t, m.c, "the input method activated", &m.log.log, "activate", "done")

	other.commit(t, func() error { return other.im.CommitString("intruder") })
	grab := &grabLog{}
	if _, err := other.im.GrabKeyboard(grab); err != nil {
		t.Fatal(err)
	}
	other.c.Sync()
	m.commit(t, func() error { return m.im.CommitString("real") })
	until(t, ty.c, "only the available input method's text", &ty.log.log, `commit "real"`, "done 1")
	h.key(30, true)
	h.key(30, false)
	ty.seen(t, "the editor still got its keys", func() bool { return slices.Contains(ty.in.Log, "key 30 0") })
	other.c.Sync()
	if len(grab.log) != 0 || grab.keymap != 0 {
		t.Fatalf("an unavailable input method grabbed the keyboard: %q", grab.log)
	}
}
