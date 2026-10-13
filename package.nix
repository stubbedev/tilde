{
  lib,
  buildGo127Module,
  libxkbcommon,
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
  vendorHash = "sha256-sgxRd4lPFK6uaLHUU6qUAX0Ak8wXksx9ha0Z1+KFSaE=";

  subPackages = ["cmd/tilde"];

  # purego loads these at runtime; the store paths are baked in so the
  # binary needs no library search path.
  ldflags = [
    "-X github.com/stubbedev/tilde/internal/xkb.defaultLibrary=${libxkbcommon}/lib/libxkbcommon.so.0"
  ];

  env.CGO_ENABLED = 0;

  # The test suite is pure Go and safe to run in the sandbox.
  doCheck = true;

  meta = {
    description = "A pure-Go tiling Wayland compositor";
    homepage = "https://github.com/stubbedev/tilde";
    license = lib.licenses.mit;
    mainProgram = "tilde";
    platforms = lib.platforms.unix;
  };
}
