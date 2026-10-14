{
  lib,
  buildGo127Module,
  libxkbcommon,
  libglvnd,
  libgbm,
  hwdata,
  libinput,
  xwayland,
  dejavu_fonts,
  # Set by the flake from `self.shortRev or self.dirtyShortRev`; falls back to
  # the placeholder used when nothing was stamped in.
  rev ? "unknown",
  version ? "0-unstable",
}:
buildGo127Module {
  pname = "tilde";
  inherit version;

  src = lib.cleanSource ./.;

  # Pins the module dependencies in go.sum. When they change, set this to
  # lib.fakeHash, run `nix build` and put the reported got: hash here.
  vendorHash = "sha256-QWiDJazSvv1R6QpEAyEWf6oZsmsRIU3D1HIXrVXq884=";

  subPackages = ["cmd/tilde" "cmd/tildectl"];

  # purego loads these at runtime; the store paths are baked in so the
  # binary needs no library search path.
  ldflags = [
    "-X github.com/stubbedev/tilde/internal/xkb.defaultLibrary=${libxkbcommon}/lib/libxkbcommon.so.0"
    "-X github.com/stubbedev/tilde/internal/gpu.defaultEGL=${libglvnd}/lib/libEGL.so.1"
    "-X github.com/stubbedev/tilde/internal/gpu.defaultGLESv2=${libglvnd}/lib/libGLESv2.so.2"
    "-X github.com/stubbedev/tilde/internal/gpu.defaultGBM=${libgbm}/lib/libgbm.so.1"
    "-X github.com/stubbedev/tilde/internal/edid.defaultPNP=${hwdata}/share/hwdata/pnp.ids"
    "-X github.com/stubbedev/tilde/internal/session.defaultLibinput=${libinput.out}/lib/libinput.so.10"
    "-X github.com/stubbedev/tilde/internal/xwayland.defaultXwayland=${xwayland}/bin/Xwayland"
    "-X main.defaultFont=${dejavu_fonts}/share/fonts/truetype/DejaVuSans.ttf"
  ];

  env.CGO_ENABLED = 0;

  postInstall = ''
    install -Dm644 packaging/systemd/tilde-session.target $out/share/systemd/user/tilde-session.target
    install -Dm644 packaging/xdg-desktop-portal/tilde-portals.conf $out/share/xdg-desktop-portal/tilde-portals.conf
    install -Dm644 packaging/wayland-sessions/tilde.desktop $out/share/wayland-sessions/tilde.desktop
    substituteInPlace $out/share/wayland-sessions/tilde.desktop --replace-fail "Exec=tilde" "Exec=$out/bin/tilde"
  '';

  # The test suite is pure Go and safe to run in the sandbox.
  doCheck = true;

  passthru.providedSessions = ["tilde"];

  meta = {
    description = "A pure-Go tiling Wayland compositor";
    homepage = "https://github.com/stubbedev/tilde";
    license = lib.licenses.mit;
    mainProgram = "tilde";
    platforms = lib.platforms.unix;
  };
}
