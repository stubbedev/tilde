{
  pkgs,
  lys-ui,
}:
with pkgs; [
  just
  wayland-utils
  sway
  grim
  libxkbcommon
  weston
  foot
  wl-clipboard
  dbus
  (import ./lys-ui-debug.nix lys-ui)
  (import ./alacritty-swrast.nix pkgs)
]
