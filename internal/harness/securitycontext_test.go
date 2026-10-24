package harness

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend/headless"
	"github.com/stubbedev/lys-wm/internal/compositor"
	cwsc "github.com/stubbedev/lys-wm/internal/protocol/client/securitycontext"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	cwsc2 "github.com/stubbedev/lys-wm/internal/protocol/client/wlrscreencopy"
	"github.com/stubbedev/lys-wm/internal/testclient"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wire"
	"github.com/stubbedev/lys-wm/internal/wlclient"
)

type globalNames map[string]uint32

func (g globalNames) Global(_ *cw.WlRegistry, name uint32, iface string, _ uint32) error {
	g[iface] = name
	return nil
}

func (globalNames) GlobalRemove(*cw.WlRegistry, uint32) error { return nil }

func listeningSocket(t *testing.T) (string, int) {
	t.Helper()
	path := filepath.Join(testenv.SocketDir(t), "sandbox")
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(unix.Bind(fd, &unix.SockaddrUnix{Name: path}), unix.Listen(fd, 8)); err != nil {
		t.Fatal(errors.Join(err, unix.Close(fd)))
	}
	return path, fd
}

func registerSandbox(t *testing.T, c *testclient.Client, listenFD, closeFD int) {
	t.Helper()
	m, err := cwsc.NewWpSecurityContextManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	ctx, err := m.CreateListener(listenFD, closeFD)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(ctx.SetSandboxEngine("org.flatpak"), ctx.SetAppID("org.example.App"), ctx.SetInstanceID("1"), ctx.Commit(), c.Display.Roundtrip()); err != nil {
		t.Fatal(err)
	}
}

func sandboxedGlobals(t *testing.T, path string) (*wlclient.Display, *cw.WlRegistry, globalNames) {
	t.Helper()
	d, err := wlclient.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	names := globalNames{}
	reg, err := d.WlDisplay().GetRegistry(names)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return d, reg, names
}

func TestSandboxedClientsDoNotSeePrivilegedGlobals(t *testing.T) {
	h := Start(t, small)
	host := testclient.Connect(t, h.Socket())
	path, listenFD := listeningSocket(t)
	var pipe [2]int
	if err := unix.Pipe2(pipe[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	registerSandbox(t, host, listenFD, pipe[0])
	if err := errors.Join(unix.Close(listenFD), unix.Close(pipe[0])); err != nil {
		t.Fatal(err)
	}

	_, _, hostNames := sandboxedGlobals(t, h.Socket())
	d, reg, names := sandboxedGlobals(t, path)
	for _, ordinary := range []string{"wl_compositor", "xdg_wm_base", "wl_seat", "wl_data_device_manager", "zwp_linux_dmabuf_v1", "xdg_activation_v1"} {
		if _, ok := hostNames[ordinary]; ok {
			if _, ok := names[ordinary]; !ok {
				t.Errorf("a sandboxed client lacks %s", ordinary)
			}
		}
	}
	for _, privileged := range []string{
		"zwlr_screencopy_manager_v1", "ext_image_copy_capture_manager_v1", "zwlr_data_control_manager_v1", "ext_data_control_manager_v1",
		"zwlr_foreign_toplevel_manager_v1", "ext_foreign_toplevel_list_v1", "zwlr_output_manager_v1", "zwp_virtual_keyboard_manager_v1", "zwlr_virtual_pointer_manager_v1",
		"ext_session_lock_manager_v1", "zwlr_layer_shell_v1", "wp_security_context_manager_v1", "zwlr_gamma_control_manager_v1", "zwp_input_method_manager_v2",
	} {
		if _, ok := hostNames[privileged]; !ok {
			t.Errorf("the host lacks %s, so the test proves nothing about it", privileged)
		}
		if _, ok := names[privileged]; ok {
			t.Errorf("a sandboxed client sees %s", privileged)
		}
	}
	screencopy, err := cwsc2.NewZwlrScreencopyManagerV1(d, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Bind(hostNames["zwlr_screencopy_manager_v1"], screencopy); err != nil {
		t.Fatal(err)
	}
	var pe *wire.ProtocolError
	if err := d.Roundtrip(); !errors.As(err, &pe) {
		t.Fatalf("binding screencopy by name from the sandbox: %v", err)
	}

	if err := unix.Close(pipe[1]); err != nil {
		t.Fatal(err)
	}
	host.Sync()
	if _, err := wlclient.Connect(path); err == nil {
		t.Fatal("the sandbox socket still accepts after its close fd hung up")
	}
}

func TestASecurityContextNeedsAListeningSocket(t *testing.T) {
	h := Start(t, small)
	c := testclient.Connect(t, h.Socket())
	m, err := cwsc.NewWpSecurityContextManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	var pipe [2]int
	if err := unix.Pipe2(pipe[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = errors.Join(unix.Close(pipe[0]), unix.Close(pipe[1])) }()
	if _, err := m.CreateListener(pipe[0], pipe[1]); err != nil {
		t.Fatal(err)
	}
	c.ExpectProtocolError(uint32(cwsc.WpSecurityContextManagerV1ErrorInvalidListenFD))
}

func securityContext(t *testing.T, c *testclient.Client) (*cwsc.WpSecurityContextV1, int) {
	t.Helper()
	m, err := cwsc.NewWpSecurityContextManagerV1(c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Bind(m)
	_, listenFD := listeningSocket(t)
	var closeFDs [2]int
	if err := unix.Pipe2(closeFDs[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	ctx, err := m.CreateListener(listenFD, closeFDs[0])
	if err = errors.Join(err, unix.Close(listenFD), unix.Close(closeFDs[0])); err != nil {
		t.Fatal(err)
	}
	return ctx, closeFDs[1]
}

func TestSecurityContextMetadataRules(t *testing.T) {
	cases := map[string]struct {
		code uint32
		do   func(ctx *cwsc.WpSecurityContextV1) error
	}{
		"an app id set twice": {uint32(cwsc.WpSecurityContextV1ErrorAlreadySet), func(ctx *cwsc.WpSecurityContextV1) error {
			return errors.Join(ctx.SetAppID("a"), ctx.SetAppID("b"))
		}},
		"a commit twice": {uint32(cwsc.WpSecurityContextV1ErrorAlreadyUsed), func(ctx *cwsc.WpSecurityContextV1) error {
			return errors.Join(ctx.Commit(), ctx.Commit())
		}},
		"metadata after the commit": {uint32(cwsc.WpSecurityContextV1ErrorAlreadyUsed), func(ctx *cwsc.WpSecurityContextV1) error {
			return errors.Join(ctx.Commit(), ctx.SetAppID("late"))
		}},
	}
	h := Start(t, small)
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := testclient.Connect(t, h.Socket())
			ctx, closeFD := securityContext(t, c)
			defer unix.Close(closeFD)
			if err := tc.do(ctx); err != nil {
				t.Fatal(err)
			}
			c.ExpectProtocolError(tc.code)
		})
	}
}

func TestAnUncommittedSecurityContextLetsGoOfItsFDs(t *testing.T) {
	h := Start(t, small)
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
	c := testclient.Connect(t, h.Socket())
	if err := c.Display.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	baseline := openFDs(t)
	ctx, closeFD := securityContext(t, c)
	if err := errors.Join(ctx.Destroy(), c.Display.Roundtrip(), unix.Close(closeFD)); err != nil {
		t.Fatal(err)
	}
	h.Do(func(*headless.Backend, *compositor.Compositor) error { return nil })
	if n := openFDs(t); n != baseline {
		t.Fatalf("%d file descriptors open after an uncommitted security context was destroyed, %d before", n, baseline)
	}
}
