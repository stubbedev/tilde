package shell

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	sxa "github.com/stubbedev/tilde/internal/protocol/server/xdgactivation"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const (
	activationVersion = 1
	liveTokens        = 64
)

type Activate struct{}

func (Activate) isRequest() {}

type token struct {
	value   string
	granted bool
}

func (sh *Shell) bindActivation(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxa.NewXdgActivationV1(c, id, version, activation{sh, c})
	return err
}

type activation struct {
	sh *Shell
	c  *server.Client
}

func (activation) Destroy(*sxa.XdgActivationV1) error {
	return nil
}

func (a activation) GetActivationToken(*sxa.XdgActivationV1, *sxa.XdgActivationTokenV1) (sxa.XdgActivationTokenV1Handler, error) {
	return &tokenRequest{sh: a.sh, c: a.c}, nil
}

func (a activation) Activate(_ *sxa.XdgActivationV1, value string, wl *sw.WlSurface) error {
	sh := a.sh
	i := slices.IndexFunc(sh.tokens, func(t token) bool { return t.value == value })
	if i < 0 {
		return nil
	}
	t := sh.tokens[i]
	sh.tokens = slices.Delete(sh.tokens, i, i+1)
	if !t.granted {
		return nil
	}
	s, err := surface.From(wl)
	if err != nil {
		return err
	}
	xs, ok := s.Role().(*XdgSurface)
	if !ok {
		return nil
	}
	top, ok := xs.role.(*Toplevel)
	if !ok {
		return nil
	}
	return top.request(Activate{})
}

type tokenRequest struct {
	sh        *Shell
	c         *server.Client
	serial    uint32
	committed bool
}

func (r *tokenRequest) SetSerial(_ *sxa.XdgActivationTokenV1, serial uint32, _ *sw.WlSeat) error {
	r.serial = serial
	return nil
}

func (*tokenRequest) SetAppID(*sxa.XdgActivationTokenV1, string) error {
	return nil
}

func (*tokenRequest) SetSurface(*sxa.XdgActivationTokenV1, *sw.WlSurface) error {
	return nil
}

func (r *tokenRequest) Commit(obj *sxa.XdgActivationTokenV1) error {
	if r.committed {
		return obj.ProtocolError(sxa.XdgActivationTokenV1ErrorAlreadyUsed, "the token was already committed")
	}
	r.committed = true
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Errorf("shell: generate an activation token: %w", err)
	}
	t := token{value: hex.EncodeToString(raw[:]), granted: r.sh.listener.UserActive(r.c, r.serial)}
	if len(r.sh.tokens) == liveTokens {
		r.sh.tokens = slices.Delete(r.sh.tokens, 0, 1)
	}
	r.sh.tokens = append(r.sh.tokens, t)
	return obj.SendDone(t.value)
}

func (*tokenRequest) Destroy(*sxa.XdgActivationTokenV1) error {
	return nil
}
