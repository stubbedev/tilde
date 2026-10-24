package compositor

import (
	"github.com/stubbedev/lys-wm/internal/gammacontrol"
	"github.com/stubbedev/lys-wm/internal/inputmethod"
	sedc "github.com/stubbedev/lys-wm/internal/protocol/server/extdatacontrol"
	sefl "github.com/stubbedev/lys-wm/internal/protocol/server/extforeigntoplevellist"
	seis "github.com/stubbedev/lys-wm/internal/protocol/server/extimagecapturesource"
	seic "github.com/stubbedev/lys-wm/internal/protocol/server/extimagecopycapture"
	sesl "github.com/stubbedev/lys-wm/internal/protocol/server/extsessionlock"
	sew "github.com/stubbedev/lys-wm/internal/protocol/server/extworkspace"
	shgs "github.com/stubbedev/lys-wm/internal/protocol/server/hyprlandglobalshortcuts"
	sksi "github.com/stubbedev/lys-wm/internal/protocol/server/keyboardshortcutsinhibit"
	svk "github.com/stubbedev/lys-wm/internal/protocol/server/virtualkeyboard"
	swdc "github.com/stubbedev/lys-wm/internal/protocol/server/wlrdatacontrol"
	swft "github.com/stubbedev/lys-wm/internal/protocol/server/wlrforeigntoplevel"
	swlr "github.com/stubbedev/lys-wm/internal/protocol/server/wlrlayershell"
	swom "github.com/stubbedev/lys-wm/internal/protocol/server/wlroutputmanagement"
	swop "github.com/stubbedev/lys-wm/internal/protocol/server/wlroutputpower"
	swsc "github.com/stubbedev/lys-wm/internal/protocol/server/wlrscreencopy"
	svp "github.com/stubbedev/lys-wm/internal/protocol/server/wlrvirtualpointer"
	"github.com/stubbedev/lys-wm/internal/securitycontext"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
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
	inputmethod.Interface,
}

func sandbox(display *server.Display) error {
	display.Restrict(privileged...)
	return securitycontext.New(display)
}
