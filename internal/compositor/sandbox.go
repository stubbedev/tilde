package compositor

import (
	"github.com/stubbedev/tilde/internal/gammacontrol"
	sedc "github.com/stubbedev/tilde/internal/protocol/server/extdatacontrol"
	sefl "github.com/stubbedev/tilde/internal/protocol/server/extforeigntoplevellist"
	seis "github.com/stubbedev/tilde/internal/protocol/server/extimagecapturesource"
	seic "github.com/stubbedev/tilde/internal/protocol/server/extimagecopycapture"
	sesl "github.com/stubbedev/tilde/internal/protocol/server/extsessionlock"
	sew "github.com/stubbedev/tilde/internal/protocol/server/extworkspace"
	shgs "github.com/stubbedev/tilde/internal/protocol/server/hyprlandglobalshortcuts"
	sksi "github.com/stubbedev/tilde/internal/protocol/server/keyboardshortcutsinhibit"
	svk "github.com/stubbedev/tilde/internal/protocol/server/virtualkeyboard"
	swdc "github.com/stubbedev/tilde/internal/protocol/server/wlrdatacontrol"
	swft "github.com/stubbedev/tilde/internal/protocol/server/wlrforeigntoplevel"
	swlr "github.com/stubbedev/tilde/internal/protocol/server/wlrlayershell"
	swom "github.com/stubbedev/tilde/internal/protocol/server/wlroutputmanagement"
	swop "github.com/stubbedev/tilde/internal/protocol/server/wlroutputpower"
	swsc "github.com/stubbedev/tilde/internal/protocol/server/wlrscreencopy"
	svp "github.com/stubbedev/tilde/internal/protocol/server/wlrvirtualpointer"
	"github.com/stubbedev/tilde/internal/securitycontext"
	"github.com/stubbedev/tilde/internal/server"
	"github.com/stubbedev/tilde/internal/wire"
)

var privileged = []*wire.Interface{
	swsc.ZwlrScreencopyManagerV1Interface,
	seic.ExtImageCopyCaptureManagerV1Interface,
	seis.ExtOutputImageCaptureSourceManagerV1Interface,
	seis.ExtForeignToplevelImageCaptureSourceManagerV1Interface,
	swdc.ZwlrDataControlManagerV1Interface,
	sedc.ExtDataControlManagerV1Interface,
	swft.ZwlrForeignToplevelManagerV1Interface,
	sefl.ExtForeignToplevelListV1Interface,
	swom.ZwlrOutputManagerV1Interface,
	swop.ZwlrOutputPowerManagerV1Interface,
	svk.ZwpVirtualKeyboardManagerV1Interface,
	svp.ZwlrVirtualPointerManagerV1Interface,
	sesl.ExtSessionLockManagerV1Interface,
	swlr.ZwlrLayerShellV1Interface,
	sew.ExtWorkspaceManagerV1Interface,
	shgs.HyprlandGlobalShortcutsManagerV1Interface,
	sksi.ZwpKeyboardShortcutsInhibitManagerV1Interface,
	securitycontext.Interface,
	gammacontrol.Interface,
}

func sandbox(display *server.Display) error {
	display.Restrict(privileged...)
	return securitycontext.New(display)
}
