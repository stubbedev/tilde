{
  pkgs,
  lys-wm,
  lys-ui,
}:
lys-wm.overrideAttrs (old: {
  pname = "lys-wm-headless";
  doCheck = true;
  nativeCheckInputs = import ./test-programs.nix {inherit pkgs lys-ui;};
  env = old.env // import ./test-env.nix pkgs;
  checkPhase = ''
    runHook preCheck
    export HOME="$TMPDIR"
    just headless
    runHook postCheck
  '';
})
