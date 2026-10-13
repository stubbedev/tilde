{pkgs, ...}: {
  env = {
    CGO_ENABLED = "0";
    GOTOOLCHAIN = "local";
    TILDE_LIBXKBCOMMON = "${pkgs.libxkbcommon}/lib/libxkbcommon.so.0";
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
  ];

  enterTest = ''
    just gates
  '';
}
