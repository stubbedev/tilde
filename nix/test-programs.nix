{
  pkgs,
  gelm,
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
  (import ./gelm-debug.nix gelm)
  (import ./alacritty-swrast.nix pkgs)
]
