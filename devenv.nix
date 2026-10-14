{
  pkgs,
  inputs,
  ...
}: let
  gelm = inputs.gelm.packages.${pkgs.stdenv.hostPlatform.system}.gelm.overrideAttrs {tags = ["gelmdebug"];};
  alacritty-swrast = pkgs.writeShellScriptBin "alacritty-swrast" ''
    export __EGL_VENDOR_LIBRARY_DIRS=${pkgs.mesa}/share/glvnd/egl_vendor.d
    export LIBGL_ALWAYS_SOFTWARE=1
    exec ${pkgs.alacritty}/bin/alacritty "$@"
  '';
in {
  env = {
    CGO_ENABLED = "0";
    GOTOOLCHAIN = "local";
    TILDE_LIBXKBCOMMON = "${pkgs.libxkbcommon}/lib/libxkbcommon.so.0";
    TILDE_TEST_XCURSOR_PATH = "${pkgs.vanilla-dmz}/share/icons";
    FONTCONFIG_FILE = pkgs.makeFontsConf {fontDirectories = [pkgs.dejavu_fonts];};
    TILDE_FONT = "${pkgs.dejavu_fonts}/share/fonts/truetype/DejaVuSans.ttf";
  };

  packages = with pkgs; [
    go_1_27
    gopls
    golangci-lint
    delve
    govulncheck
    just
    git
    gh
    wayland-utils
    sway
    grim
    libxkbcommon
    weston
    foot
    wl-clipboard
    dbus
    gelm
    alacritty-swrast
  ];

  enterTest = ''
    just gates
  '';
}
