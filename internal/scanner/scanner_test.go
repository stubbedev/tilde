package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	cw "github.com/stubbedev/tilde/internal/protocol/client/wayland"
	scs "github.com/stubbedev/tilde/internal/protocol/server/cursorshape"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	swlr "github.com/stubbedev/tilde/internal/protocol/server/wlrlayershell"
	sxdg "github.com/stubbedev/tilde/internal/protocol/server/xdgshell"
	"github.com/stubbedev/tilde/internal/wire"
)

func TestGeneratedTreeIsCurrent(t *testing.T) {
	const dir = "../protocol"
	m, err := loadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	module, err := modulePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	protocols, err := loadProtocols(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sides {
		var want []string
		for _, p := range protocols {
			src, err := generate(p, s, module)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(dir, s.dir(), p.pkg, "protocol.go")
			want = append(want, name)
			got, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(got, src) {
				t.Errorf("%s is stale; run go generate ./internal/scanner", name)
			}
		}
		got, err := filepath.Glob(filepath.Join(dir, s.dir(), "*", "*"))
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s holds files the manifest does not generate: %v", s.dir(), got)
		}
	}
}

func TestNames(t *testing.T) {
	cases := map[string]string{"wl_surface": "WlSurface", "set_buffer_scale": "SetBufferScale", "90": "90", "flipped_270": "Flipped270", "delete_id": "DeleteID", "get_fd": "GetFD"}
	for in, want := range cases {
		if got := camel(in); got != want {
			t.Errorf("camel(%q) = %q, want %q", in, got, want)
		}
	}
	imports := map[string]bool{"wayland": true}
	args := map[string]string{"physical_width": "physicalWidth", "make": "makeArg", "interface": "interfaceArg", "wayland": "waylandArg", "obj": "objArg", "id": "id", "object_id": "objectID", "fd": "fd"}
	for in, want := range args {
		if got := argName(in, imports); got != want {
			t.Errorf("argName(%q) = %q, want %q", in, got, want)
		}
	}
}

func parseAll(xmls ...string) error {
	var protocols []*protocol
	for i, x := range xmls {
		p, err := parseProtocol(strings.NewReader(x), "test.xml", "p"+string(rune('a'+i)))
		if err != nil {
			return err
		}
		protocols = append(protocols, p)
	}
	return resolve(protocols)
}

func TestInvalidProtocolsAreRejected(t *testing.T) {
	cases := []struct {
		name string
		xml  []string
		want string
	}{
		{"no version", []string{`<protocol name="p"><interface name="a"/></protocol>`}, "has no version"},
		{"since above version", []string{`<protocol name="p"><interface name="a" version="1"><request name="r" since="2"/></interface></protocol>`}, "exceeds interface version"},
		{"unknown type", []string{`<protocol name="p"><interface name="a" version="1"><request name="r"><arg name="x" type="float"/></request></interface></protocol>`}, "unknown type"},
		{"nullable int", []string{`<protocol name="p"><interface name="a" version="1"><request name="r"><arg name="x" type="int" allow-null="true"/></request></interface></protocol>`}, "cannot be nullable"},
		{"unknown interface", []string{`<protocol name="p"><interface name="a" version="1"><request name="r"><arg name="x" type="object" interface="nope"/></request></interface></protocol>`}, "unknown interface"},
		{"unknown enum", []string{`<protocol name="p"><interface name="a" version="1"><request name="r"><arg name="x" type="uint" enum="nope"/></request></interface></protocol>`}, "unknown enum"},
		{"two new_ids", []string{`<protocol name="p"><interface name="a" version="1"><request name="r"><arg name="x" type="new_id" interface="a"/><arg name="y" type="new_id" interface="a"/></request></interface></protocol>`}, "more than one new_id"},
		{"untyped new_id event", []string{`<protocol name="p"><interface name="a" version="1"><event name="e"><arg name="x" type="new_id"/></event></interface></protocol>`}, "unnamed interface"},
		{"reserved request", []string{`<protocol name="p"><interface name="a" version="1"><request name="dispatch"/></interface></protocol>`}, "collides with a generated method"},
		{"bad enum value", []string{`<protocol name="p"><interface name="a" version="1"><enum name="e"><entry name="x" value="z"/></enum></interface></protocol>`}, "invalid value"},
		{"duplicate interface", []string{`<protocol name="p"><interface name="a" version="1"/></protocol>`, `<protocol name="q"><interface name="a" version="1"/></protocol>`}, "defined in both"},
		{"import cycle", []string{
			`<protocol name="p"><interface name="a" version="1"><request name="r"><arg name="x" type="object" interface="b"/></request></interface></protocol>`,
			`<protocol name="q"><interface name="b" version="1"><request name="r"><arg name="x" type="object" interface="a"/></request></interface></protocol>`,
		}, "import each other"},
	}
	for _, c := range cases {
		if err := parseAll(c.xml...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

func TestIdentifierCollisionsFailGeneration(t *testing.T) {
	x := `<protocol name="p"><interface name="a" version="1"><enum name="b"><entry name="c" value="0"/></enum></interface><interface name="a_b" version="1"/></protocol>`
	p, err := parseProtocol(strings.NewReader(x), "test.xml", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := resolve([]*protocol{p}); err != nil {
		t.Fatal(err)
	}
	if _, err := generate(p, serverSide, "example.com/m"); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("generate = %v, want a duplicate identifier error", err)
	}
}

type peer struct {
	t       *testing.T
	conn    *wire.Conn
	objects map[wire.ObjectID]wire.Resource
	next    wire.ObjectID
	removed []wire.ObjectID
}

func (p *peer) Conn() *wire.Conn { return p.conn }

func (p *peer) Get(id wire.ObjectID) (wire.Resource, bool) {
	r, ok := p.objects[id]
	return r, ok
}

func (p *peer) Add(r wire.Resource) error {
	id := r.Base().ID()
	if _, dup := p.objects[id]; dup {
		return wire.ErrInvalidNewID
	}
	p.objects[id] = r
	return nil
}

func (p *peer) Remove(id wire.ObjectID) error {
	delete(p.objects, id)
	p.removed = append(p.removed, id)
	return nil
}

func (p *peer) NewID() (wire.ObjectID, error) {
	p.next++
	return p.next, nil
}

func (p *peer) receive() error {
	if err := p.conn.Fill(); err != nil {
		return err
	}
	for {
		m, err := p.conn.Next()
		if err != nil || m == nil {
			return err
		}
		r, ok := p.objects[m.ID]
		if !ok {
			p.t.Fatalf("message for unknown object %d", m.ID)
		}
		if err := r.Dispatch(m); err != nil {
			return err
		}
	}
}

func connect(t *testing.T) (server, client *peer) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	server = &peer{t: t, conn: wire.NewConn(fds[0]), objects: map[wire.ObjectID]wire.Resource{}, next: 0xff000000 - 1}
	client = &peer{t: t, conn: wire.NewConn(fds[1]), objects: map[wire.ObjectID]wire.Resource{}}
	t.Cleanup(func() {
		_ = server.conn.Close()
		_ = client.conn.Close()
	})
	return server, client
}

func deliver(t *testing.T, from, to *peer) error {
	t.Helper()
	if err := from.conn.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return to.receive()
}

type compositor struct {
	sw.WlCompositorHandler
	surface *surface
}

func (c *compositor) CreateSurface(_ *sw.WlCompositor, s *sw.WlSurface) (sw.WlSurfaceHandler, error) {
	c.surface.resource = s
	return c.surface, nil
}

func (c *compositor) CreateRegion(*sw.WlCompositor, *sw.WlRegion) (sw.WlRegionHandler, error) {
	return struct{ sw.WlRegionHandler }{}, nil
}

type surface struct {
	sw.WlSurfaceHandler
	resource  *sw.WlSurface
	attached  *sw.WlBuffer
	attachX   int32
	destroyed bool
}

func (s *surface) Attach(_ *sw.WlSurface, b *sw.WlBuffer, x, _ int32) error {
	s.attached, s.attachX = b, x
	return nil
}

func (s *surface) Destroy(*sw.WlSurface) error {
	s.destroyed = true
	return nil
}

type clientSurface struct {
	cw.WlSurfaceHandler
	entered *cw.WlOutput
}

func (s *clientSurface) Enter(_ *cw.WlSurface, o *cw.WlOutput) error {
	s.entered = o
	return nil
}

type clientOutput struct{ cw.WlOutputHandler }

type session struct {
	server, client *peer
	compositor     *compositor
	clientComp     *cw.WlCompositor
}

func newSession(t *testing.T, serverVersion uint32) *session {
	server, client := connect(t)
	s := &session{server: server, client: client, compositor: &compositor{surface: &surface{}}}
	var err error
	if s.clientComp, err = cw.NewWlCompositor(client, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.NewWlCompositor(server, s.clientComp.Base().ID(), serverVersion, s.compositor); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGeneratedServerAndClientInterOperate(t *testing.T) {
	s := newSession(t, 6)
	cs := &clientSurface{}
	clientSurf, err := s.clientComp.CreateSurface(cs)
	if err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, s.client, s.server); err != nil {
		t.Fatal(err)
	}
	srvSurf := s.compositor.surface.resource
	if srvSurf == nil || srvSurf.Base().ID() != clientSurf.Base().ID() || srvSurf.Base().Version() != 6 {
		t.Fatalf("server surface = %+v, want id %d at version 6", srvSurf, clientSurf.Base().ID())
	}

	if err := clientSurf.Attach(nil, 7, 8); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, s.client, s.server); err != nil {
		t.Fatal(err)
	}
	if s.compositor.surface.attached != nil || s.compositor.surface.attachX != 7 {
		t.Fatal("nullable buffer or int argument decoded wrongly")
	}

	clientOut, err := cw.NewWlOutput(s.client, 4, clientOutput{})
	if err != nil {
		t.Fatal(err)
	}
	srvOut, err := sw.NewWlOutput(s.server, clientOut.Base().ID(), 4, struct{ sw.WlOutputHandler }{})
	if err != nil {
		t.Fatal(err)
	}
	if err := srvSurf.SendEnter(srvOut); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, s.server, s.client); err != nil {
		t.Fatal(err)
	}
	if cs.entered != clientOut {
		t.Fatal("object argument did not resolve to the client's proxy")
	}

	if err := srvSurf.SendEnter(nil); !errors.Is(err, wire.ErrNullObject) {
		t.Fatalf("SendEnter(nil) = %v, want ErrNullObject", err)
	}

	if err := clientSurf.Destroy(); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, s.client, s.server); err != nil {
		t.Fatal(err)
	}
	if !s.compositor.surface.destroyed || !slices.Contains(s.server.removed, srvSurf.Base().ID()) || !slices.Contains(s.client.removed, clientSurf.Base().ID()) {
		t.Fatal("destructor did not reach the handler and remove the object on both sides")
	}
}

type registry struct {
	sw.WlRegistryHandler
	name    uint32
	iface   string
	version uint32
	id      wire.ObjectID
}

func (r *registry) Bind(_ *sw.WlRegistry, name uint32, iface string, version uint32, id wire.ObjectID) error {
	r.name, r.iface, r.version, r.id = name, iface, version, id
	return nil
}

func TestUntypedNewIDBind(t *testing.T) {
	server, client := connect(t)
	reg := &registry{}
	clientReg, err := cw.NewWlRegistry(client, 1, struct{ cw.WlRegistryHandler }{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sw.NewWlRegistry(server, clientReg.Base().ID(), 1, reg); err != nil {
		t.Fatal(err)
	}
	comp, err := cw.NewWlCompositor(client, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := clientReg.Bind(42, comp); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, client, server); err != nil {
		t.Fatal(err)
	}
	if reg.name != 42 || reg.iface != "wl_compositor" || reg.version != 5 || reg.id != comp.Base().ID() {
		t.Fatalf("bind decoded as %+v", reg)
	}
}

type shm struct {
	sw.WlShmHandler
	fd   int
	size int32
}

func (s *shm) CreatePool(_ *sw.WlShm, _ *sw.WlShmPool, fd int, size int32) (sw.WlShmPoolHandler, error) {
	s.fd, s.size = fd, size
	return struct{ sw.WlShmPoolHandler }{}, nil
}

type clientShm struct {
	cw.WlShmHandler
	formats []cw.WlShmFormat
}

func (s *clientShm) Format(_ *cw.WlShm, f cw.WlShmFormat) error {
	s.formats = append(s.formats, f)
	return nil
}

func TestFDAndEnumArguments(t *testing.T) {
	server, client := connect(t)
	cs := &clientShm{}
	clientShm, err := cw.NewWlShm(client, 1, cs)
	if err != nil {
		t.Fatal(err)
	}
	ss := &shm{fd: -1}
	srvShm, err := sw.NewWlShm(server, clientShm.Base().ID(), 1, ss)
	if err != nil {
		t.Fatal(err)
	}
	if err := srvShm.SendFormat(sw.WlShmFormatXrgb8888); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, server, client); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cs.formats, []cw.WlShmFormat{cw.WlShmFormatXrgb8888}) {
		t.Fatalf("formats = %v", cs.formats)
	}
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	if _, err := clientShm.CreatePool(p[0], 4096); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, client, server); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(ss.fd)
	var a, b unix.Stat_t
	if err := unix.Fstat(ss.fd, &a); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fstat(p[0], &b); err != nil {
		t.Fatal(err)
	}
	if a.Ino != b.Ino || ss.size != 4096 {
		t.Fatalf("pool fd or size decoded wrongly: size %d", ss.size)
	}
	if !sw.WlSeatCapability(3).Has(sw.WlSeatCapabilityKeyboard) || sw.WlSeatCapability(1).Has(sw.WlSeatCapabilityKeyboard) {
		t.Fatal("bitfield Has is wrong")
	}
}

type dataDevice struct {
	cw.WlDataDeviceHandler
	offer  *cw.WlDataOffer
	offers *dataOffer
}

func (d *dataDevice) DataOffer(_ *cw.WlDataDevice, o *cw.WlDataOffer) (cw.WlDataOfferHandler, error) {
	d.offer = o
	return d.offers, nil
}

type dataOffer struct {
	cw.WlDataOfferHandler
	mimes []string
}

func (o *dataOffer) Offer(_ *cw.WlDataOffer, mime string) error {
	o.mimes = append(o.mimes, mime)
	return nil
}

func TestEventCreatesObject(t *testing.T) {
	server, client := connect(t)
	dd := &dataDevice{offers: &dataOffer{}}
	clientDev, err := cw.NewWlDataDevice(client, 3, dd)
	if err != nil {
		t.Fatal(err)
	}
	srvDev, err := sw.NewWlDataDevice(server, clientDev.Base().ID(), 3, struct{ sw.WlDataDeviceHandler }{})
	if err != nil {
		t.Fatal(err)
	}
	offer, err := srvDev.SendDataOffer(struct{ sw.WlDataOfferHandler }{})
	if err != nil {
		t.Fatal(err)
	}
	if err := offer.SendOffer("text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, server, client); err != nil {
		t.Fatal(err)
	}
	if dd.offer == nil || dd.offer.Base().ID() != offer.Base().ID() || offer.Base().ID() < 0xff000000 {
		t.Fatal("event new_id did not create a client proxy with the server's id")
	}
	if !slices.Equal(dd.offers.mimes, []string{"text/plain"}) {
		t.Fatalf("offers = %v", dd.offers.mimes)
	}
	if _, err := srvDev.SendDataOffer(nil); !errors.Is(err, wire.ErrNoHandler) {
		t.Fatalf("SendDataOffer(nil) = %v, want ErrNoHandler", err)
	}
}

func TestVersionChecks(t *testing.T) {
	s := newSession(t, 4)
	clientSurf, err := s.clientComp.CreateSurface(&clientSurface{})
	if err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, s.client, s.server); err != nil {
		t.Fatal(err)
	}
	if err := s.compositor.surface.resource.SendPreferredBufferScale(2); !errors.Is(err, wire.ErrVersion) {
		t.Fatalf("event above the bound version = %v, want ErrVersion", err)
	}
	if err := clientSurf.Offset(1, 2); err != nil {
		t.Fatal(err)
	}
	var me *wire.MessageError
	if err := deliver(t, s.client, s.server); !errors.As(err, &me) || !errors.Is(err, wire.ErrVersion) {
		t.Fatalf("request above the bound version = %v, want a MessageError wrapping ErrVersion", err)
	}
}

type nilSurfaces struct{ sw.WlCompositorHandler }

func (nilSurfaces) CreateSurface(*sw.WlCompositor, *sw.WlSurface) (sw.WlSurfaceHandler, error) {
	return nil, nil
}

func TestMalformedRequestsAreProtocolErrors(t *testing.T) {
	cases := []struct {
		name string
		send func(s *session, surf *cw.WlSurface, region *cw.WlRegion)
		want error
	}{
		{"wrong interface", func(s *session, surf *cw.WlSurface, region *cw.WlRegion) {
			e := s.client.conn.Begin(surf.Base().ID(), 1)
			e.Object(region.Base().ID())
			e.Int(0)
			e.Int(0)
			_ = e.End()
		}, wire.ErrWrongInterface},
		{"unknown object", func(s *session, surf *cw.WlSurface, _ *cw.WlRegion) {
			e := s.client.conn.Begin(surf.Base().ID(), 1)
			e.Object(999)
			e.Int(0)
			e.Int(0)
			_ = e.End()
		}, wire.ErrUnknownObject},
		{"invalid opcode", func(s *session, surf *cw.WlSurface, _ *cw.WlRegion) {
			_ = s.client.conn.Begin(surf.Base().ID(), 99).End()
		}, wire.ErrInvalidOpcode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSession(t, 6)
			surf, err := s.clientComp.CreateSurface(&clientSurface{})
			if err != nil {
				t.Fatal(err)
			}
			region, err := s.clientComp.CreateRegion()
			if err != nil {
				t.Fatal(err)
			}
			c.send(s, surf, region)
			if err := deliver(t, s.client, s.server); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	server, client := connect(t)
	comp, err := cw.NewWlCompositor(client, 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sw.NewWlCompositor(server, comp.Base().ID(), 6, nilSurfaces{}); err != nil {
		t.Fatal(err)
	}
	if _, err := comp.CreateSurface(&clientSurface{}); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, client, server); !errors.Is(err, wire.ErrNoHandler) {
		t.Fatalf("handler returning no handler = %v, want ErrNoHandler", err)
	}
}

func TestGeneratedEnumsKnowTheirValuesAndVersions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
		want  bool
	}{
		{"a seat capability", sw.WlSeatCapability(7).Valid(1), true},
		{"an unknown seat capability bit", sw.WlSeatCapability(8).Valid(9), false},
		{"on-demand keyboard focus at version 3", swlr.ZwlrLayerSurfaceV1KeyboardInteractivityOnDemand.Valid(3), false},
		{"on-demand keyboard focus at version 4", swlr.ZwlrLayerSurfaceV1KeyboardInteractivityOnDemand.Valid(4), true},
		{"the dnd-ask cursor at version 1", scs.WpCursorShapeDeviceV1ShapeDndAsk.Valid(1), false},
		{"the dnd-ask cursor at version 2", scs.WpCursorShapeDeviceV1ShapeDndAsk.Valid(2), true},
		{"cursor shape 0", scs.WpCursorShapeDeviceV1Shape(0).Valid(2), false},
		{"an anchor past bottom_right", sxdg.XdgPositionerAnchor(9).Valid(7), false},
		{"every constraint adjustment", sxdg.XdgPositionerConstraintAdjustment(0x3f).Valid(1), true},
	} {
		if tc.valid != tc.want {
			t.Errorf("%s: Valid = %t, want %t", tc.name, tc.valid, tc.want)
		}
	}
}
