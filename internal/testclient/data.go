package testclient

import (
	"errors"
	"io"
	"os"
	"slices"

	"golang.org/x/sys/unix"

	cps "github.com/stubbedev/tilde/internal/protocol/client/primaryselection"
	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	"github.com/stubbedev/tilde/internal/wire"
)

type Offer struct {
	Data          *cw.WlDataOffer
	Primary       *cps.ZwpPrimarySelectionOfferV1
	Mimes         []string
	SourceActions cw.WlDataDeviceManagerDndAction
	Action        cw.WlDataDeviceManagerDndAction
}

type Clipboard struct {
	Device    *cw.WlDataDevice
	Primaries *cps.ZwpPrimarySelectionDeviceV1
	Log       Log
	Selection *Offer
	Primary   *Offer
	Drag      *Offer
	Serial    uint32
	pending   *Offer
}

func (c *Client) Clipboard(seat *cw.WlSeat) *Clipboard {
	c.t.Helper()
	cb := &Clipboard{}
	var err error
	cb.Device, err = c.DataDevices.GetDataDevice(dataDeviceEvents{cb}, seat)
	c.must(err)
	cb.Primaries, err = c.PrimarySelection.GetDevice(primaryDeviceEvents{cb}, seat)
	c.must(err)
	return cb
}

type dataDeviceEvents struct{ cb *Clipboard }

func (e dataDeviceEvents) DataOffer(_ *cw.WlDataDevice, res *cw.WlDataOffer) (cw.WlDataOfferHandler, error) {
	o := &Offer{Data: res}
	e.cb.pending = o
	return dataOfferEvents{o}, nil
}

func (e dataDeviceEvents) Enter(_ *cw.WlDataDevice, serial uint32, _ *cw.WlSurface, x, y wire.Fixed, res *cw.WlDataOffer) error {
	e.cb.Serial = serial
	e.cb.Drag = nil
	if res != nil {
		e.cb.Drag = e.cb.pending
	}
	return e.cb.Log.Note("enter %g,%g", x.Float(), y.Float())
}

func (e dataDeviceEvents) Leave(*cw.WlDataDevice) error {
	return e.cb.Log.Note("leave")
}

func (e dataDeviceEvents) Motion(_ *cw.WlDataDevice, _ uint32, x, y wire.Fixed) error {
	return e.cb.Log.Note("motion %g,%g", x.Float(), y.Float())
}

func (e dataDeviceEvents) Drop(*cw.WlDataDevice) error {
	return e.cb.Log.Note("drop")
}

func (e dataDeviceEvents) Selection(_ *cw.WlDataDevice, res *cw.WlDataOffer) error {
	e.cb.Selection = nil
	if res != nil {
		e.cb.Selection = e.cb.pending
	}
	return e.cb.Log.Note("selection %v", res != nil)
}

type dataOfferEvents struct{ o *Offer }

func (e dataOfferEvents) Offer(_ *cw.WlDataOffer, mime string) error {
	e.o.Mimes = append(e.o.Mimes, mime)
	return nil
}

func (e dataOfferEvents) SourceActions(_ *cw.WlDataOffer, a cw.WlDataDeviceManagerDndAction) error {
	e.o.SourceActions = a
	return nil
}

func (e dataOfferEvents) Action(_ *cw.WlDataOffer, a cw.WlDataDeviceManagerDndAction) error {
	e.o.Action = a
	return nil
}

type primaryDeviceEvents struct{ cb *Clipboard }

func (e primaryDeviceEvents) DataOffer(_ *cps.ZwpPrimarySelectionDeviceV1, res *cps.ZwpPrimarySelectionOfferV1) (cps.ZwpPrimarySelectionOfferV1Handler, error) {
	o := &Offer{Primary: res}
	e.cb.pending = o
	return primaryOfferEvents{o}, nil
}

func (e primaryDeviceEvents) Selection(_ *cps.ZwpPrimarySelectionDeviceV1, res *cps.ZwpPrimarySelectionOfferV1) error {
	e.cb.Primary = nil
	if res != nil {
		e.cb.Primary = e.cb.pending
	}
	return e.cb.Log.Note("primary %v", res != nil)
}

type primaryOfferEvents struct{ o *Offer }

func (e primaryOfferEvents) Offer(_ *cps.ZwpPrimarySelectionOfferV1, mime string) error {
	e.o.Mimes = append(e.o.Mimes, mime)
	return nil
}

type Source struct {
	Data    *cw.WlDataSource
	Primary *cps.ZwpPrimarySelectionSourceV1
	Log     Log
	content map[string]string
	c       *Client
}

func sortedMimes(content map[string]string) []string {
	mimes := make([]string, 0, len(content))
	for m := range content {
		mimes = append(mimes, m)
	}
	slices.Sort(mimes)
	return mimes
}

func (c *Client) DataSource(content map[string]string) *Source {
	c.t.Helper()
	s := &Source{content: content, c: c}
	var err error
	s.Data, err = c.DataDevices.CreateDataSource(dataSourceEvents{s})
	c.must(err)
	for _, m := range sortedMimes(content) {
		c.must(s.Data.Offer(m))
	}
	return s
}

func (c *Client) PrimarySource(content map[string]string) *Source {
	c.t.Helper()
	s := &Source{content: content, c: c}
	var err error
	s.Primary, err = c.PrimarySelection.CreateSource(primarySourceEvents{s})
	c.must(err)
	for _, m := range sortedMimes(content) {
		c.must(s.Primary.Offer(m))
	}
	return s
}

func (s *Source) send(mime string, fd int) error {
	f := os.NewFile(uintptr(fd), "transfer")
	_, werr := io.WriteString(f, s.content[mime])
	s.c.sends++
	return errors.Join(werr, f.Close(), s.Log.Note("send %s", mime))
}

type dataSourceEvents struct{ s *Source }

func (e dataSourceEvents) Target(_ *cw.WlDataSource, mime *string) error {
	if mime == nil {
		return e.s.Log.Note("target none")
	}
	return e.s.Log.Note("target %s", *mime)
}

func (e dataSourceEvents) Send(_ *cw.WlDataSource, mime string, fd int) error {
	return e.s.send(mime, fd)
}

func (e dataSourceEvents) Cancelled(*cw.WlDataSource) error {
	return e.s.Log.Note("cancelled")
}

func (e dataSourceEvents) DndDropPerformed(*cw.WlDataSource) error {
	return e.s.Log.Note("dropped")
}

func (e dataSourceEvents) DndFinished(*cw.WlDataSource) error {
	return e.s.Log.Note("finished")
}

func (e dataSourceEvents) Action(_ *cw.WlDataSource, a cw.WlDataDeviceManagerDndAction) error {
	return e.s.Log.Note("action %d", a)
}

type primarySourceEvents struct{ s *Source }

func (e primarySourceEvents) Send(_ *cps.ZwpPrimarySelectionSourceV1, mime string, fd int) error {
	return e.s.send(mime, fd)
}

func (e primarySourceEvents) Cancelled(*cps.ZwpPrimarySelectionSourceV1) error {
	return e.s.Log.Note("cancelled")
}

func (c *Client) Receive(request func(fd int) error, source *Client) string {
	c.t.Helper()
	var p [2]int
	c.must(unix.Pipe2(p[:], unix.O_CLOEXEC))
	r := os.NewFile(uintptr(p[0]), "receive")
	defer func() { _ = r.Close() }()
	sends := source.sends
	c.must(errors.Join(request(p[1]), unix.Close(p[1]), c.Display.Flush()))
	source.DispatchUntil("the source to send", func() bool { return source.sends > sends })
	out, err := io.ReadAll(r)
	c.must(err)
	return string(out)
}
