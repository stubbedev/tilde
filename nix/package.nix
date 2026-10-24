{self, ...}: {
  perSystem = {
    lib,
    pkgs,
    self',
    ...
  }: {
    packages = {
      default = self'.packages.lys-wm;
      lys-wm = pkgs.buildGo127Module {
        pname = "lys-wm";
        # Nix has no version to read from the source tree, so builds from a
        # checkout are stamped with the commit date.
        version = "0-unstable-${self.lastModifiedDate or "0"}";

        src = lib.fileset.toSource {
          root = ../.;
          fileset = lib.fileset.difference (lib.fileset.gitTracked ../.) (
            lib.fileset.unions [
              ../flake.nix
              ../flake.lock
              ../nix
            ]
          );
        };

        # Pins the module dependencies in go.sum. When they change, set this to
        # lib.fakeHash, run `nix build` and put the reported got: hash here.
        vendorHash = "sha256-ZLUNfzAfEAbK9QeC3BGkM/HB4P3AM894zlITZIcpe4U=";

        subPackages = ["cmd/lys-wm" "cmd/lys-wmctl"];

        # purego loads these at runtime; the store paths are baked in so the
        # binary needs no library search path.
        ldflags = with pkgs; [
          "-X github.com/stubbedev/lys-wm/internal/xkb.defaultLibrary=${libxkbcommon}/lib/libxkbcommon.so.0"
          "-X github.com/stubbedev/lys-wm/internal/gpu.defaultEGL=${libglvnd}/lib/libEGL.so.1"
          "-X github.com/stubbedev/lys-wm/internal/gpu.defaultGLESv2=${libglvnd}/lib/libGLESv2.so.2"
          "-X github.com/stubbedev/lys-wm/internal/gpu.defaultGBM=${libgbm}/lib/libgbm.so.1"
          "-X github.com/stubbedev/lys-wm/internal/edid.defaultPNP=${hwdata}/share/hwdata/pnp.ids"
          "-X github.com/stubbedev/lys-wm/internal/session.defaultLibinput=${libinput.out}/lib/libinput.so.10"
          "-X github.com/stubbedev/lys-wm/internal/xwayland.defaultXwayland=${xwayland}/bin/Xwayland"
          "-X main.defaultFont=${dejavu_fonts}/share/fonts/truetype/DejaVuSans.ttf"
        ];

        env.CGO_ENABLED = 0;

        postInstall = ''
          install -Dm644 packaging/systemd/lys-wm-session.target $out/share/systemd/user/lys-wm-session.target
          install -Dm644 packaging/xdg-desktop-portal/lys-wm-portals.conf $out/share/xdg-desktop-portal/lys-wm-portals.conf
          install -Dm644 packaging/wayland-sessions/lys-wm.desktop $out/share/wayland-sessions/lys-wm.desktop
          substituteInPlace $out/share/wayland-sessions/lys-wm.desktop --replace-fail "Exec=lys-wm" "Exec=$out/bin/lys-wm"
        '';

        # The test suite is pure Go and safe to run in the sandbox.
        doCheck = true;

        passthru.providedSessions = ["lys-wm"];

        meta = {
          description = "A pure-Go tiling Wayland compositor";
          homepage = "https://github.com/stubbedev/lys-wm";
          license = lib.licenses.mit;
          mainProgram = "lys-wm";
          platforms = lib.platforms.unix;
        };
      };
    };

    apps = {
      default = self'.apps.lys-wm;
      lys-wm = {
        type = "app";
        program = lib.getExe self'.packages.lys-wm;
      };
    };
  };
}
