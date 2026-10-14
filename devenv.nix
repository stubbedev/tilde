{
  pkgs,
  inputs,
  ...
}: let
  gelm = inputs.gelm.packages.${pkgs.stdenv.hostPlatform.system}.gelm.overrideAttrs {tags = ["gelmdebug"];};
in {
  env = {
    CGO_ENABLED = "0";
    GOTOOLCHAIN = "local";
    TILDE_LIBXKBCOMMON = "${pkgs.libxkbcommon}/lib/libxkbcommon.so.0";
    TILDE_TEST_XCURSOR_PATH = "${pkgs.vanilla-dmz}/share/icons";
    FONTCONFIG_FILE = pkgs.makeFontsConf {fontDirectories = [pkgs.dejavu_fonts];};
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
    gelm
  ];

  enterTest = ''
    just gates
  '';
}
