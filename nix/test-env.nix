pkgs: {
  TILDE_LIBXKBCOMMON = "${pkgs.libxkbcommon}/lib/libxkbcommon.so.0";
  TILDE_TEST_XCURSOR_PATH = "${pkgs.vanilla-dmz}/share/icons";
  FONTCONFIG_FILE = pkgs.makeFontsConf {fontDirectories = [pkgs.dejavu_fonts];};
  TILDE_FONT = "${pkgs.dejavu_fonts}/share/fonts/truetype/DejaVuSans.ttf";
  TILDE_LIBEGL = "${pkgs.libglvnd}/lib/libEGL.so.1";
  TILDE_LIBGLESV2 = "${pkgs.libglvnd}/lib/libGLESv2.so.2";
  TILDE_LIBGBM = "${pkgs.libgbm}/lib/libgbm.so.1";
  TILDE_PNP_IDS = "${pkgs.hwdata}/share/hwdata/pnp.ids";
  TILDE_LIBINPUT = "${pkgs.libinput.out}/lib/libinput.so.10";
  TILDE_XWAYLAND = "${pkgs.xwayland}/bin/Xwayland";
  __EGL_VENDOR_LIBRARY_DIRS = "${pkgs.mesa}/share/glvnd/egl_vendor.d";
  LIBGL_ALWAYS_SOFTWARE = "1";
}
