package shell

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	sw "github.com/stubbedev/tilde/internal/protocol/server/wayland"
	sxf1 "github.com/stubbedev/tilde/internal/protocol/server/xdgforeignv1"
	sxf2 "github.com/stubbedev/tilde/internal/protocol/server/xdgforeignv2"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/surface"
	"github.com/stubbedev/tilde/internal/wire"
)

const foreignVersion = 1

func randomHandle() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("shell: generate a handle: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func xdgSurfaceOf(wl *sw.WlSurface) (*XdgSurface, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	xs, _ := s.Role().(*XdgSurface)
	return xs, nil
}

func toplevelOf(wl *sw.WlSurface) (*Toplevel, error) {
	xs, err := xdgSurfaceOf(wl)
	if err != nil || xs == nil {
		return nil, err
	}
	top, _ := xs.role.(*Toplevel)
	return top, nil
}

type export struct {
	handle  string
	xs      *XdgSurface
	imports []*imported
}

func (e *export) toplevel() *Toplevel {
	top, _ := e.xs.role.(*Toplevel)
	return top
}

type imported struct {
	sh        *Shell
	export    *export
	children  []*Toplevel
	destroyed func() error
}

func (sh *Shell) export(xs *XdgSurface) (*export, error) {
	handle, err := randomHandle()
	if err != nil {
		return nil, err
	}
	e := &export{handle: handle, xs: xs}
	sh.exports[handle] = e
	xs.exports = append(xs.exports, e)
	return e, nil
}

func (sh *Shell) unexport(e *export) error {
	if sh.exports[e.handle] != e {
		return nil
	}
	delete(sh.exports, e.handle)
	e.xs.exports = slices.DeleteFunc(e.xs.exports, func(x *export) bool { return x == e })
	errs := make([]error, 0, 2*len(e.imports))
	for _, imp := range e.imports {
		imp.export = nil
		errs = append(errs, imp.unparent(e.toplevel()), imp.destroyed())
	}
	e.imports = nil
	return errors.Join(errs...)
}

func (sh *Shell) toplevelGone(t *Toplevel) error {
	var errs []error
	for _, e := range slices.Clone(t.xs.exports) {
		errs = append(errs, sh.unexport(e))
	}
	return errors.Join(errs...)
}

func (sh *Shell) importHandle(c *server.Client, res wire.Resource, handle string, destroyed func() error) (*imported, error) {
	imp := &imported{sh: sh, destroyed: destroyed}
	c.OnDestroy(res, func() error {
		if imp.export != nil {
			imp.export.imports = slices.DeleteFunc(imp.export.imports, func(x *imported) bool { return x == imp })
			return imp.unparent(imp.export.toplevel())
		}
		return nil
	})
	e, ok := sh.exports[handle]
	if !ok {
		return imp, destroyed()
	}
	imp.export = e
	e.imports = append(e.imports, imp)
	return imp, nil
}

func (imp *imported) unparent(parent *Toplevel) error {
	var errs []error
	for _, child := range imp.children {
		if parent != nil && !child.destroyed && child.parent == parent {
			child.parent = nil
			errs = append(errs, imp.sh.listener.ToplevelChanged(child, ChangedParent))
		}
	}
	imp.children = nil
	return errors.Join(errs...)
}

func (imp *imported) setParentOf(child *Toplevel) error {
	if imp.export == nil {
		return nil
	}
	parent := imp.export.toplevel()
	if parent == nil {
		return nil
	}
	for a := parent; a != nil; a = a.parent {
		if a == child {
			return nil
		}
	}
	child.parent = parent
	if !slices.Contains(imp.children, child) {
		imp.children = append(imp.children, child)
	}
	return imp.sh.listener.ToplevelChanged(child, ChangedParent)
}

func (sh *Shell) bindExporterV2(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxf2.NewZxdgExporterV2(c, id, version, exporterV2{sh, c})
	return err
}

type exporterV2 struct {
	sh *Shell
	c  *server.Client
}

func (exporterV2) Destroy(*sxf2.ZxdgExporterV2) error { return nil }

func (x exporterV2) ExportToplevel(obj *sxf2.ZxdgExporterV2, res *sxf2.ZxdgExportedV2, wl *sw.WlSurface) (sxf2.ZxdgExportedV2Handler, error) {
	top, err := toplevelOf(wl)
	if err != nil {
		return nil, err
	}
	if top == nil {
		return nil, obj.ProtocolError(sxf2.ZxdgExporterV2ErrorInvalidSurface, fmt.Sprintf("wl_surface@%d is not an xdg_toplevel", wl.Base().ID()))
	}
	e, err := x.sh.export(top.xs)
	if err != nil {
		return nil, err
	}
	x.c.OnDestroy(res, func() error { return x.sh.unexport(e) })
	return exportedV2{}, x.c.Sent(res.SendHandle(e.handle))
}

type exportedV2 struct{}

func (exportedV2) Destroy(*sxf2.ZxdgExportedV2) error { return nil }

func (sh *Shell) bindImporterV2(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxf2.NewZxdgImporterV2(c, id, version, importerV2{sh, c})
	return err
}

type importerV2 struct {
	sh *Shell
	c  *server.Client
}

func (importerV2) Destroy(*sxf2.ZxdgImporterV2) error { return nil }

func (x importerV2) ImportToplevel(_ *sxf2.ZxdgImporterV2, res *sxf2.ZxdgImportedV2, handle string) (sxf2.ZxdgImportedV2Handler, error) {
	imp, err := x.sh.importHandle(x.c, res, handle, func() error { return x.c.Sent(res.SendDestroyed()) })
	return importedV2{imp}, err
}

type importedV2 struct{ *imported }

func (importedV2) Destroy(*sxf2.ZxdgImportedV2) error { return nil }

func (x importedV2) SetParentOf(obj *sxf2.ZxdgImportedV2, wl *sw.WlSurface) error {
	child, err := toplevelOf(wl)
	if err != nil {
		return err
	}
	if child == nil {
		return obj.ProtocolError(sxf2.ZxdgImportedV2ErrorInvalidSurface, fmt.Sprintf("wl_surface@%d is not an xdg_toplevel", wl.Base().ID()))
	}
	return x.setParentOf(child)
}

func (sh *Shell) bindExporterV1(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxf1.NewZxdgExporterV1(c, id, version, exporterV1{sh, c})
	return err
}

type exporterV1 struct {
	sh *Shell
	c  *server.Client
}

func (exporterV1) Destroy(*sxf1.ZxdgExporterV1) error { return nil }

func (x exporterV1) Export(obj *sxf1.ZxdgExporterV1, res *sxf1.ZxdgExportedV1, wl *sw.WlSurface) (sxf1.ZxdgExportedV1Handler, error) {
	xs, err := xdgSurfaceOf(wl)
	if err != nil {
		return nil, err
	}
	if xs == nil {
		return nil, &wire.ProtocolError{Object: obj.Base().ID(), Message: fmt.Sprintf("wl_surface@%d is not an xdg_surface", wl.Base().ID())}
	}
	e, err := x.sh.export(xs)
	if err != nil {
		return nil, err
	}
	x.c.OnDestroy(res, func() error { return x.sh.unexport(e) })
	return exportedV1{}, x.c.Sent(res.SendHandle(e.handle))
}

type exportedV1 struct{}

func (exportedV1) Destroy(*sxf1.ZxdgExportedV1) error { return nil }

func (sh *Shell) bindImporterV1(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxf1.NewZxdgImporterV1(c, id, version, importerV1{sh, c})
	return err
}

type importerV1 struct {
	sh *Shell
	c  *server.Client
}

func (importerV1) Destroy(*sxf1.ZxdgImporterV1) error { return nil }

func (x importerV1) Import(_ *sxf1.ZxdgImporterV1, res *sxf1.ZxdgImportedV1, handle string) (sxf1.ZxdgImportedV1Handler, error) {
	imp, err := x.sh.importHandle(x.c, res, handle, func() error { return x.c.Sent(res.SendDestroyed()) })
	return importedV1{imp}, err
}

type importedV1 struct{ *imported }

func (importedV1) Destroy(*sxf1.ZxdgImportedV1) error { return nil }

func (x importedV1) SetParentOf(obj *sxf1.ZxdgImportedV1, wl *sw.WlSurface) error {
	child, err := toplevelOf(wl)
	if err != nil {
		return err
	}
	if child == nil {
		return &wire.ProtocolError{Object: obj.Base().ID(), Message: fmt.Sprintf("wl_surface@%d is not an xdg_toplevel", wl.Base().ID())}
	}
	return x.setParentOf(child)
}
