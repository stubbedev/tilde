pkgs: {
  LYS_WM_LIBXKBCOMMON = "${pkgs.libxkbcommon}/lib/libxkbcommon.so.0";
  LYS_WM_TEST_XCURSOR_PATH = "${pkgs.vanilla-dmz}/share/icons";
  FONTCONFIG_FILE = pkgs.makeFontsConf {fontDirectories = [pkgs.dejavu_fonts];};
  LYS_WM_FONT = "${pkgs.dejavu_fonts}/share/fonts/truetype/DejaVuSans.ttf";
  LYS_WM_LIBEGL = "${pkgs.libglvnd}/lib/libEGL.so.1";
  LYS_WM_LIBGLESV2 = "${pkgs.libglvnd}/lib/libGLESv2.so.2";
  LYS_WM_LIBGBM = "${pkgs.libgbm}/lib/libgbm.so.1";
  LYS_WM_PNP_IDS = "${pkgs.hwdata}/share/hwdata/pnp.ids";
  LYS_WM_LIBINPUT = "${pkgs.libinput.out}/lib/libinput.so.10";
  LYS_WM_XWAYLAND = "${pkgs.xwayland}/bin/Xwayland";
  __EGL_VENDOR_LIBRARY_DIRS = "${pkgs.mesa}/share/glvnd/egl_vendor.d";
  LIBGL_ALWAYS_SOFTWARE = "1";
}
