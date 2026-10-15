package compositor

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"github.com/stubbedev/tilde/internal/output"
	sel "github.com/stubbedev/tilde/internal/protocol/server/extforeigntoplevellist"
	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	swft "github.com/stubbedev/tilde/internal/protocol/server/wlrforeigntoplevel"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
	"github.com/stubbedev/tilde/internal/wm"
)

const (
	extToplevelListVersion = 1
	wlrToplevelVersion     = 3
)

type foreignState struct {
	title, appID          string
	output                *output.Output
	activated, fullscreen bool
	parent                *window
}

func (c *Compositor) foreignStateOf(w *window) foreignState {
	return foreignState{
		title: w.top.Title(), appID: w.top.AppID(), output: w.output,
		activated: c.active == w, fullscreen: w.layer == wm.LayerFullscreen,
		parent: c.windowOf(w.top.parent()),
	}
}

type extList struct {
	client   *server.Client
	resource *sel.ExtForeignToplevelListV1
	handles  map[*window]*extHandle
	stopped  bool
}

type extHandle struct {
	resource *sel.ExtForeignToplevelHandleV1
	sent     foreignState
}

type wlrManager struct {
	client   *server.Client
	resource *swft.ZwlrForeignToplevelManagerV1
	handles  map[*window]*wlrHandle
}

type wlrHandle struct {
	resource *swft.ZwlrForeignToplevelHandleV1
	sent     foreignState
	fresh    bool
}

func (c *Compositor) addForeignToplevels(d *server.Display) error {
	if _, err := d.AddGlobal(sel.ExtForeignToplevelListV1Interface, extToplevelListVersion, c.bindExtList); err != nil {
		return err
	}
	_, err := d.AddGlobal(swft.ZwlrForeignToplevelManagerV1Interface, wlrToplevelVersion, c.bindWlrToplevels)
	return err
}

func (c *Compositor) bindExtList(cl *server.Client, id wire.ObjectID, version uint32) error {
	l := &extList{client: cl, handles: map[*window]*extHandle{}}
	var err error
	if l.resource, err = sel.NewExtForeignToplevelListV1(cl, id, version, extListRequests{c, l}); err != nil {
		return err
	}
	server.Track(l.resource, &c.extLists, l)
	return c.syncExtList(l)
}

type extListRequests struct {
	c *Compositor
	l *extList
}

func (r extListRequests) Stop(*sel.ExtForeignToplevelListV1) error {
	if r.l.stopped {
		return nil
	}
	r.l.stopped = true
	return r.l.client.Sent(r.l.resource.SendFinished())
}

func (extListRequests) Destroy(*sel.ExtForeignToplevelListV1) error {
	return nil
}

type extHandleRequests struct{}

func (extHandleRequests) Destroy(*sel.ExtForeignToplevelHandleV1) error {
	return nil
}

func (c *Compositor) syncExtList(l *extList) error {
	if l.stopped {
		return nil
	}
	var errs []error
	for _, w := range c.gone(l.handles) {
		errs = append(errs, l.client.Sent(l.handles[w].resource.SendClosed()))
		delete(l.handles, w)
	}
	for _, w := range c.windows {
		now := c.foreignStateOf(w)
		h, ok := l.handles[w]
		if !ok {
			res, err := l.resource.SendToplevel(extHandleRequests{})
			if err != nil {
				errs = append(errs, l.client.Sent(err))
				continue
			}
			h = &extHandle{resource: res}
			l.handles[w] = h
			errs = append(errs, l.client.Sent(res.SendIdentifier(fmt.Sprintf("tilde-%d", w.id))))
		} else if h.sent.title == now.title && h.sent.appID == now.appID {
			continue
		}
		errs = append(errs, l.client.Sent(errors.Join(h.resource.SendTitle(now.title), h.resource.SendAppID(now.appID), h.resource.SendDone())))
		h.sent = now
	}
	return errors.Join(errs...)
}

func (c *Compositor) bindWlrToplevels(cl *server.Client, id wire.ObjectID, version uint32) error {
	m := &wlrManager{client: cl, handles: map[*window]*wlrHandle{}}
	var err error
	if m.resource, err = swft.NewZwlrForeignToplevelManagerV1(cl, id, version, wlrManagerRequests{m}); err != nil {
		return err
	}
	server.Track(m.resource, &c.wlrManagers, m)
	return c.syncWlrManager(m)
}

type wlrManagerRequests struct{ m *wlrManager }

func (r wlrManagerRequests) Stop(*swft.ZwlrForeignToplevelManagerV1) error {
	return r.m.client.Sent(r.m.resource.SendFinished())
}

func wlrStates(s foreignState) []byte {
	var out []byte
	if s.activated {
		out = binary.LittleEndian.AppendUint32(out, uint32(swft.ZwlrForeignToplevelHandleV1StateActivated))
	}
	if s.fullscreen {
		out = binary.LittleEndian.AppendUint32(out, uint32(swft.ZwlrForeignToplevelHandleV1StateFullscreen))
	}
	return out
}

func (c *Compositor) syncWlrManager(m *wlrManager) error {
	var errs []error
	for _, w := range c.gone(m.handles) {
		errs = append(errs, m.client.Sent(m.handles[w].resource.SendClosed()))
		delete(m.handles, w)
	}
	for _, w := range c.windows {
		if _, ok := m.handles[w]; ok {
			continue
		}
		res, err := m.resource.SendToplevel(wlrHandleRequests{c, w})
		if err != nil {
			errs = append(errs, m.client.Sent(err))
			continue
		}
		m.handles[w] = &wlrHandle{resource: res, fresh: true}
	}
	for _, w := range c.windows {
		h := m.handles[w]
		if h == nil {
			continue
		}
		errs = append(errs, m.client.Sent(c.updateWlrHandle(m, h, c.foreignStateOf(w))))
	}
	return errors.Join(errs...)
}

func (c *Compositor) updateWlrHandle(m *wlrManager, h *wlrHandle, now foreignState) error {
	if !h.fresh && h.sent == now {
		return nil
	}
	res, prev := h.resource, h.sent
	var errs []error
	if h.fresh || now.title != prev.title {
		errs = append(errs, res.SendTitle(now.title))
	}
	if h.fresh || now.appID != prev.appID {
		errs = append(errs, res.SendAppID(now.appID))
	}
	if h.fresh || now.output != prev.output {
		if prev.output != nil {
			for _, wl := range prev.output.Resources(m.client) {
				errs = append(errs, res.SendOutputLeave(wl))
			}
		}
		if now.output != nil {
			for _, wl := range now.output.Resources(m.client) {
				errs = append(errs, res.SendOutputEnter(wl))
			}
		}
	}
	if h.fresh || now.activated != prev.activated || now.fullscreen != prev.fullscreen {
		errs = append(errs, res.SendState(wlrStates(now)))
	}
	if res.Base().Version() >= 3 && (h.fresh || now.parent != prev.parent) {
		var parent *swft.ZwlrForeignToplevelHandleV1
		if p := m.handles[now.parent]; now.parent != nil && p != nil {
			parent = p.resource
		}
		errs = append(errs, res.SendParent(parent))
	}
	errs = append(errs, res.SendDone())
	h.sent, h.fresh = now, false
	return errors.Join(errs...)
}

func (c *Compositor) gone[H any](handles map[*window]H) []*window {
	var out []*window
	for w := range handles {
		if !slices.Contains(c.windows, w) {
			out = append(out, w)
		}
	}
	slices.SortFunc(out, func(a, b *window) int { return cmp.Compare(a.id, b.id) })
	return out
}

func (c *Compositor) syncForeign() error {
	var errs []error
	for _, l := range c.extLists {
		errs = append(errs, c.syncExtList(l))
	}
	for _, m := range c.wlrManagers {
		errs = append(errs, c.syncWlrManager(m))
	}
	return errors.Join(errs...)
}

type wlrHandleRequests struct {
	c *Compositor
	w *window
}

func (r wlrHandleRequests) live() bool {
	return slices.Contains(r.c.windows, r.w)
}

func (r wlrHandleRequests) Activate(*swft.ZwlrForeignToplevelHandleV1, *sw.WlSeat) error {
	if !r.live() {
		return nil
	}
	return r.c.focusWindow(r.w)
}

func (r wlrHandleRequests) Close(*swft.ZwlrForeignToplevelHandleV1) error {
	if !r.live() {
		return nil
	}
	return r.w.top.close()
}

func (r wlrHandleRequests) SetFullscreen(*swft.ZwlrForeignToplevelHandleV1, *sw.WlOutput) error {
	if !r.live() {
		return nil
	}
	return r.c.update(r.c.wm.SetFullscreen(r.w.id, true))
}

func (r wlrHandleRequests) UnsetFullscreen(*swft.ZwlrForeignToplevelHandleV1) error {
	if !r.live() {
		return nil
	}
	return r.c.update(r.c.wm.SetFullscreen(r.w.id, false))
}

func (wlrHandleRequests) SetMaximized(*swft.ZwlrForeignToplevelHandleV1) error   { return nil }
func (wlrHandleRequests) UnsetMaximized(*swft.ZwlrForeignToplevelHandleV1) error { return nil }
func (wlrHandleRequests) SetMinimized(*swft.ZwlrForeignToplevelHandleV1) error   { return nil }
func (wlrHandleRequests) UnsetMinimized(*swft.ZwlrForeignToplevelHandleV1) error { return nil }

func (wlrHandleRequests) SetRectangle(*swft.ZwlrForeignToplevelHandleV1, *sw.WlSurface, int32, int32, int32, int32) error {
	return nil
}

func (wlrHandleRequests) Destroy(*swft.ZwlrForeignToplevelHandleV1) error {
	return nil
}
