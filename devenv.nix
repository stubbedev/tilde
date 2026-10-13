{pkgs, ...}: {
  env = {
    CGO_ENABLED = "0";
    GOTOOLCHAIN = "local";
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
  ];

  enterTest = ''
    just gates
  '';
}
