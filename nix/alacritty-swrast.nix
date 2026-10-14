pkgs: let
  env = import ./test-env.nix pkgs;
in
  pkgs.writeShellScriptBin "alacritty-swrast" ''
    export __EGL_VENDOR_LIBRARY_DIRS=${env.__EGL_VENDOR_LIBRARY_DIRS}
    export LIBGL_ALWAYS_SOFTWARE=${env.LIBGL_ALWAYS_SOFTWARE}
    exec ${pkgs.alacritty}/bin/alacritty "$@"
  ''
